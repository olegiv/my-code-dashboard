// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/engine"
	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/secret"
	"github.com/olegiv/my-code-dashboard/internal/store"
)

func TestVersionHelpAndUnsafeListener(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"version"}, &out); err != nil || !strings.Contains(out.String(), "0.1.0") {
		t.Fatal("version command failed")
	}
	out.Reset()
	if err := run([]string{"--help"}, &out); err != nil || !strings.Contains(out.String(), "-demo") {
		t.Fatal("help command failed")
	}
	for _, addr := range []string{"0.0.0.0:7364", ":7364", "192.0.2.1:7364"} {
		if err := run([]string{"serve", "--data-dir", filepath.Join(t.TempDir(), "data"), "--addr", addr}, &out); err == nil {
			t.Fatalf("public listener %s allowed", addr)
		}
	}
	if err := run([]string{"restore", "--data-dir", t.TempDir()}, &out); err == nil {
		t.Fatal("restore without archive accepted")
	}
	if err := run([]string{"connection", "--data-dir", t.TempDir()}, &out); err == nil {
		t.Fatal("unprovisioned headless connection accepted")
	}
}

func TestHeadlessCredentialIDCannotChangeProviderTarget(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	c := model.Connection{ID: "shared-id", Provider: "gitlab", BaseURL: "https://gitlab.example.com", CreatedAt: time.Now().UTC()}
	if err = s.SaveConnection(c); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, target := range []struct{ provider, base string }{{"gitlab", "https://another.example.com"}, {"github", "https://github.com"}} {
		var out bytes.Buffer
		err = run([]string{"connection", "--data-dir", dir, "--credential-dir", filepath.Join(dir, "missing-credentials"), "--credential-id", c.ID, "--name", "Synthetic", "--provider", target.provider, "--base-url", target.base}, &out)
		if err == nil || !strings.Contains(err.Error(), "different credential ID") {
			t.Fatalf("changed provider target reached credential lookup: %v", err)
		}
	}
	s, err = store.Open(filepath.Join(dir, "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	same, fresh := c, model.Connection{ID: "new-id", Provider: "github", BaseURL: "https://github.com"}
	for _, candidate := range []*model.Connection{&same, &fresh} {
		if err = preserveConnectionTarget(s, candidate); err != nil {
			t.Fatal(err)
		}
	}
	persisted, err := s.Connection(c.ID)
	if err != nil || persisted.Provider != c.Provider || persisted.BaseURL != c.BaseURL {
		t.Fatal("rejected target change modified the original connection")
	}
}

type addressWriter chan string

func (w addressWriter) Write(p []byte) (int, error) {
	if _, after, found := strings.Cut(string(p), "Local: "); found {
		w <- strings.TrimSpace(after)
	}
	return len(p), nil
}

func TestServeDemoAndShutdown(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e := engine.New(ctx, s, secret.NewMemory(), true)
	addresses := make(addressWriter, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- serve(ctx, cancel, s, e, serveOptions{dataDir: dir, addr: "127.0.0.1:0", demo: true, noSchedule: true}, addresses)
	}()
	var address string
	select {
	case address = <-addresses:
	case err := <-finished:
		t.Fatalf("serve exited before listening: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not start")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Synthetic") {
		t.Fatalf("demo response: status=%d error=%v", response.StatusCode, err)
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down")
	}
	if _, err := client.Get(address + "/healthz"); err == nil {
		t.Fatal("listener still accepts requests after shutdown")
	}
}

func TestServeWithLongRunningBrowserOpener(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("exercises the Linux xdg-open command")
	}
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate exit", true: "browser still running"}[blocked], func(t *testing.T) {
			dir := t.TempDir()
			opener := filepath.Join(dir, "xdg-open")
			const script = `#!/bin/sh
opener_dir=${0%/*}
printf '%s\n' "$1" > "$opener_dir/started"
while [ ! -e "$opener_dir/release" ]; do /bin/sleep 0.02; done
: > "$opener_dir/exited"
`
			if err := os.WriteFile(opener, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			release := func() {
				if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
					t.Error(err)
				}
			}
			if !blocked {
				release()
			}
			waitForFile := func(name string) bool {
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
						return true
					}
					time.Sleep(10 * time.Millisecond)
				}
				return false
			}
			s, err := store.Open(filepath.Join(dir, "dashboard.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			e := engine.New(ctx, s, secret.NewMemory(), true)
			addresses := make(addressWriter, 1)
			finished := make(chan error, 1)
			go func() {
				finished <- serve(ctx, cancel, s, e, serveOptions{dataDir: dir, addr: "127.0.0.1:0", demo: true, noSchedule: true, openBrowser: true}, addresses)
				close(finished)
			}()
			t.Cleanup(func() {
				cancel()
				release()
				select {
				case err := <-finished:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(10 * time.Second):
					t.Error("serve did not shut down after releasing opener")
				}
				if !waitForFile("exited") {
					t.Error("fake opener did not exit")
				}
				s.Close()
			})
			var address string
			select {
			case address = <-addresses:
			case <-time.After(10 * time.Second):
				t.Fatal("serve did not start")
			}
			if !waitForFile("started") {
				t.Fatal("fake opener was not launched")
			}
			client := &http.Client{Timeout: time.Second}
			response, err := client.Get(address + "/healthz")
			if err != nil {
				t.Fatalf("HTTP serving blocked by browser opener: %v", err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("health status = %d", response.StatusCode)
			}
			cancel()
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown blocked by browser opener")
			}
			if blocked {
				if _, err := os.Stat(filepath.Join(dir, "exited")); !os.IsNotExist(err) {
					t.Fatal("opener exited before the release signal")
				}
			}
		})
	}
}
