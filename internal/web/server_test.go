// SPDX-License-Identifier: GPL-3.0-only
package web

import (
	"context"
	"fmt"
	"html"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/engine"
	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/provider"
	"github.com/olegiv/my-code-dashboard/internal/secret"
	"github.com/olegiv/my-code-dashboard/internal/store"
)

func appFixture(t *testing.T, demo bool) *Server {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(context.Background(), s, secret.NewMemory(), demo)
	t.Cleanup(func() { e.Wait(); s.Close() })
	if demo {
		if err = e.SeedDemo(); err != nil {
			t.Fatal(err)
		}
	}
	app, err := New(e, dir, "127.0.0.1:7364", "test")
	if err != nil {
		t.Fatal(err)
	}
	return app
}
func request(app *Server, method, path string, values url.Values, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:7364"+path, strings.NewReader(values.Encode()))
	if method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}
func TestPagesFiltersAndHTMXResponses(t *testing.T) {
	app := appFixture(t, true)
	for _, path := range []string{"/", "/repositories", "/findings", "/connections", "/activity", "/settings", "/assets/app.js", "/assets/vendor/htmx-4.0.0.min.js", "/healthz"} {
		r := request(app, "GET", path, nil, nil)
		if r.Code != 200 {
			t.Errorf("%s => %d %s", path, r.Code, r.Body.String())
		}
		if !strings.Contains(r.Header().Get("Content-Security-Policy"), "script-src 'self'") || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("security headers missing")
		}
	}
	r := request(app, "GET", "/findings?severity=critical&provider=github", nil, map[string]string{"HX-Request": "true"})
	if strings.Contains(r.Body.String(), "<!doctype") || !strings.Contains(r.Body.String(), "3</strong> matching findings") || strings.Contains(r.Body.String(), "platform/") {
		t.Fatalf("partial filter response: %s", r.Body.String())
	}
	r = request(app, "GET", "/findings?native_state=not-a-status", nil, nil)
	if !strings.Contains(r.Body.String(), "No active findings match this view") || !strings.Contains(r.Body.String(), "not proof") {
		t.Fatal("empty filter falsely claims security")
	}
	r = request(app, "GET", "/repositories/view?id=missing", nil, nil)
	if r.Code != 404 {
		t.Fatal("unknown repository accepted")
	}
	repos, _ := app.Engine.Store.Repositories()
	r = request(app, "GET", "/repositories/view?id="+url.QueryEscape(repos[0].ID), nil, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Coverage &amp; freshness") {
		t.Fatal("repository coverage missing")
	}
}

func TestEnabledScannerWithoutPipelineEvidenceNeedsAttention(t *testing.T) {
	for _, state := range []string{"enabled", "observed"} {
		t.Run(state, func(t *testing.T) {
			app := appFixture(t, false)
			c := model.Connection{ID: "fixture", Provider: "gitlab", BaseURL: "https://gitlab.example"}
			if err := app.Engine.Store.SaveConnection(c); err != nil {
				t.Fatal(err)
			}
			if err := app.Engine.Store.SaveDiscovered(c, []model.Repository{{ExternalID: "42", Name: "example/project"}}); err != nil {
				t.Fatal(err)
			}
			repos, _ := app.Engine.Store.Repositories()
			repo := repos[0]
			if err := app.Engine.Store.SelectRepositories(map[string]bool{repo.ID: true}); err != nil {
				t.Fatal(err)
			}
			snapshot := model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code", ScannerState: state, Note: "Synthetic scanner evidence"}, Findings: []model.Finding{{ID: "1", Source: "fixture", Title: "Active synthetic finding", State: "open", Severity: "high"}}}
			if err := app.Engine.Store.Apply(repo, snapshot, app.Engine.Now()); err != nil {
				t.Fatal(err)
			}
			run := model.Run{ID: "fixture", StartedAt: app.Engine.Now(), FinishedAt: app.Engine.Now(), DetailsRecorded: true, Outcomes: []model.CollectionOutcome{{RepositoryID: repo.ID, RepositoryName: repo.Name, Provider: "gitlab", Category: "code", Collection: "success", ScannerState: state, Reason: snapshot.Coverage.Note}}}
			want := 0
			if state == "enabled" {
				want = 1
			}
			if got := len(run.Attention()); got != want {
				t.Errorf("activity attention = %d, want %d", got, want)
			}
			for _, page := range []string{"overview", "repository"} {
				req := httptest.NewRequest("GET", "http://127.0.0.1:7364/?id="+url.QueryEscape(repo.ID), nil)
				v, err := app.view(req, page, "Synthetic")
				if err != nil || v.Issues != want || len(v.Repositories) != 1 || v.Repositories[0].Issues != want {
					t.Fatalf("%s coverage issues: %+v, %v", page, v, err)
				}
				if page == "repository" && (v.SelectedRepo == nil || v.SelectedRepo.Issues != want) {
					t.Fatal("repository detail lost the coverage gap")
				}
			}
			if err := app.Engine.Store.SaveRun(run); err != nil {
				t.Fatal(err)
			}
			activity := request(app, "GET", "/activity", nil, nil)
			if activity.Code != 200 || (want == 1 && !strings.Contains(activity.Body.String(), snapshot.Coverage.Note)) {
				t.Fatal("activity did not display the saved reason")
			}
			response := request(app, "GET", "/repositories/view?id="+url.QueryEscape(repo.ID), nil, nil)
			if response.Code != 200 || !strings.Contains(response.Body.String(), "Active synthetic finding") {
				t.Fatal("coverage gap hid the imported active finding")
			}
		})
	}
}
func TestFindingsHistoryRestorationReturnsShell(t *testing.T) {
	app := appFixture(t, true)
	path := "/findings?severity=critical&provider=github"
	response := request(app, "GET", path, nil, map[string]string{"HX-Request": "true", "HX-History-Restore-Request": "true"})
	for _, expected := range []string{"<!doctype html>", "Main navigation", "Search findings", "3</strong> matching findings"} {
		if response.Code != 200 || !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("history restore lost shell/filter/results: %s", expected)
		}
	}
	response = request(app, "GET", path, nil, map[string]string{"HX-Request": "true"})
	if response.Code != 200 || strings.Contains(response.Body.String(), "<!doctype") || !strings.Contains(response.Body.String(), "3</strong> matching findings") {
		t.Fatal("ordinary HTMX filters no longer return a results fragment")
	}
}

func TestUnmonitoredRepositoryRetainsExplicitFindingViews(t *testing.T) {
	app := appFixture(t, true)
	repos, _ := app.Engine.Store.Repositories()
	repo := repos[0]
	if err := app.Engine.Store.Apply(repo, model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code"}, Findings: []model.Finding{
		{ID: "retained-open", Source: "fixture", Title: "Retained active synthetic finding", State: "open", Severity: "high"},
		{ID: "retained-resolved", Source: "fixture", Title: "Retained resolved synthetic finding", State: "resolved", Severity: "high"},
	}}, app.Engine.Now()); err != nil {
		t.Fatal(err)
	}
	if err := app.Engine.Store.SelectRepositories(map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/repositories/view?id=" + url.QueryEscape(repo.ID), "/findings?repo=" + url.QueryEscape(repo.ID)} {
		r := request(app, "GET", path, nil, nil)
		if r.Code != 200 || !strings.Contains(r.Body.String(), "Retained active synthetic finding") || strings.Contains(r.Body.String(), "Retained resolved synthetic finding") {
			t.Fatalf("retained active findings missing from %s", path)
		}
	}
	for _, path := range []string{"/", "/findings"} {
		r := request(app, "GET", path, nil, nil)
		if strings.Contains(r.Body.String(), "Retained active synthetic finding") {
			t.Fatalf("unmonitored repository leaked into portfolio %s", path)
		}
		v, err := app.view(httptest.NewRequest("GET", "http://127.0.0.1:7364"+path, nil), "findings", "Findings")
		if err != nil || v.Open != 0 || v.Monitored != 0 || v.FindingCount != 0 {
			t.Fatalf("unmonitored findings affected portfolio totals: %v", err)
		}
	}
}

func TestHostCrossOriginAndCSRFProtection(t *testing.T) {
	app := appFixture(t, true)
	for _, host := range []string{"attacker.example:7364", "127.0.0.1:9999", "localhost.attacker.example:7364"} {
		if r := request(app, "GET", "/", nil, map[string]string{"Host": host}); r.Code != 403 {
			t.Fatal("DNS rebinding host accepted")
		}
	}
	if r := request(app, "POST", "/refresh", nil, nil); r.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	values := url.Values{"csrf": {app.csrf}}
	if r := request(app, "POST", "/refresh", values, map[string]string{"Origin": "https://attacker.example", "Sec-Fetch-Site": "cross-site"}); r.Code != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
	r := request(app, "POST", "/refresh", values, map[string]string{"HX-Request": "true", "Origin": "http://127.0.0.1:7364", "Sec-Fetch-Site": "same-origin"})
	if r.Code != 200 {
		t.Fatal("valid refresh denied")
	}
	app.Engine.Wait()
	runs, _ := app.Engine.Store.Runs()
	if len(runs) != 1 {
		t.Fatal("refresh did not run")
	}
	r = request(app, "GET", "/status", nil, map[string]string{"HX-Request": "true"})
	if r.Header().Get("HX-Refresh") != "true" {
		t.Fatal("completed refresh did not update dashboard")
	}
}
func TestProviderContentIsEscapedAndDemoRejectsTokens(t *testing.T) {
	app := appFixture(t, true)
	repos, _ := app.Engine.Store.Repositories()
	repo := repos[0]
	malicious := `<script>alert('synthetic')</script>`
	if err := app.Engine.Store.Apply(repo, model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code"}, Findings: []model.Finding{{ID: "synthetic", Source: "test", Title: malicious, URL: "javascript:alert(1)", Severity: "unknown", State: "open", NativeState: "DETECTED"}}}, app.Engine.Now()); err != nil {
		t.Fatal(err)
	}
	r := request(app, "GET", "/findings?q="+url.QueryEscape("synthetic"), nil, nil)
	if strings.Contains(r.Body.String(), malicious) || strings.Contains(r.Body.String(), `href="javascript:`) || !strings.Contains(r.Body.String(), "&lt;script&gt;") {
		t.Fatal("provider content was not safely escaped")
	}
	values := url.Values{"csrf": {app.csrf}, "token": {"SYNTHETIC_TOKEN_NEVER_ECHO"}}
	r = request(app, "POST", "/connections", values, nil)
	if r.Code != 303 || strings.Contains(r.Header().Get("Location"), "SYNTHETIC_TOKEN") {
		t.Fatal("demo credential isolation failed")
	}
	c, _ := app.Engine.Store.Connections()
	if len(c) != 2 {
		t.Fatal("demo wrote real connection")
	}
}
func TestSettingsSelectionAndBackupWorkflow(t *testing.T) {
	app := appFixture(t, true)
	repos, _ := app.Engine.Store.Repositories()
	values := url.Values{"csrf": {app.csrf}, "repository": {repos[0].ID}}
	if r := request(app, "POST", "/repositories", values, nil); r.Code != 303 {
		t.Fatal("selection failed")
	}
	repos, _ = app.Engine.Store.Repositories()
	selected := 0
	for _, r := range repos {
		if r.Selected {
			selected++
		}
	}
	if selected != 1 {
		t.Fatal("selection did not persist")
	}
	values = url.Values{"csrf": {app.csrf}, "time": {"09:00"}, "timezone": {"Europe/Zurich"}, "changes_days": {"60"}, "logs_days": {"20"}, "enabled": {"on"}}
	if r := request(app, "POST", "/settings", values, nil); r.Code != 303 {
		t.Fatal("settings failed")
	}
	s, _ := app.Engine.Store.Settings()
	if s.ChangesDays != 60 || !s.Enabled {
		t.Fatal("settings did not persist")
	}
	values.Set("time", "26:00")
	r := request(app, "POST", "/settings", values, nil)
	if !strings.Contains(r.Header().Get("Location"), "error=") {
		t.Fatal("invalid settings accepted")
	}
	r = request(app, "POST", "/backup", url.Values{"csrf": {app.csrf}}, nil)
	if r.Code != 200 || r.Header().Get("Content-Type") != "application/gzip" || r.Body.Len() < 200 {
		t.Fatal("browser backup failed")
	}
}
func TestEmptyInstallationGuidesConnectionAndNoDataIsUnknown(t *testing.T) {
	app := appFixture(t, false)
	r := request(app, "GET", "/", nil, nil)
	if !strings.Contains(r.Body.String(), "Connect") || strings.Contains(strings.ToLower(r.Body.String()), "you are secure") {
		t.Fatal("empty installation lacks honest onboarding")
	}
	app.SecretFiles = true
	r = request(app, "POST", "/connections", url.Values{"csrf": {app.csrf}, "token": {"synthetic"}}, nil)
	if r.Code != 303 || !strings.Contains(r.Header().Get("Location"), "Provision") {
		t.Fatal("headless backend accepted browser token")
	}
}

func TestActivityShowsHistoricalRepositoryCategoryAndSafeReasons(t *testing.T) {
	app := appFixture(t, true)
	repos, _ := app.Engine.Store.Repositories()
	repo := repos[0]
	now := app.Engine.Now().UTC()
	malicious := `<script>alert('synthetic')</script>`
	run := model.Run{ID: "detailed", Trigger: "manual", Status: "partial", StartedAt: now, FinishedAt: now, Succeeded: 2, Disabled: 1, NoAnalysis: 1, Failed: 1, Unsupported: 4, DetailsRecorded: true, Outcomes: []model.CollectionOutcome{
		{RepositoryID: repo.ID, RepositoryName: "example/original-name", Provider: "github", Category: "secrets", Collection: "disabled", ScannerState: "disabled", Reason: "GitHub reports that secret scanning is disabled."},
		{RepositoryID: repo.ID, RepositoryName: "example/original-name", Provider: "github", Category: "code", Collection: "no_analysis", Reason: "No code analysis for the default branch."},
		{RepositoryID: repo.ID, RepositoryName: malicious, Provider: "gitlab", Category: "dependencies", Collection: "denied", Reason: "Read access denied. " + malicious},
		{RepositoryID: repo.ID, RepositoryName: "example/original-name", Provider: "gitlab", Category: "dast", Collection: "success", ScannerState: "disabled", Reason: "Historical findings imported; scanner is currently disabled."},
		{RepositoryID: repo.ID, RepositoryName: "example/original-name", Provider: "github", Category: "containers", Collection: "unsupported", Reason: "No dedicated source."},
	}}
	if err := app.Engine.Store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	if err := app.Engine.Store.SaveRun(model.Run{ID: "legacy", StartedAt: now.Add(-time.Minute), Status: "partial", Failed: 5, Message: "Legacy summary."}); err != nil {
		t.Fatal(err)
	}
	// Current coverage differs: activity must use the outcome saved with the run.
	if err := app.Engine.Store.Apply(repo, model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code", Note: "Current coverage is different."}}, now); err != nil {
		t.Fatal(err)
	}
	response := request(app, "GET", "/activity", nil, nil)
	if response.Code != 200 {
		t.Fatalf("activity failed: %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{"2 completed", "1 failed", "1 disabled · 1 missing analysis", "4 unsupported categories skipped", "Repository and category details", "example/original-name", "Code / SAST", "Secrets", "Missing analysis", "Access denied", "No code analysis for the default branch.", "Historical findings imported; scanner is currently disabled.", "Scanner: Disabled", "Repository/category reasons were not recorded", "&lt;script&gt;", "/repositories/view?id=" + url.QueryEscape(repo.ID)} {
		if !strings.Contains(body, want) {
			t.Errorf("activity missing %q", want)
		}
	}
	if strings.Contains(body, malicious) || strings.Contains(body, "Current coverage is different.") || strings.Contains(body, "No_analysis") {
		t.Fatal("activity used unsafe content, current coverage, or an unreadable state label")
	}
}

func TestFindingPaginationAndStaleEvidence(t *testing.T) {
	app := appFixture(t, true)
	repos, _ := app.Engine.Store.Repositories()
	repo := repos[0]
	findings := []model.Finding{}
	for i := range 101 {
		findings = append(findings, model.Finding{ID: fmt.Sprint(i), Source: "synthetic", Title: fmt.Sprintf("Synthetic issue %03d", i), Severity: "high", NativeSeverity: "HIGH", State: "open", NativeState: "DETECTED"})
	}
	for _, state := range []string{"resolved", "dismissed", "unknown"} {
		findings = append(findings, model.Finding{ID: state, Source: "synthetic", Title: "Historical synthetic finding " + state, Severity: "high", State: state, NativeState: strings.ToUpper(state)})
	}
	ago := app.Engine.Now().Add(-72 * time.Hour)
	if err := app.Engine.Store.Apply(repo, model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code", AnalysisAt: ago, ScannerState: "observed"}, Findings: findings}, ago); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://127.0.0.1:7364/findings?repo="+url.QueryEscape(repo.ID)+"&category=code&native_state=DETECTED&page=3", nil)
	v, err := app.view(req, "findings", "Findings")
	if err != nil || v.FindingCount != 101 || v.PageCount != 3 || len(v.Findings) != 1 || v.Stale == 0 {
		t.Fatalf("pagination/staleness: count=%d pages=%d rows=%d stale=%d err=%v", v.FindingCount, v.PageCount, len(v.Findings), v.Stale, err)
	}
	if !strings.Contains(v.PreviousURL, "native_state=DETECTED") || !strings.Contains(v.PreviousURL, "repo=") {
		t.Fatal("pagination dropped filters")
	}
	response := request(app, "GET", "/repositories/view?id="+url.QueryEscape(repo.ID), nil, nil)
	if !strings.Contains(response.Body.String(), "text-warning") {
		t.Fatal("stale analysis was not highlighted")
	}
	if strings.Contains(response.Body.String(), "Historical synthetic finding") || !strings.Contains(response.Body.String(), "Active findings") {
		t.Fatal("repository details displayed inactive findings")
	}
	for _, page := range []string{"overview", "findings"} {
		req = httptest.NewRequest("GET", "http://127.0.0.1:7364/findings?repo="+url.QueryEscape(repo.ID)+"&category=code", nil)
		active, err := app.view(req, page, "Active findings")
		if err != nil || active.State != "open" || active.FindingCount != 102 {
			t.Fatalf("%s default view includes inactive findings: state=%s count=%d err=%v", page, active.State, active.FindingCount, err)
		}
	}
	req = httptest.NewRequest("GET", "http://127.0.0.1:7364/findings?repo="+url.QueryEscape(repo.ID)+"&category=code&state=all", nil)
	all, err := app.view(req, "findings", "Findings")
	if err != nil || all.State != "all" || all.FindingCount != 105 {
		t.Fatalf("explicit all-status view lost history: state=%s count=%d err=%v", all.State, all.FindingCount, err)
	}
	req = httptest.NewRequest("GET", "http://127.0.0.1:7364/repositories/view?id="+url.QueryEscape(repo.ID)+"&category=code", nil)
	v, err = app.view(req, "repository", "Repository details")
	if err != nil || v.State != "open" || v.FindingCount != 102 || v.PageCount != 3 {
		t.Fatalf("repository active findings: state=%s count=%d pages=%d err=%v", v.State, v.FindingCount, v.PageCount, err)
	}
	next, err := url.Parse(v.NextURL)
	if err != nil || next.Query().Get("repo") != repo.ID || next.Query().Get("state") != "open" {
		t.Fatalf("repository pagination lost active status or repository: %q", v.NextURL)
	}
	response = request(app, "GET", v.NextURL, nil, map[string]string{"HX-Request": "true"})
	if response.Code != 200 || !strings.Contains(response.Body.String(), "102</strong> matching findings") || strings.Contains(response.Body.String(), "Historical synthetic finding") {
		t.Fatal("active findings pagination included unrelated or inactive findings")
	}
	response = request(app, "GET", "/findings?repo="+url.QueryEscape(repo.ID)+"&state=resolved", nil, nil)
	if !strings.Contains(response.Body.String(), "Historical synthetic finding resolved") {
		t.Fatal("historical findings are no longer accessible through the status filter")
	}
	stored, err := app.Engine.Store.Findings()
	if err != nil {
		t.Fatal(err)
	}
	resolved := []model.Finding{}
	for _, f := range stored {
		if f.RepositoryID == repo.ID && f.Category == "code" {
			f.State, f.NativeState = "resolved", "fixed"
			resolved = append(resolved, f)
		}
	}
	if err := app.Engine.Store.Apply(repo, model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code"}, Findings: resolved}, app.Engine.Now()); err != nil {
		t.Fatal(err)
	}
	response = request(app, "GET", "/repositories/view?id="+url.QueryEscape(repo.ID)+"&category=code", nil, nil)
	if !strings.Contains(response.Body.String(), "No active findings match this view") || !strings.Contains(response.Body.String(), "not proof") {
		t.Fatal("inactive-only collection did not show an honest empty active view")
	}
}

type connectionProvider struct{ failure error }

func (p connectionProvider) Validate(context.Context) (provider.Identity, error) {
	return provider.Identity{Name: "synthetic-account", Version: "synthetic"}, p.failure
}
func (p connectionProvider) Discover(context.Context) ([]model.Repository, error) {
	return []model.Repository{{ExternalID: "101", Name: "example/synthetic", DefaultBranch: "main"}}, p.failure
}
func (p connectionProvider) RefreshRepository(_ context.Context, r model.Repository) (model.Repository, error) {
	return r, p.failure
}
func (p connectionProvider) Collect(context.Context, model.Repository) []model.Snapshot { return nil }

func TestConnectionValidationDiscoveryAndTokenNeverInDatabase(t *testing.T) {
	app := appFixture(t, false)
	app.Engine.Factory = func(model.Connection, string) (provider.Provider, error) { return connectionProvider{}, nil }
	token := "SYNTHETIC_TOKEN_NOT_A_REAL_CREDENTIAL"
	values := url.Values{"csrf": {app.csrf}, "name": {"Synthetic account"}, "provider": {"github"}, "base_url": {"https://github.com"}, "token": {token}}
	r := request(app, "POST", "/connections", values, nil)
	if r.Code != 303 || strings.Contains(r.Header().Get("Location"), token) {
		t.Fatal("connection workflow failed or echoed token")
	}
	connections, _ := app.Engine.Store.Connections()
	if len(connections) != 1 || connections[0].Identity != "synthetic-account" {
		t.Fatal("validated connection not saved")
	}
	c := connections[0]
	stored, err := app.Engine.Secrets.Get(c.ID)
	if err != nil || stored != token {
		t.Fatal("credential backend not used")
	}
	backup := filepath.Join(t.TempDir(), "database.sqlite")
	if err = app.Engine.Store.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(backup)
	if strings.Contains(string(raw), token) {
		t.Fatal("token persisted in database")
	}
	r = request(app, "POST", "/connections/discover", url.Values{"csrf": {app.csrf}, "connection_id": {c.ID}}, nil)
	if r.Code != 303 {
		t.Fatal("discovery failed")
	}
	target, err := url.Parse(r.Header().Get("Location"))
	if err != nil || target.Path != "/repositories" || target.Query().Get("connection") != c.ID {
		t.Fatalf("discovery did not open the clicked connection: %q", r.Header().Get("Location"))
	}
	filtered := request(app, "GET", target.RequestURI(), nil, nil)
	if !strings.Contains(filtered.Body.String(), `data-connection-filter="`+c.ID+`"`) || !strings.Contains(filtered.Body.String(), `value="`+c.ID+`" selected`) {
		t.Fatal("repository picker did not select the discovered connection")
	}
	repos, _ := app.Engine.Store.Repositories()
	if len(repos) != 1 || repos[0].Selected {
		t.Fatal("discovery must not auto-select repositories")
	}
	r = request(app, "POST", "/repositories", url.Values{"csrf": {app.csrf}, "connection": {c.ID}, "repository": {repos[0].ID}}, nil)
	target, err = url.Parse(r.Header().Get("Location"))
	if r.Code != 303 || err != nil || target.Query().Get("connection") != c.ID {
		t.Fatal("saving selection lost the connection filter")
	}
	app.Engine.Factory = func(model.Connection, string) (provider.Provider, error) {
		return connectionProvider{failure: &provider.Error{Kind: "denied", Message: "Credential expired or rejected."}}, nil
	}
	values.Set("connection_id", c.ID)
	values.Set("token", "SYNTHETIC_REPLACEMENT")
	r = request(app, "POST", "/connections", values, nil)
	if !strings.Contains(r.Header().Get("Location"), "error=") {
		t.Fatal("expired credential accepted")
	}
	stored, _ = app.Engine.Secrets.Get(c.ID)
	if stored != token {
		t.Fatal("failed validation replaced prior credential")
	}
}

func TestConnectionFormsKeepIdentifiersOutOfMutationPaths(t *testing.T) {
	for _, id := range []string{"synthetic-safe", "../service/install?x=", "../service/remove?x=", `quoted"/雪`, "../refresh?x="} {
		t.Run(id, func(t *testing.T) {
			app := appFixture(t, false)
			c := model.Connection{ID: id, Name: "Synthetic restored connection", Provider: "github", BaseURL: "https://github.com", ReconnectRequired: true}
			if err := app.Engine.Store.SaveConnection(c); err != nil {
				t.Fatal(err)
			}
			response := request(app, "GET", "/connections", nil, nil)
			base, _ := url.Parse("http://127.0.0.1:7364/connections")
			forms := regexp.MustCompile(`<form[^>]*action="([^"]*)"[^>]*>(.*?)</form>`).FindAllStringSubmatch(response.Body.String(), -1)
			checked := 0
			for _, form := range forms {
				operation := ""
				switch {
				case strings.Contains(form[2], "Discover repositories"):
					operation = "discover"
				case strings.Contains(form[2], "Remove local credential"):
					operation = "forget"
				default:
					continue
				}
				action, err := base.Parse(html.UnescapeString(form[1]))
				if err != nil || action.Path != "/connections/"+operation || action.RawQuery != "" {
					t.Fatalf("connection ID changed the %s form destination: %s (%v)", operation, form[1], err)
				}
				if !strings.Contains(form[2], `name="connection_id" value="`+html.EscapeString(id)+`"`) {
					t.Fatal("form omitted its escaped connection identifier")
				}
				checked++
			}
			if checked != 2 {
				t.Fatalf("expected discovery and removal forms, got %d", checked)
			}
			values := url.Values{"csrf": {app.csrf}, "connection_id": {id}}
			// A restored connection remains quarantined and never reaches a provider.
			app.Engine.Factory = func(model.Connection, string) (provider.Provider, error) {
				t.Fatal("restored connection reached provider factory")
				return nil, nil
			}
			response = request(app, "POST", "/connections/discover?connection_id=other", values, nil)
			if response.Code != 303 || !strings.Contains(response.Header().Get("Location"), "error=") {
				t.Fatal("discovery did not retain credential quarantine")
			}
			response = request(app, "POST", "/connections/forget?connection_id=other", values, nil)
			stored, err := app.Engine.Store.Connection(id)
			if response.Code != 303 || err != nil || stored.Status != "disconnected" {
				t.Fatal("removal did not address the posted connection ID")
			}
			if response = request(app, "POST", "/connections/forget", url.Values{"connection_id": {id}}, nil); response.Code != 403 {
				t.Fatal("connection form bypassed CSRF protection")
			}
		})
	}
}

func TestReconnectRejectsRecipientDifferentFromDisplayedForm(t *testing.T) {
	for _, tc := range []struct {
		name, provider, base string
		accepted             bool
	}{
		{"default GitHub guidance", "github", "https://github.com", false},
		{"different GitLab recipient", "gitlab", "https://other.example", false},
		{"explicit matching recipient", "gitlab", "https://gitlab.example", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := appFixture(t, false)
			c := model.Connection{ID: "imported", Name: `Personal GitHub "synthetic"`, Provider: "gitlab", BaseURL: "https://gitlab.example", ReconnectRequired: true}
			if err := app.Engine.Store.SaveConnection(c); err != nil {
				t.Fatal(err)
			}
			page := request(app, "GET", "/connections", nil, nil).Body.String()
			if !strings.Contains(page, `data-provider="gitlab" data-base-url="https://gitlab.example" data-name="`+html.EscapeString(c.Name)+`"`) || !strings.Contains(page, `x-on:change="existingChanged"`) || !strings.Contains(page, "Verify that URL before entering a token") {
				t.Fatal("reconnection selector omitted the escaped saved recipient or guidance")
			}
			calls := 0
			app.Engine.Factory = func(got model.Connection, token string) (provider.Provider, error) {
				calls++
				if got.ID != c.ID || got.Provider != c.Provider || got.BaseURL != c.BaseURL || token != "SYNTHETIC_RECONNECT_TOKEN" {
					t.Fatal("connection validation used an unexpected recipient or token")
				}
				return connectionProvider{}, nil
			}
			values := url.Values{"csrf": {app.csrf}, "connection_id": {c.ID}, "name": {c.Name}, "provider": {tc.provider}, "base_url": {tc.base}, "token": {"SYNTHETIC_RECONNECT_TOKEN"}}
			response := request(app, "POST", "/connections", values, nil)
			stored, err := app.Engine.Store.Connection(c.ID)
			if response.Code != 303 || err != nil || stored.Provider != c.Provider || stored.BaseURL != c.BaseURL {
				t.Fatal("reconnection changed the stored recipient")
			}
			if tc.accepted {
				if calls != 1 || stored.ReconnectRequired || strings.Contains(response.Header().Get("Location"), "error=") {
					t.Fatal("matching explicit recipient did not reconnect")
				}
			} else if calls != 0 || !stored.ReconnectRequired || !strings.Contains(response.Header().Get("Location"), "error=") {
				t.Fatal("reconnection sent a token to a recipient different from the submitted form")
			}
		})
	}
}
