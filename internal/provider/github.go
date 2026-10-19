// SPDX-License-Identifier: GPL-3.0-only
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/model"
)

type github struct {
	api     *api
	browser string
}
type ghRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func (g *github) repo(r ghRepo) (model.Repository, error) {
	if r.ID <= 0 || strings.TrimSpace(r.FullName) == "" {
		return model.Repository{}, &Error{Kind: "error", Message: "GitHub returned incomplete repository metadata. Previous data is retained."}
	}
	return model.Repository{ExternalID: strconv.FormatInt(r.ID, 10), Name: r.FullName, Namespace: r.Owner.Login, URL: SafeURL(r.HTMLURL, g.browser), DefaultBranch: r.DefaultBranch, Archived: r.Archived}, nil
}
func (g *github) Validate(ctx context.Context) (Identity, error) {
	var v struct {
		Login string `json:"login"`
	}
	_, err := g.api.request(ctx, http.MethodGet, "user", nil, &v)
	if err == nil && v.Login == "" {
		err = &Error{Kind: "error", Message: "GitHub returned no account identity."}
	}
	return Identity{Name: v.Login, Version: "REST 2026-03-10"}, err
}
func (g *github) Discover(ctx context.Context) ([]model.Repository, error) {
	raw, err := pages[ghRepo](ctx, g.api, "user/repos?affiliation=owner,collaborator,organization_member&per_page=100&sort=full_name")
	if err != nil {
		return nil, err
	}
	repos := make([]model.Repository, 0, len(raw))
	for _, r := range raw {
		repo, err := g.repo(r)
		if err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, nil
}
func (g *github) RefreshRepository(ctx context.Context, r model.Repository) (model.Repository, error) {
	var raw ghRepo
	_, err := g.api.request(ctx, http.MethodGet, "repositories/"+url.PathEscape(r.ExternalID), nil, &raw)
	if err != nil {
		return r, err
	}
	fresh, err := g.repo(raw)
	if err != nil {
		return r, err
	}
	if fresh.ExternalID != r.ExternalID {
		return r, &Error{Kind: "error", Message: "GitHub returned a different repository identity. Previous data is retained."}
	}
	fresh.ID = r.ID
	fresh.ConnectionID = r.ConnectionID
	fresh.Provider = r.Provider
	fresh.Selected = r.Selected
	return fresh, nil
}

func (g *github) Collect(ctx context.Context, r model.Repository) []model.Snapshot {
	path := "repos/" + escapePath(r.Name) + "/"
	out := []model.Snapshot{g.dependencies(ctx, path), g.code(ctx, path, r.DefaultBranch), g.secrets(ctx, path)}
	for _, category := range []string{"containers", "iac", "dast", "other"} {
		out = append(out, unavailable(category, "No dedicated GitHub alert source for this category. Relevant SARIF tools remain visible under Code."))
	}
	return out
}

func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

type ghDependency struct {
	Number           int    `json:"number"`
	State            string `json:"state"`
	HTMLURL          string `json:"html_url"`
	SecurityAdvisory struct {
		Summary     string `json:"summary"`
		Severity    string `json:"severity"`
		Identifiers []struct {
			Value string `json:"value"`
		} `json:"identifiers"`
	} `json:"security_advisory"`
	Dependency struct {
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
	} `json:"dependency"`
}

func (g *github) dependencies(ctx context.Context, path string) model.Snapshot {
	items, err := pages[ghDependency](ctx, g.api, path+"dependabot/alerts?state=open,fixed,dismissed,auto_dismissed&per_page=100")
	if err != nil {
		return failed("dependencies", err)
	}
	s := model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "dependencies", ScannerState: "unknown", Note: "Alerts collected. Dependency analysis time and scanner enablement are not established by this endpoint."}}
	seen := map[int]bool{}
	for _, v := range items {
		if v.Number <= 0 || v.State == "" {
			return failed("dependencies", &Error{Kind: "error", Message: "GitHub returned an incomplete dependency alert."})
		}
		if seen[v.Number] {
			continue
		}
		seen[v.Number] = true
		ids := []string{}
		for _, id := range v.SecurityAdvisory.Identifiers {
			ids = append(ids, id.Value)
		}
		s.Findings = append(s.Findings, model.Finding{ID: strconv.Itoa(v.Number), Source: "github-dependabot", Title: v.SecurityAdvisory.Summary, Category: "dependencies", Severity: model.Severity(v.SecurityAdvisory.Severity), NativeSeverity: v.SecurityAdvisory.Severity, State: model.State(v.State), NativeState: v.State, URL: SafeURL(v.HTMLURL, g.browser), Package: v.Dependency.Package.Name, Identifiers: ids, Scanner: "Dependabot", Scope: "repository dependency graph"})
	}
	return s
}

type ghCode struct {
	Number  int    `json:"number"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Rule    struct {
		Description      string `json:"description"`
		Severity         string `json:"severity"`
		SecuritySeverity string `json:"security_severity_level"`
		ID               string `json:"id"`
	} `json:"rule"`
	Tool struct {
		Name string `json:"name"`
	} `json:"tool"`
	Instance struct {
		Ref    string `json:"ref"`
		Commit string `json:"commit_sha"`
	} `json:"most_recent_instance"`
}
type ghAnalysis struct {
	CreatedAt time.Time `json:"created_at"`
	Ref       string    `json:"ref"`
	Commit    string    `json:"commit_sha"`
	Error     string    `json:"error"`
	Warning   string    `json:"warning"`
	Tool      struct {
		Name string `json:"name"`
	} `json:"tool"`
	Category string `json:"category"`
}

func (g *github) code(ctx context.Context, path, branch string) model.Snapshot {
	query := url.Values{"per_page": {"100"}}
	if branch != "" {
		query.Set("ref", "refs/heads/"+branch)
	}
	items, err := pages[ghCode](ctx, g.api, path+"code-scanning/alerts?"+query.Encode())
	if err != nil {
		s := githubFailed("code", err)
		s.Coverage.Ref = query.Get("ref")
		return s
	}
	s := model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code", ScannerState: "unknown", Note: "Default-branch code alerts. Analysis evidence is unavailable."}}
	seen := map[int]bool{}
	for _, v := range items {
		if v.Number <= 0 || v.State == "" {
			return failed("code", &Error{Kind: "error", Message: "GitHub returned an incomplete code alert."})
		}
		if seen[v.Number] {
			continue
		}
		seen[v.Number] = true
		s.Findings = append(s.Findings, model.Finding{ID: strconv.Itoa(v.Number), Source: "github-code", Category: "code", Title: v.Rule.Description, Severity: model.Severity(v.Rule.SecuritySeverity), NativeSeverity: v.Rule.SecuritySeverity, DiagnosticSeverity: v.Rule.Severity, State: model.State(v.State), NativeState: v.State, URL: SafeURL(v.HTMLURL, g.browser), Scanner: v.Tool.Name, Identifiers: []string{v.Rule.ID}, Scope: v.Instance.Ref, Commit: v.Instance.Commit})
	}
	analyses, err := pages[ghAnalysis](ctx, g.api, path+"code-scanning/analyses?"+query.Encode())
	if err != nil {
		s := githubFailed("code", err)
		s.Coverage.Ref = query.Get("ref")
		return s
	}
	if len(analyses) > 0 {
		type stream struct{ tool, category, ref string }
		latest := map[stream]ghAnalysis{}
		successful := map[stream]ghAnalysis{}
		var oldest time.Time
		issues := 0
		for _, a := range analyses {
			key := stream{a.Tool.Name, a.Category, a.Ref}
			prior, ok := latest[key]
			if !ok || a.CreatedAt.After(prior.CreatedAt) {
				latest[key] = a
			}
			if a.Error == "" {
				prior, ok := successful[key]
				if !ok || a.CreatedAt.After(prior.CreatedAt) {
					successful[key] = a
				}
			}
		}
		for _, a := range latest {
			if a.Error != "" || a.Warning != "" {
				issues++
			}
		}
		for _, a := range successful {
			if oldest.IsZero() || a.CreatedAt.Before(oldest) {
				oldest = a.CreatedAt
			}
			if a.CreatedAt.Equal(oldest) {
				s.Coverage.Ref = a.Ref
				s.Coverage.Commit = a.Commit
			}
		}
		s.Coverage.AnalysisAt = oldest
		s.Coverage.ScannerState = "observed"
		if issues > 0 {
			s.Coverage.ScannerState = "warning"
		}
		s.Coverage.Note = fmt.Sprintf("%d tool/category analysis streams; oldest latest successful analysis shown. %d stream warnings/errors. Open provider for details.", len(latest), issues)
	}
	var setup struct {
		State string `json:"state"`
	}
	if _, e := g.api.request(ctx, http.MethodGet, path+"code-scanning/default-setup", nil, &setup); e == nil {
		if setup.State == "configured" {
			s.Coverage.Note += " CodeQL default setup is configured."
		}
		if setup.State == "not-configured" {
			s.Coverage.Note += " CodeQL default setup is not configured; advanced setup may still be present."
		}
	}
	return s
}

type ghSecret struct {
	Number     int    `json:"number"`
	State      string `json:"state"`
	HTMLURL    string `json:"html_url"`
	Type       string `json:"secret_type"`
	Display    string `json:"secret_type_display_name"`
	Resolution string `json:"resolution"`
}

func (g *github) secrets(ctx context.Context, path string) model.Snapshot {
	items, err := pages[ghSecret](ctx, g.api, path+"secret-scanning/alerts?hide_secret=true&per_page=100")
	if err != nil {
		return githubFailed("secrets", err)
	}
	s := model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "secrets", ScannerState: "unknown", Note: "Provider/default pattern alerts. Generic/AI password and partner-only pattern coverage is not established. No secret values or locations are stored."}}
	seen := map[int]bool{}
	// The default excludes generic patterns. Complete both streams before replacing
	// a category, batching the documented selectors to avoid nine extra API calls.
	selectors := []string{"http_basic_authentication_header", "http_bearer_authentication_header", "mongodb_connection_string", "mysql_connection_url", "postgres_connection_string", "openssh_private_key", "rsa_private_key", "ec_private_key", "pgp_private_key"}
	more, e := pages[ghSecret](ctx, g.api, path+"secret-scanning/alerts?hide_secret=true&per_page=100&secret_type="+url.QueryEscape(strings.Join(selectors, ",")))
	if e != nil {
		return githubFailed("secrets", e)
	}
	items = append(items, more...)
	for _, v := range items {
		if v.Number <= 0 || v.State == "" {
			return failed("secrets", &Error{Kind: "error", Message: "GitHub returned an incomplete secret alert."})
		}
		if seen[v.Number] {
			continue
		}
		seen[v.Number] = true
		label := v.Display
		if label == "" {
			label = v.Type
		}
		if label == "" {
			label = "Credential"
		}
		s.Findings = append(s.Findings, model.Finding{ID: strconv.Itoa(v.Number), Source: "github-secrets", Category: "secrets", Title: label + " exposed", Severity: "unknown", NativeSeverity: "not supplied", State: model.State(v.State), NativeState: v.State, NativeResolution: v.Resolution, URL: SafeURL(v.HTMLURL, g.browser), Scanner: "GitHub secret scanning", Scope: "repository history; pattern coverage may be partial"})
	}
	return s
}

func githubFailed(category string, err error) model.Snapshot {
	var failure *Error
	if errors.As(err, &failure) && (failure.Kind == "denied" || failure.Kind == "unavailable") {
		switch {
		case category == "code" && failure.code == "github_code_disabled":
			return failed(category, &Error{Kind: "disabled", Message: "GitHub reports that code scanning is disabled for this repository. Previous findings are retained; code coverage is missing."})
		case category == "secrets" && failure.code == "github_secrets_disabled":
			return failed(category, &Error{Kind: "disabled", Message: "GitHub reports that secret scanning is disabled for this repository. Previous findings are retained; secret coverage is missing."})
		case category == "code" && failure.code == "github_no_analysis":
			return failed(category, &Error{Kind: "no_analysis", Message: "GitHub reports no code analysis for the requested default branch. Previous findings are retained; this is not a zero-finding report."})
		}
	}
	return failed(category, err)
}
