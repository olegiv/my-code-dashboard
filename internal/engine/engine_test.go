// SPDX-License-Identifier: GPL-3.0-only
package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/provider"
	"github.com/olegiv/my-code-dashboard/internal/secret"
	"github.com/olegiv/my-code-dashboard/internal/store"
)

type fakeProvider struct {
	block, started chan struct{}
	failure        error
	retryAt        time.Time
	snapshots      []model.Snapshot
	metadata       func(model.Repository) (model.Repository, error)
}

func (f *fakeProvider) Validate(context.Context) (provider.Identity, error) {
	return provider.Identity{Name: "synthetic-user", Version: "synthetic"}, f.failure
}
func (f *fakeProvider) Discover(context.Context) ([]model.Repository, error) {
	return []model.Repository{{ExternalID: "1", Name: "example/repo", DefaultBranch: "main"}}, f.failure
}
func (f *fakeProvider) RefreshRepository(ctx context.Context, r model.Repository) (model.Repository, error) {
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.block != nil {
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-f.block:
		}
	}
	if f.metadata != nil {
		return f.metadata(r)
	}
	return r, nil
}

func TestInvalidMetadataRetainsFindingsAndValidBackups(t *testing.T) {
	for _, mode := range []string{"provider rejection", "store rejection", "normal rename"} {
		t.Run(mode, func(t *testing.T) {
			p := &fakeProvider{metadata: func(r model.Repository) (model.Repository, error) {
				r.Name = "example/renamed"
				switch mode {
				case "provider rejection":
					return r, &provider.Error{Kind: "error", Message: "Incomplete repository metadata."}
				case "store rejection":
					r.ExternalID = "0"
				}
				return r, nil
			}}
			e, _ := engineFixture(t, t.Context(), p)
			repos, _ := e.Store.Repositories()
			before := repos[0]
			if err := e.Store.Apply(before, model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code"}, Findings: []model.Finding{{ID: "1", Source: "synthetic", Title: "Retained active finding", State: "open", NativeState: "open"}}}, e.Now()); err != nil {
				t.Fatal(err)
			}
			if _, _, err := e.Start("manual"); err != nil {
				t.Fatal(err)
			}
			e.Wait()
			fresh, err := e.Store.Repository(before.ID)
			if err != nil || fresh.ExternalID != before.ExternalID {
				t.Fatal("refresh corrupted the repository identity")
			}
			findings, _ := e.Store.Findings()
			runs, _ := e.Store.Runs()
			if mode == "normal rename" {
				if fresh.Name != "example/renamed" || runs[0].Succeeded != 1 || runs[0].Failed != 0 || !findings[0].Missing {
					t.Fatal("valid metadata did not reach ordinary collection")
				}
			} else if fresh != before || len(findings) != 1 || findings[0].Missing || findings[0].State != "open" || runs[0].Failed != len(model.Categories) || runs[0].Outcomes[0].RepositoryName != before.Name {
				t.Fatal("metadata failure replaced prior findings or activity identity")
			}
			path := filepath.Join(t.TempDir(), "backup.sqlite")
			if err = e.Store.Backup(t.Context(), path); err != nil {
				t.Fatal(err)
			}
			if err = store.ValidateBackup(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func (f *fakeProvider) Collect(context.Context, model.Repository) []model.Snapshot {
	if f.snapshots != nil {
		return f.snapshots
	}
	if f.failure != nil {
		return []model.Snapshot{{Coverage: model.Coverage{Category: "code", Collection: "rate_limited", RetryAt: f.retryAt}}}
	}
	return []model.Snapshot{{Complete: true, Coverage: model.Coverage{Category: "code", ScannerState: "unknown"}}}
}

type delayedDiscovery struct {
	*fakeProvider
	started, release chan struct{}
	discoveryErr     error
}

func (p delayedDiscovery) Discover(ctx context.Context) ([]model.Repository, error) {
	close(p.started)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		return []model.Repository{{ExternalID: "stale-only", Name: "synthetic/stale-only"}}, p.discoveryErr
	}
}

func TestDiscoveryDoesNotOverwriteChangedCredentials(t *testing.T) {
	for _, mutation := range []string{"reconnect", "forget", "unchanged"} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", mutation, failed), func(t *testing.T) {
				e, c := engineFixture(t, context.Background(), &fakeProvider{})
				p := delayedDiscovery{fakeProvider: &fakeProvider{}, started: make(chan struct{}), release: make(chan struct{})}
				if failed {
					p.discoveryErr = errors.New("synthetic discovery failure")
				}
				e.Factory = func(model.Connection, string) (provider.Provider, error) { return p, nil }
				done := make(chan error, 1)
				go func() { done <- e.Discover(t.Context(), c.ID) }()
				<-p.started
				switch mutation {
				case "reconnect":
					c.Name = "Replacement connection"
					if err := e.Connect(t.Context(), c, "synthetic replacement"); err != nil {
						t.Fatal(err)
					}
				case "forget":
					if err := e.ForgetCredential(c.ID); err != nil {
						t.Fatal(err)
					}
				}
				close(p.release)
				discoveryErr := <-done
				stored, err := e.Store.Connection(c.ID)
				if err != nil {
					t.Fatal(err)
				}
				repos, _ := e.Store.Repositories()
				switch mutation {
				case "reconnect":
					if stored.Name != c.Name || stored.Identity != "synthetic-user" || stored.Version != "synthetic" || stored.Status != "connected" || !strings.Contains(stored.Message, "Validated") {
						t.Fatal("discovery overwrote the newly validated connection")
					}
				case "forget":
					if stored.Status != "disconnected" || !strings.Contains(stored.Message, "removed") {
						t.Fatal("discovery overwrote credential removal")
					}
				}
				switch {
				case mutation != "unchanged":
					if discoveryErr == nil || len(repos) != 1 {
						t.Fatal("stale discovery results were not discarded")
					}
				case failed:
					if discoveryErr == nil || stored.Status != "error" || len(repos) != 1 {
						t.Fatal("ordinary discovery failure lost its status")
					}
				default:
					if discoveryErr != nil || stored.Status != "connected" || len(repos) != 2 {
						t.Fatal("unchanged connection did not accept normal discovery")
					}
				}
			})
		}
	}
}

func TestRefreshRecordsCoverageGapsAndRetriesOnlyFailures(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "coverage gaps", true: "actual failure"}[failure], func(t *testing.T) {
			p := &fakeProvider{snapshots: []model.Snapshot{
				{Complete: true, Coverage: model.Coverage{Category: "dependencies", Note: "Empty report imported."}},
				{Coverage: model.Coverage{Category: "code", Collection: "no_analysis", ScannerState: "not_observed", Note: "No analysis for the default branch."}},
				{Coverage: model.Coverage{Category: "secrets", Collection: "disabled", ScannerState: "disabled", Note: "Scanner disabled at the provider."}},
				{Coverage: model.Coverage{Category: "containers", Collection: "unsupported", Note: "No dedicated source."}},
			}}
			if failure {
				p.snapshots = append(p.snapshots, model.Snapshot{Coverage: model.Coverage{Category: "dast", Collection: "denied", Note: "Read access denied."}})
			}
			e, _ := engineFixture(t, context.Background(), p)
			if err := e.Tick(e.Now()); err != nil {
				t.Fatal(err)
			}
			e.Wait()
			runs, err := e.Store.Runs()
			if err != nil || len(runs) != 1 {
				t.Fatalf("run not persisted: %+v %v", runs, err)
			}
			run := runs[0]
			wantStatus, wantFailed := "coverage_gaps", 0
			if failure {
				wantStatus, wantFailed = "partial", 1
			}
			if run.Status != wantStatus || run.Succeeded != 1 || run.Failed != wantFailed || run.Disabled != 1 || run.NoAnalysis != 1 || run.Unsupported != 1 || !run.DetailsRecorded || len(run.Outcomes) != len(p.snapshots) || len(run.Attention()) != 2+wantFailed {
				t.Fatalf("incorrect totals/outcomes: %+v", run)
			}
			repos, _ := e.Store.Repositories()
			for i, outcome := range run.Outcomes {
				if outcome.RepositoryID != repos[0].ID || outcome.RepositoryName != "example/repo" || outcome.Provider != "github" || outcome.Category != p.snapshots[i].Coverage.Category || outcome.Reason != p.snapshots[i].Coverage.Note {
					t.Fatalf("outcome lost its context: %+v", outcome)
				}
			}
			settings, _ := e.Store.Settings()
			if failure {
				if settings.Attempts != 1 || settings.RetryAt.IsZero() {
					t.Fatal("actual failure did not schedule a bounded retry")
				}
			} else {
				if settings.Attempts != 3 || !settings.RetryAt.IsZero() {
					t.Fatal("coverage gaps scheduled unnecessary retries")
				}
				if err = e.Tick(e.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				e.Wait()
				runs, _ = e.Store.Runs()
				if len(runs) != 1 {
					t.Fatal("coverage gaps retried in the same daily slot")
				}
			}
			// Later successful imports and a rename must not rewrite old activity.
			repo := repos[0]
			repo.Name = "example/renamed"
			if err = e.Store.SaveRepository(repo); err != nil {
				t.Fatal(err)
			}
			p.snapshots = []model.Snapshot{{Complete: true, Coverage: model.Coverage{Category: "code", ScannerState: "observed", Note: "Later import succeeded."}}}
			next := e.Now().Add(2 * time.Hour)
			e.Now = func() time.Time { return next }
			if _, _, err = e.Start("manual"); err != nil {
				t.Fatal(err)
			}
			e.Wait()
			runs, _ = e.Store.Runs()
			if len(runs) != 2 || runs[0].Status != "success" || runs[1].Outcomes[1].Reason != "No analysis for the default branch." || runs[1].Outcomes[1].RepositoryName != "example/repo" {
				t.Fatal("later coverage/rename changed historical activity")
			}
		})
	}
}

func TestSuccessfulReportWithDisabledScannerIsStillImported(t *testing.T) {
	p := &fakeProvider{snapshots: []model.Snapshot{{Complete: true, Coverage: model.Coverage{Category: "code", ScannerState: "disabled", Note: "Historical report remains accessible; scanner is disabled."}, Findings: []model.Finding{{ID: "1", Source: "synthetic", Title: "Historical active finding", State: "open", NativeState: "DETECTED"}}}}}
	e, _ := engineFixture(t, context.Background(), p)
	if err := e.Tick(e.Now()); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	runs, _ := e.Store.Runs()
	findings, _ := e.Store.Findings()
	if len(runs) != 1 || runs[0].Succeeded != 1 || runs[0].Failed != 0 || runs[0].Status != "coverage_gaps" || len(runs[0].Attention()) != 1 || len(findings) != 1 || findings[0].State != "open" {
		t.Fatal("disabled scanner metadata prevented a valid historical import or hid its coverage gap")
	}
	settings, _ := e.Store.Settings()
	if settings.Attempts != 3 || !settings.RetryAt.IsZero() {
		t.Fatal("scanner metadata triggered failure retries")
	}
}
func engineFixture(t *testing.T, ctx context.Context, p *fakeProvider) (*Engine, model.Connection) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	secrets := secret.NewMemory()
	e := New(ctx, s, secrets, false)
	e.Now = func() time.Time { return time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC) }
	e.Factory = func(model.Connection, string) (provider.Provider, error) { return p, nil }
	c := model.Connection{ID: "fixture", Provider: "github", Name: "Synthetic", BaseURL: "https://github.com"}
	if err = s.SaveConnection(c); err != nil {
		t.Fatal(err)
	}
	_ = secrets.Set(c.ID, "synthetic-not-real")
	if err = s.SaveDiscovered(c, []model.Repository{{ExternalID: "1", Name: "example/repo"}}); err != nil {
		t.Fatal(err)
	}
	repos, _ := s.Repositories()
	if err = s.SelectRepositories(map[string]bool{repos[0].ID: true}); err != nil {
		t.Fatal(err)
	}
	return e, c
}
func TestDSTGapDoesNotReplayPreviousDailySlot(t *testing.T) {
	e, _ := engineFixture(t, context.Background(), &fakeProvider{})
	clock, _ := time.Parse(time.RFC3339, "2026-03-29T00:15:00Z")
	e.Now = func() time.Time { return clock }
	settings, _ := e.Store.Settings()
	settings.Hour, settings.Minute = 2, 30
	settings.LastSlot, _ = time.Parse(time.RFC3339, "2026-03-28T01:30:00Z")
	settings.Attempts = 3
	if err := e.Store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(clock); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	runs, _ := e.Store.Runs()
	if len(runs) != 0 {
		t.Fatal("DST normalization replayed yesterday's consumed slot")
	}
	clock, _ = time.Parse(time.RFC3339, "2026-03-29T01:30:00Z")
	for range 2 {
		if err := e.Tick(clock); err != nil {
			t.Fatal(err)
		}
		e.Wait()
	}
	runs, _ = e.Store.Runs()
	if len(runs) != 1 || runs[0].Trigger != "scheduled" || runs[0].Status != "success" {
		t.Fatal("shifted DST slot did not run exactly once")
	}
}

func TestRefreshCoalescesAndDoesNotConsumeScheduledSlotWhileBusy(t *testing.T) {
	p := &fakeProvider{block: make(chan struct{}), started: make(chan struct{}, 1)}
	e, _ := engineFixture(t, context.Background(), p)
	first, started, err := e.Start("manual")
	if err != nil || !started {
		t.Fatal(err)
	}
	<-p.started
	id, started, err := e.Start("manual")
	if err != nil || started || id != first {
		t.Fatal("overlapping refresh was started")
	}
	if err = e.Tick(e.Now()); err != nil {
		t.Fatal(err)
	}
	settings, _ := e.Store.Settings()
	if !settings.LastSlot.IsZero() {
		t.Fatal("busy tick consumed schedule")
	}
	close(p.block)
	e.Wait()
	if err = e.Tick(e.Now()); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	runs, _ := e.Store.Runs()
	if len(runs) != 2 {
		t.Fatal("missed slot was not caught up")
	}
	if err = e.Tick(e.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	runs, _ = e.Store.Runs()
	if len(runs) != 2 {
		t.Fatal("successful slot ran twice")
	}
}
func TestPersistedRetriesHonorDeadlineAndHaveBound(t *testing.T) {
	p := &fakeProvider{failure: errors.New("synthetic failure")}
	e, _ := engineFixture(t, context.Background(), p)
	now := e.Now()
	p.retryAt = now.Add(time.Hour)
	if err := e.Tick(now); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	settings, _ := e.Store.Settings()
	if settings.Attempts != 1 || !settings.RetryAt.Equal(p.retryAt) {
		t.Fatalf("retry deadline lost %+v", settings)
	}
	// Recreate the engine, retaining only persisted state, as after sleep/restart.
	resumed := New(context.Background(), e.Store, e.Secrets, false)
	resumed.Factory = e.Factory
	clock := now.Add(10 * time.Minute)
	resumed.Now = func() time.Time { return clock }
	if err := resumed.Tick(clock); err != nil {
		t.Fatal(err)
	}
	resumed.Wait()
	runs, _ := e.Store.Runs()
	if len(runs) != 1 {
		t.Fatal("restart ignored rate limit deadline")
	}
	clock = now.Add(time.Hour)
	if err := resumed.Tick(clock); err != nil {
		t.Fatal(err)
	}
	resumed.Wait()
	clock = clock.Add(11 * time.Minute)
	p.retryAt = clock.Add(26 * time.Hour)
	if err := resumed.Tick(clock); err != nil {
		t.Fatal(err)
	}
	resumed.Wait()
	clock = clock.Add(time.Hour)
	if err := resumed.Tick(clock); err != nil {
		t.Fatal(err)
	}
	resumed.Wait()
	runs, _ = e.Store.Runs()
	if len(runs) != 3 {
		t.Fatalf("expected three bounded attempts, got %d", len(runs))
	}
	settings, _ = e.Store.Settings()
	if settings.Attempts != 3 {
		t.Fatal("retry budget not persisted")
	}
	clock = now.Add(24 * time.Hour)
	if err := resumed.Tick(clock); err != nil {
		t.Fatal(err)
	}
	resumed.Wait()
	runs, _ = e.Store.Runs()
	if len(runs) != 3 {
		t.Fatal("new slot ignored a provider deadline received on the final attempt")
	}
}
func TestRetryDeadlineSurvivesNewDailySlot(t *testing.T) {
	p := &fakeProvider{failure: errors.New("synthetic failure")}
	e, _ := engineFixture(t, context.Background(), p)
	settings, _ := e.Store.Settings()
	location, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 18, settings.Hour, settings.Minute, 0, 0, location).Add(-5 * time.Minute)
	e.Now = func() time.Time { return clock }
	p.retryAt = clock.Add(time.Hour)
	if err = e.Tick(clock); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	clock = clock.Add(5 * time.Minute)
	if err = e.Tick(clock); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	runs, _ := e.Store.Runs()
	if len(runs) != 1 {
		t.Fatal("new daily slot bypassed outstanding provider retry deadline")
	}
	resumed := New(context.Background(), e.Store, e.Secrets, false)
	resumed.Factory, resumed.Now = e.Factory, e.Now
	clock = p.retryAt
	if err = resumed.Tick(clock); err != nil {
		t.Fatal(err)
	}
	resumed.Wait()
	runs, _ = e.Store.Runs()
	if len(runs) != 2 {
		t.Fatal("refresh did not resume when deadline expired")
	}
}

func TestManualRefreshPersistsProviderDeadline(t *testing.T) {
	p := &fakeProvider{failure: errors.New("synthetic rate limit")}
	e, _ := engineFixture(t, context.Background(), p)
	clock := e.Now()
	p.retryAt = clock.Add(26 * time.Hour)
	if _, started, err := e.Start("manual"); err != nil || !started {
		t.Fatalf("manual refresh: %v", err)
	}
	e.Wait()
	settings, err := e.Store.Settings()
	if err != nil || !settings.RetryAt.Equal(p.retryAt) || settings.Attempts != 0 {
		t.Fatalf("manual refresh lost deadline or consumed schedule budget: %+v, %v", settings, err)
	}
	resumed := New(context.Background(), e.Store, e.Secrets, false)
	resumed.Factory = e.Factory
	resumed.Now = func() time.Time { return clock }
	for _, elapsed := range []time.Duration{time.Minute, 24 * time.Hour} {
		clock = e.Now().Add(elapsed)
		if err = resumed.Tick(clock); err != nil {
			t.Fatal(err)
		}
		resumed.Wait()
		runs, _ := e.Store.Runs()
		if len(runs) != 1 {
			t.Fatal("scheduler ignored deadline from manual refresh after restart/new slot")
		}
	}
	clock = p.retryAt
	p.failure = nil
	if err = resumed.Tick(clock); err != nil {
		t.Fatal(err)
	}
	resumed.Wait()
	runs, _ := e.Store.Runs()
	settings, err = e.Store.Settings()
	if err != nil || len(runs) != 2 || settings.Attempts != 3 || !settings.RetryAt.IsZero() {
		t.Fatal("scheduled collection did not resume successfully at the deadline")
	}
}

func TestSchedulePreferencesPreserveOutstandingBackoff(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary change", true: "provider deadline"}[limited], func(t *testing.T) {
			e, _ := engineFixture(t, context.Background(), &fakeProvider{})
			clock := e.Now()
			settings, _ := e.Store.Settings()
			settings.LastSlot, settings.Attempts = clock, 3
			if limited {
				settings.RetryAt = clock.Add(26 * time.Hour)
			}
			deadline := settings.RetryAt
			if err := e.Store.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			if err := e.SavePreferences(8, 30, "UTC", true, 60, 20); err != nil {
				t.Fatal(err)
			}
			settings, err := e.Store.Settings()
			if err != nil || !settings.LastSlot.IsZero() || settings.Attempts != 0 || !settings.RetryAt.Equal(deadline) || settings.Hour != 8 || settings.Minute != 30 || settings.Timezone != "UTC" || settings.ChangesDays != 60 || settings.LogsDays != 20 {
				t.Fatalf("preference change lost backoff or failed to reset schedule: %+v, %v", settings, err)
			}
			resumed := New(context.Background(), e.Store, e.Secrets, false)
			resumed.Factory, resumed.Now = e.Factory, func() time.Time { return clock }
			if err = resumed.Tick(clock); err != nil {
				t.Fatal(err)
			}
			resumed.Wait()
			runs, _ := e.Store.Runs()
			if limited && len(runs) != 0 || !limited && len(runs) != 1 {
				t.Fatal("edited schedule ignored backoff or failed ordinary catch-up")
			}
			if limited {
				clock = deadline
				if err = resumed.Tick(clock); err != nil {
					t.Fatal(err)
				}
				resumed.Wait()
				runs, _ = e.Store.Runs()
				if len(runs) != 1 {
					t.Fatal("edited schedule did not resume at the saved deadline")
				}
			}
		})
	}
}

func TestCompleteReportRetainsOptionalMetadataBackoff(t *testing.T) {
	p := &fakeProvider{}
	e, _ := engineFixture(t, context.Background(), p)
	clock := e.Now()
	deadline := clock.Add(26 * time.Hour)
	p.snapshots = []model.Snapshot{{Complete: true, Coverage: model.Coverage{Category: "code", ScannerState: "unknown", RetryAt: deadline}, Findings: []model.Finding{{ID: "synthetic-1", Title: "Synthetic advisory", State: "open"}}}}
	if err := e.Tick(clock); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	settings, err := e.Store.Settings()
	findings, _ := e.Store.Findings()
	runs, _ := e.Store.Runs()
	if err != nil || !settings.RetryAt.Equal(deadline) || settings.Attempts != 3 || len(findings) != 1 || len(runs) != 1 || runs[0].Status != "success" {
		t.Fatal("complete report did not preserve findings, success and optional metadata deadline")
	}
	resumed := New(context.Background(), e.Store, e.Secrets, false)
	resumed.Factory, resumed.Now = e.Factory, func() time.Time { return clock }
	clock = clock.Add(24 * time.Hour)
	if err = resumed.Tick(clock); err != nil {
		t.Fatal(err)
	}
	resumed.Wait()
	runs, _ = e.Store.Runs()
	if len(runs) != 1 {
		t.Fatal("new daily slot ignored optional metadata backoff after restart")
	}
	clock = deadline
	p.snapshots[0].Coverage.RetryAt = time.Time{}
	if err = resumed.Tick(clock); err != nil {
		t.Fatal(err)
	}
	resumed.Wait()
	runs, _ = e.Store.Runs()
	settings, _ = e.Store.Settings()
	if len(runs) != 2 || !settings.RetryAt.IsZero() {
		t.Fatal("complete report did not resume after optional metadata backoff")
	}
}

func TestRestoredConnectionRequiresExplicitCredentialValidation(t *testing.T) {
	p := &fakeProvider{}
	e, c := engineFixture(t, context.Background(), p)
	c.ReconnectRequired = true
	if err := e.Store.SaveConnection(c); err != nil {
		t.Fatal(err)
	}
	called := false
	e.Factory = func(model.Connection, string) (provider.Provider, error) {
		called = true
		return p, nil
	}
	if err := e.Discover(context.Background(), c.ID); err == nil || called {
		t.Fatal("restored discovery used an existing credential")
	}
	if _, _, err := e.Start("manual"); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	if called {
		t.Fatal("restored refresh used an existing credential")
	}
	if err := e.ForgetCredential(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Secrets.Get(c.ID); err != nil {
		t.Fatal("forgetting a restored connection deleted an unrelated existing credential")
	}
	if err := e.Connect(context.Background(), c, "synthetic replacement"); err != nil {
		t.Fatal(err)
	}
	updated, err := e.Store.Connection(c.ID)
	if err != nil || updated.ReconnectRequired {
		t.Fatalf("explicit credential validation did not reconnect: %v", err)
	}
	called = false
	if err = e.Discover(context.Background(), c.ID); err != nil || !called {
		t.Fatalf("validated reconnection unavailable: %v", err)
	}
}

func TestInterruptedRunAndCredentialRollback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakeProvider{block: make(chan struct{}), started: make(chan struct{}, 1)}
	e, c := engineFixture(t, ctx, p)
	_, _, err := e.Start("manual")
	if err != nil {
		t.Fatal(err)
	}
	<-p.started
	cancel()
	e.Wait()
	runs, _ := e.Store.Runs()
	if runs[0].Status != "interrupted" {
		t.Fatal("interruption not recorded")
	}
	p.block = nil
	p.started = nil
	// A failed metadata write must restore the previous keychain value.
	if err = e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = e.Connect(context.Background(), c, "synthetic-replacement"); err == nil {
		t.Fatal("closed database unexpectedly accepted connection")
	}
	value, _ := e.Secrets.Get(c.ID)
	if value != "synthetic-not-real" {
		t.Fatal("failed connection update destroyed previous credential")
	}
}
func TestDemoIndependentSelectionAndNoCredentialWrites(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := New(context.Background(), s, secret.NewMemory(), true)
	if err = e.SeedDemo(); err != nil {
		t.Fatal(err)
	}
	repos, _ := s.Repositories()
	if len(repos) != 8 {
		t.Fatal("demo discovery incomplete")
	}
	for _, r := range repos {
		if !r.Selected {
			t.Fatal("demo selection not initialized")
		}
	}
	if err = e.Connect(context.Background(), model.Connection{}, "synthetic"); err == nil {
		t.Fatal("demo accepted credentials")
	}
	settings, _ := s.Settings()
	if settings.Enabled {
		t.Fatal("demo enabled daily external work")
	}
	if err = e.SavePreferences(25, 0, "Europe/Zurich", true, 90, 30); err == nil {
		t.Fatal("invalid schedule accepted")
	}
}
