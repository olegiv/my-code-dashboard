// SPDX-License-Identifier: GPL-3.0-only
package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/provider"
)

type demoProvider struct {
	connection model.Connection
	now        func() time.Time
}

func (d demoProvider) Validate(context.Context) (provider.Identity, error) {
	return provider.Identity{Name: "demo", Version: "synthetic"}, nil
}
func (d demoProvider) Discover(context.Context) ([]model.Repository, error) {
	names := []string{"atlas/api", "atlas/web", "atlas/infrastructure", "atlas/billing"}
	if d.connection.Provider == "gitlab" {
		names = []string{"platform/identity", "platform/worker", "platform/gateway", "platform/docs"}
	}
	result := []model.Repository{}
	for i, n := range names {
		result = append(result, model.Repository{ExternalID: fmt.Sprint(i + 1), Name: n, Namespace: "atlas"})
		result[len(result)-1].Namespace = "atlas"
		if d.connection.Provider == "gitlab" {
			result[len(result)-1].Namespace = "platform"
		}
		result[len(result)-1].DefaultBranch = "main"
		result[len(result)-1].URL = "https://example.com/repositories/" + n
	}
	return result, nil
}
func (d demoProvider) RefreshRepository(_ context.Context, r model.Repository) (model.Repository, error) {
	return r, nil
}
func (d demoProvider) Collect(_ context.Context, r model.Repository) []model.Snapshot {
	now := d.now().UTC()
	out := []model.Snapshot{}
	for i, category := range model.Categories {
		c := model.Coverage{Category: category, ScannerState: "observed", Note: "Synthetic demonstration data. No real repositories or provider credentials are used.", AnalysisAt: now.Add(-6 * time.Hour), Ref: "refs/heads/main", Commit: "b71c8a2"}
		s := model.Snapshot{Complete: true, Coverage: c}
		if category == "other" || (r.Provider == "github" && (category == "containers" || category == "iac" || category == "dast")) {
			s.Complete = false
			s.Coverage.Collection = "unsupported"
			s.Coverage.ScannerState = "unknown"
			s.Coverage.AnalysisAt = time.Time{}
			s.Coverage.Note = "This category is not available from the configured provider source."
			out = append(out, s)
			continue
		}
		if r.ExternalID == "3" && category == "code" {
			s.Complete = false
			s.Coverage.Collection = "denied"
			s.Coverage.Note = "Synthetic permission failure. Existing results are retained."
			out = append(out, s)
			continue
		}
		if r.ExternalID == "4" {
			s.Coverage.AnalysisAt = now.Add(-96 * time.Hour)
		}
		if r.ExternalID != "4" && category != "iac" && category != "dast" {
			titles := []string{"HTTP request smuggling in gateway parser", "Untrusted input reaches a database query", "Credential found in repository history", "Outdated base image packages"}
			severity := []string{"critical", "high", "unknown", "medium"}[i%4]
			s.Findings = []model.Finding{{ID: category + "-" + r.ExternalID, Source: "demo", Category: category, Title: titles[i%4], Severity: severity, NativeSeverity: severity, State: "open", NativeState: "open", Scanner: map[string]string{"dependencies": "Dependabot", "code": "CodeQL", "secrets": "Secret scanning", "containers": "Container scanning"}[category], Scope: "refs/heads/main", Package: "example/package", Identifiers: []string{"CVE-2099-0001"}}}
		}
		out = append(out, s)
	}
	return out
}

func (e *Engine) SeedDemo() error {
	connections, err := e.Store.Connections()
	if err != nil {
		return err
	}
	if len(connections) > 0 {
		return nil
	}
	for _, kind := range []string{"github", "gitlab"} {
		base := "https://github.com"
		name := "GitHub · demo"
		if kind == "gitlab" {
			base = "https://gitlab.example.com"
			name = "GitLab · demo"
		}
		c := model.Connection{ID: model.NewID(), Provider: kind, Name: name, BaseURL: base, Identity: "demo", Version: "synthetic", Status: "connected", Message: "Synthetic data only", CreatedAt: e.Now()}
		if err = e.Store.SaveConnection(c); err != nil {
			return err
		}
		p := demoProvider{connection: c, now: e.Now}
		repos, _ := p.Discover(context.Background())
		if err = e.Store.SaveDiscovered(c, repos); err != nil {
			return err
		}
	}
	repos, err := e.Store.Repositories()
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, r := range repos {
		selected[r.ID] = true
	}
	if err = e.Store.SelectRepositories(selected); err != nil {
		return err
	}
	for _, r := range repos {
		r.Selected = true
		p, _ := e.adapter(mustConnection(e, r.ConnectionID))
		for _, s := range p.Collect(context.Background(), r) {
			if err = e.Store.Apply(r, s, e.Now().Add(-2*time.Hour)); err != nil {
				return err
			}
		}
	}
	s, err := e.Store.Settings()
	if err != nil {
		return err
	}
	s.Enabled = false
	return e.Store.SaveSettings(s)
}
func mustConnection(e *Engine, id string) model.Connection { c, _ := e.Store.Connection(id); return c }
