// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/olegiv/my-code-dashboard/internal/engine"
	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/platform"
	"github.com/olegiv/my-code-dashboard/internal/provider"
	"github.com/olegiv/my-code-dashboard/internal/secret"
	"github.com/olegiv/my-code-dashboard/internal/store"
	"github.com/olegiv/my-code-dashboard/internal/web"
)

var version = "0.2.0-dev"

// commit identifies the source of a development build; the Makefile sets it.
var commit = ""

// displayVersion appends a development build's commit as semver build metadata.
func displayVersion() string {
	if commit == "" {
		return version
	}
	return version + "+" + commit
}

const usageHeader = `Usage: my-code-dashboard [command] [flags]

Commands:
  serve        Run the dashboard (default)
  backup       Write a backup archive (--output) while the app is stopped
  restore      Restore a backup archive (--archive) while the app is stopped
  connection   Register a headless connection from a provisioned credential file
  service      Install or remove the macOS start-at-login agent (--action)
  version      Print the version

Flags:
`

func main() {
	syscall.Umask(0077)
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "my-code-dashboard:", err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	if command == "version" || (len(args) == 1 && args[0] == "--version") {
		fmt.Fprintln(out, displayVersion())
		return nil
	}
	defaultDir, err := platform.DataDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(out)
	flags.Usage = func() {
		fmt.Fprint(out, usageHeader)
		flags.PrintDefaults()
	}
	dir := flags.String("data-dir", defaultDir, "Private installation data directory")
	addr := flags.String("addr", "127.0.0.1:7364", "Loopback address and port")
	demo := flags.Bool("demo", false, "Use synthetic data, no credentials, and a temporary database")
	openBrowser := flags.Bool("open", false, "Open the local dashboard in your browser")
	noSchedule := flags.Bool("no-scheduler", false, "Disable the scheduler for this process")
	credentialDir := flags.String("credential-dir", os.Getenv("CREDENTIALS_DIRECTORY"), "Read-only systemd/mounted credential directory (headless)")
	archive := flags.String("archive", "", "Backup archive to restore")
	output := flags.String("output", "", "Destination backup archive")
	connectionProvider := flags.String("provider", "github", "Connection provider for the headless connection command")
	connectionURL := flags.String("base-url", "https://github.com", "Provider HTTPS URL for the headless connection command")
	connectionName := flags.String("name", "", "Connection name for the headless connection command")
	credentialID := flags.String("credential-id", "", "Provisioned credential filename for the headless connection command")
	serviceAction := flags.String("action", "install", "Service action: install or remove")
	if err = flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional argument; use --help")
	}
	switch command {
	case "serve", "backup", "restore", "connection", "service":
	default:
		return errors.New("unknown command; use serve, backup, restore, connection, service, or version")
	}
	if *demo && command != "serve" {
		return errors.New("demo mode is only available for serve")
	}
	if *demo && *dir == defaultDir {
		*dir, err = os.MkdirTemp("", "my-code-dashboard-demo-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(*dir)
	}
	*dir, err = filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if command == "service" && *serviceAction == "remove" {
		settings, err := store.ReadSettings(filepath.Join(*dir, "dashboard.sqlite"))
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err = platform.RemoveService(ctx, settings.InstallationID); err != nil {
			return err
		}
		return platform.StopService(ctx, settings.InstallationID)
	}
	if command == "serve" || command == "service" {
		if err = validateAddress(*addr); err != nil {
			return err
		}
	}
	lock, err := platform.Acquire(*dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	marker := filepath.Join(*dir, ".demo")
	if *demo {
		if _, e := os.Stat(filepath.Join(*dir, "dashboard.sqlite")); e == nil {
			if _, e = os.Stat(marker); e != nil {
				return errors.New("refusing to use a real installation database in demo mode")
			}
		}
		if err = os.WriteFile(marker, []byte("Synthetic demonstration only\n"), 0600); err != nil {
			return err
		}
	} else if _, e := os.Stat(marker); e == nil {
		return errors.New("this data directory belongs to a demo installation")
	}
	if command == "restore" {
		if *archive == "" {
			return errors.New("restore requires --archive")
		}
		if err = platform.Restore(*dir, *archive); err != nil {
			return err
		}
		fmt.Fprintln(out, "Backup restored. Reconnect provider credentials before refreshing.")
		return nil
	}
	s, err := store.Open(filepath.Join(*dir, "dashboard.sqlite"))
	if err != nil {
		return err
	}
	defer s.Close()
	settings, err := s.Settings()
	if err != nil {
		return err
	}
	var secrets secret.Store = secret.Keyring{Namespace: settings.InstallationID}
	if *credentialDir != "" {
		secrets = secret.Files{Directory: *credentialDir}
	}
	if *demo {
		secrets = secret.NewMemory()
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	e := engine.New(ctx, s, secrets, *demo)
	switch command {
	case "backup":
		if *output == "" {
			return errors.New("backup requires --output")
		}
		f, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		err = platform.Export(ctx, s, f, displayVersion())
		closeErr := f.Close()
		if err != nil {
			_ = os.Remove(*output)
			return err
		}
		if closeErr != nil {
			_ = os.Remove(*output)
			return closeErr
		}
		fmt.Fprintln(out, "Backup created; provider credentials are excluded.")
		return nil
	case "connection":
		if *credentialDir == "" || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`).MatchString(*credentialID) || strings.TrimSpace(*connectionName) == "" {
			return errors.New("headless connection requires --credential-dir, --credential-id (safe filename), and --name")
		}
		base, err := provider.ValidateBase(*connectionURL)
		if err != nil {
			return err
		}
		c := model.Connection{ID: *credentialID, Name: *connectionName, Provider: *connectionProvider, BaseURL: base, CreatedAt: time.Now()}
		if err = connectHeadless(ctx, s, secrets, c); err != nil {
			return err
		}
		fmt.Fprintln(out, "Connection saved. Start the app, then discover and select repositories.")
		return nil
	case "service":
		if *serviceAction == "remove" {
			return platform.RemoveService(ctx, settings.InstallationID)
		}
		if *serviceAction != "install" {
			return errors.New("service --action must be install or remove")
		}
		if err = platform.InstallService(ctx, *dir, *addr, settings.InstallationID); err != nil {
			return err
		}
		fmt.Fprintln(out, "LaunchAgent installed.")
		return nil
	}
	return serve(ctx, cancel, s, e, serveOptions{*dir, *addr, *demo, *openBrowser, *noSchedule, *credentialDir != ""}, out)
}

type serveOptions struct {
	dataDir, addr                              string
	demo, openBrowser, noSchedule, secretFiles bool
}

func serve(ctx context.Context, cancel context.CancelFunc, s *store.Store, e *engine.Engine, options serveOptions, out io.Writer) error {
	host, _, err := net.SplitHostPort(options.addr)
	if err != nil {
		return errors.New("--addr must contain a loopback IP and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("public listeners are disabled; use a loopback IP address")
	}
	if err = s.InterruptRuns(time.Now()); err != nil {
		return err
	}
	if options.demo {
		if err = e.SeedDemo(); err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", options.addr)
	if err != nil {
		return err
	}
	actual := listener.Addr().String()
	app, err := web.New(e, options.dataDir, actual, displayVersion())
	if err != nil {
		listener.Close()
		return err
	}
	app.SecretFiles = options.secretFiles
	server := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32768}
	url := "http://" + actual
	fmt.Fprintf(out, "My Code Dashboard %s\nLocal: %s\n", displayVersion(), url)
	if options.demo {
		fmt.Fprintln(out, "Synthetic demo: no credentials or external API calls.")
	}
	if options.openBrowser {
		go func() {
			if runtime.GOOS == "darwin" {
				_ = exec.Command("/usr/bin/open", url).Run()
			} else {
				_ = exec.Command("xdg-open", url).Run()
			}
		}()
	}
	if !options.noSchedule {
		go e.Schedule(ctx)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = server.Shutdown(shutdownCtx)
	}()
	err = server.Serve(listener)
	cancel()
	<-stopped
	e.Wait()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func connectHeadless(ctx context.Context, s *store.Store, secrets secret.Store, c model.Connection) error {
	if err := preserveConnectionTarget(s, &c); err != nil {
		return err
	}
	token, err := secrets.Get(c.ID)
	if err != nil {
		return err
	}
	p, err := provider.New(c, token)
	if err != nil {
		return err
	}
	identity, err := p.Validate(ctx)
	if err != nil {
		return err
	}
	c.Identity = identity.Name
	c.Version = identity.Version
	c.Status = "connected"
	c.Message = "Using a provisioned read-only credential file."
	return s.SaveConnection(c)
}

func preserveConnectionTarget(s *store.Store, c *model.Connection) error {
	old, err := s.Connection(c.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	base, err := provider.ValidateBase(old.BaseURL)
	if err != nil || old.Provider != c.Provider || base != c.BaseURL {
		return errors.New("this connection belongs to another provider or URL; use a different credential ID")
	}
	c.CreatedAt = old.CreatedAt
	return nil
}

func validateAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return errors.New("--addr must contain a loopback IP and port")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return errors.New("--addr port must be a decimal number between 0 and 65535")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("public listeners are disabled; use a loopback IP address")
	}
	return nil
}
