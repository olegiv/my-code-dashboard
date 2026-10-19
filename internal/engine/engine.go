// SPDX-License-Identifier: GPL-3.0-only
package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/provider"
	"github.com/olegiv/my-code-dashboard/internal/secret"
	"github.com/olegiv/my-code-dashboard/internal/store"
)

type Factory func(model.Connection, string) (provider.Provider, error)
type Engine struct {
	Store           *store.Store
	Secrets         secret.Store
	Factory         Factory
	Demo            bool
	ctx             context.Context
	mu              sync.Mutex
	settingsMu      sync.Mutex
	connectionsMu   sync.Mutex
	connectionEpoch map[string]uint64
	active          string
	wg              sync.WaitGroup
	Now             func() time.Time
}

func New(ctx context.Context, s *store.Store, secrets secret.Store, demo bool) *Engine {
	e := &Engine{ctx: ctx, Store: s, Secrets: secrets, Factory: provider.New, Demo: demo, Now: time.Now, connectionEpoch: make(map[string]uint64)}
	if demo {
		e.Factory = func(c model.Connection, _ string) (provider.Provider, error) {
			return demoProvider{connection: c, now: e.Now}, nil
		}
	}
	return e
}
func (e *Engine) Wait()          { e.wg.Wait() }
func (e *Engine) Active() string { e.mu.Lock(); defer e.mu.Unlock(); return e.active }
func (e *Engine) adapter(c model.Connection) (provider.Provider, error) {
	if e.Demo {
		return e.Factory(c, "")
	}
	if c.ReconnectRequired {
		return nil, errors.New("credential reconnection required after restore")
	}
	token, err := e.Secrets.Get(c.ID)
	if err != nil {
		return nil, err
	}
	return e.Factory(c, token)
}

func (e *Engine) Connect(ctx context.Context, c model.Connection, token string) error {
	if e.Demo {
		return errors.New("demo mode does not accept real credentials")
	}
	if c.ID == "" {
		c.ID = model.NewID()
		c.CreatedAt = e.Now()
	}
	p, err := e.Factory(c, token)
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
	c.ReconnectRequired = false
	c.Message = "Validated read-only account access. Individual security permissions are checked on refresh."
	e.connectionsMu.Lock()
	defer e.connectionsMu.Unlock()
	oldToken, oldErr := e.Secrets.Get(c.ID)
	if err = e.Secrets.Set(c.ID, token); err != nil {
		return err
	}
	e.connectionEpoch[c.ID]++
	if err = e.Store.SaveConnection(c); err != nil {
		if oldErr == nil {
			_ = e.Secrets.Set(c.ID, oldToken)
		} else {
			_ = e.Secrets.Delete(c.ID)
		}
		return err
	}
	return nil
}

func (e *Engine) Discover(ctx context.Context, id string) error {
	e.connectionsMu.Lock()
	c, err := e.Store.Connection(id)
	if err != nil {
		e.connectionsMu.Unlock()
		return errors.New("connection not found")
	}
	p, err := e.adapter(c)
	epoch := e.connectionEpoch[id]
	e.connectionsMu.Unlock()
	var repos []model.Repository
	if err == nil {
		repos, err = p.Discover(ctx)
	}
	e.connectionsMu.Lock()
	defer e.connectionsMu.Unlock()
	if e.connectionEpoch[id] != epoch {
		return errors.New("connection changed during discovery; try discovery again")
	}
	// Preserve current metadata, and serialize the final write against credential
	// replacement/removal. Provider requests above do not hold this mutex.
	current, readErr := e.Store.Connection(id)
	if readErr != nil {
		return errors.New("connection not found")
	}
	c = current
	if err == nil {
		err = e.Store.SaveDiscovered(c, repos)
	}
	if err != nil {
		kind, message := provider.Failure(err)
		if kind == "error" {
			message = "Discovery failed. Check the credential, network, and provider permissions."
		}
		c.Status = "error"
		c.Message = message
		_ = e.Store.SaveConnection(c)
		return errors.New(message)
	}
	c.Status = "connected"
	c.Message = "Repository discovery completed. Only selected repositories have their findings imported."
	return e.Store.SaveConnection(c)
}

func (e *Engine) ForgetCredential(id string) error {
	if e.Demo {
		return errors.New("demo credentials cannot be changed")
	}
	e.connectionsMu.Lock()
	defer e.connectionsMu.Unlock()
	c, err := e.Store.Connection(id)
	if err != nil {
		return errors.New("connection not found")
	}
	if !c.ReconnectRequired {
		if err = e.Secrets.Delete(id); err != nil {
			return errors.New("could not remove credential from the credential backend")
		}
	}
	e.connectionEpoch[id]++
	c.Status = "disconnected"
	c.Message = "Local credential removed. Revoke the token at the provider if required. Existing findings are retained."
	return e.Store.SaveConnection(c)
}

func (e *Engine) SavePreferences(hour, minute int, zone string, enabled bool, changes, logs int) error {
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 || changes < 1 || changes > 3650 || logs < 1 || logs > 365 {
		return errors.New("invalid schedule or retention range")
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return errors.New("unknown IANA timezone")
	}
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()
	s, err := e.Store.Settings()
	if err != nil {
		return err
	}
	if s.Hour != hour || s.Minute != minute || s.Timezone != zone {
		s.LastSlot = time.Time{}
		s.Attempts = 0
	}
	s.Hour = hour
	s.Minute = minute
	s.Timezone = zone
	s.Enabled = enabled
	s.ChangesDays = changes
	s.LogsDays = logs
	if err = e.Store.SaveSettings(s); err != nil {
		return err
	}
	return e.Store.Prune(e.Now(), s)
}

func (e *Engine) Start(trigger string) (string, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.startLocked(trigger)
}
func (e *Engine) startLocked(trigger string) (string, bool, error) {
	if e.active != "" {
		return e.active, false, nil
	}
	if e.ctx.Err() != nil {
		return "", false, errors.New("application is stopping")
	}
	run := model.Run{ID: model.NewID(), Trigger: trigger, StartedAt: e.Now().UTC(), Status: "running", Message: "Collecting selected repositories…", DetailsRecorded: true}
	if err := e.Store.SaveRun(run); err != nil {
		return "", false, err
	}
	e.active = run.ID
	e.wg.Add(1)
	go func() { defer e.wg.Done(); defer func() { e.mu.Lock(); e.active = ""; e.mu.Unlock() }(); e.run(run) }()
	return run.ID, true, nil
}

func (e *Engine) refreshRepositories() ([]model.Repository, error) {
	repos, err := e.Store.Repositories()
	if err != nil {
		return nil, err
	}
	e.settingsMu.Lock()
	settings, err := e.Store.Settings()
	e.settingsMu.Unlock()
	if err != nil {
		return nil, err
	}
	var selected []model.Repository
	for _, repo := range repos {
		if repo.Selected {
			selected = append(selected, repo)
		}
	}
	for i, repo := range selected {
		if repo.ID == settings.ResumeAfterRepositoryID {
			selected = append(selected[i+1:], selected[:i+1]...)
			break
		}
	}
	return selected, nil
}

func (e *Engine) saveRefreshResume(id string) error {
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()
	settings, err := e.Store.Settings()
	if err != nil {
		return err
	}
	settings.ResumeAfterRepositoryID = id
	return e.Store.SaveSettings(settings)
}

func (e *Engine) run(run model.Run) {
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Minute)
	defer cancel()
	run.Message = ""
	repos, err := e.refreshRepositories()
	if err != nil {
		run.Failed++
		run.Message = "Could not read repository selection or refresh progress."
	}
	adapters := map[string]provider.Provider{}
	adapterErrors := map[string]error{}
	epochs := map[string]uint64{}
	connectionChanged := &provider.Error{Kind: "error", Message: "Connection changed during refresh; results were discarded and previous complete data was retained. Refresh again to use the current connection."}
	var retryAt time.Time
	for i, repo := range repos {
		if ctx.Err() != nil {
			break
		}
		p := adapters[repo.ConnectionID]
		pe := adapterErrors[repo.ConnectionID]
		e.connectionsMu.Lock()
		if _, captured := epochs[repo.ConnectionID]; !captured {
			epochs[repo.ConnectionID] = e.connectionEpoch[repo.ConnectionID]
			var c model.Connection
			c, pe = e.Store.Connection(repo.ConnectionID)
			if pe == nil {
				p, pe = e.adapter(c)
			}
			if pe == nil {
				adapters[repo.ConnectionID] = p
			} else {
				adapterErrors[repo.ConnectionID] = pe
			}
		}
		if e.connectionEpoch[repo.ConnectionID] != epochs[repo.ConnectionID] {
			pe = connectionChanged
		}
		e.connectionsMu.Unlock()
		var snapshots []model.Snapshot
		if pe == nil {
			var fresh model.Repository
			fresh, pe = p.RefreshRepository(ctx, repo)
			if pe == nil {
				e.connectionsMu.Lock()
				if e.connectionEpoch[repo.ConnectionID] != epochs[repo.ConnectionID] {
					pe = connectionChanged
				} else if err = e.Store.SaveRepository(fresh); err != nil {
					pe = err
				} else {
					repo = fresh
				}
				e.connectionsMu.Unlock()
			}
		}
		if pe == nil {
			snapshots = p.Collect(ctx, repo)
		}
		// Serialize final writes with credential replacement/removal, never HTTP.
		e.connectionsMu.Lock()
		changed := e.connectionEpoch[repo.ConnectionID] != epochs[repo.ConnectionID]
		if changed {
			pe = connectionChanged
		}
		if pe != nil {
			snapshots = nil
			kind, message := provider.Failure(pe)
			if strings.Contains(pe.Error(), "credential") {
				kind = "denied"
				message = "Credential unavailable. Unlock the keychain or replace the token in Connections."
			}
			for _, c := range model.Categories {
				coverage := model.Coverage{Category: c, Collection: kind, ScannerState: "unknown", Note: message}
				var providerError *provider.Error
				if errors.As(pe, &providerError) {
					coverage.RetryAt = providerError.RetryAt
				}
				snapshots = append(snapshots, model.Snapshot{Coverage: coverage})
			}
		}
		for _, snap := range snapshots {
			outcome := model.CollectionOutcome{RepositoryID: repo.ID, RepositoryName: repo.Name, Provider: repo.Provider, Category: snap.Coverage.Category, Collection: snap.Coverage.Collection, ScannerState: snap.Coverage.ScannerState, Reason: snap.Coverage.Note}
			if changed {
				run.Failed++
				run.Outcomes = append(run.Outcomes, outcome)
				continue
			}
			if snap.Coverage.RetryAt.After(retryAt) {
				retryAt = snap.Coverage.RetryAt
			}
			if err = e.Store.Apply(repo, snap, e.Now().UTC()); err != nil {
				run.Failed++
				outcome.Collection = "error"
				outcome.Reason = "Could not save this category's imported data. Previous complete data was retained."
				run.Outcomes = append(run.Outcomes, outcome)
				continue
			}
			if snap.Complete {
				run.Succeeded++
				outcome.Collection = "success"
			} else {
				switch snap.Coverage.Collection {
				case "disabled":
					run.Disabled++
				case "no_analysis":
					run.NoAnalysis++
				case "unsupported":
					run.Unsupported++
				default:
					run.Failed++
				}
			}
			run.Outcomes = append(run.Outcomes, outcome)
		}
		e.connectionsMu.Unlock()
		// Preserve completed repository outcomes if the application later stops.
		_ = e.Store.SaveRun(run)
		resumeAfter := repo.ID
		if i == len(repos)-1 {
			resumeAfter = ""
		}
		if err = e.saveRefreshResume(resumeAfter); err != nil {
			run.Failed++
			run.Message = "Could not save refresh progress. Completed imports were retained."
			break
		}
	}
	run.FinishedAt = e.Now().UTC()
	run.Status = "success"
	if len(run.Attention()) > 0 {
		run.Status = "coverage_gaps"
	}
	if run.Failed > 0 {
		run.Status = "partial"
	}
	if ctx.Err() != nil {
		run.Status = "interrupted"
		run.Failed++
		run.Message = "Refresh interrupted. Completed imports and previous findings for uncollected categories have been retained."
	}
	if run.Message == "" {
		run.Message = fmt.Sprintf("%d category collections completed; %d disabled; %d missing analysis; %d failed.", run.Succeeded, run.Disabled, run.NoAnalysis, run.Failed)
	}
	_ = e.Store.SaveRun(run)
	e.settingsMu.Lock()
	s, settingsErr := e.Store.Settings()
	if settingsErr == nil {
		if run.Trigger == "scheduled" {
			if run.Failed == 0 {
				s.Attempts = 3
				s.RetryAt = time.Time{}
			} else if s.Attempts < 3 {
				s.RetryAt = e.Now().Add(time.Duration(s.Attempts) * 5 * time.Minute)
			}
		}
		if retryAt.After(s.RetryAt) {
			s.RetryAt = retryAt
		}
		_ = e.Store.SaveSettings(s)
	}
	e.settingsMu.Unlock()
	if settingsErr == nil {
		_ = e.Store.Prune(e.Now(), s)
	}
}

func (e *Engine) Tick(now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()
	s, err := e.Store.Settings()
	if err != nil {
		return err
	}
	// Retention maintenance is independent of collection and its enabled state.
	if err = e.Store.Prune(now, s); err != nil {
		return err
	}
	if !s.Enabled || e.active != "" {
		return nil
	}
	slot, err := model.LatestSlot(now, s)
	if err != nil {
		return err
	}
	if !slot.Equal(s.LastSlot) {
		s.LastSlot = slot
		s.Attempts = 0
	}
	if s.Attempts >= 3 || s.RetryAt.After(now) {
		return nil
	}
	s.Attempts++
	s.RetryAt = now.Add(5 * time.Minute)
	if err = e.Store.SaveSettings(s); err != nil {
		return err
	}
	_, _, err = e.startLocked("scheduled")
	return err
}
func (e *Engine) Schedule(ctx context.Context) {
	_ = e.Tick(e.Now())
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			_ = e.Tick(now)
		}
	}
}
