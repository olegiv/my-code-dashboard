// SPDX-License-Identifier: GPL-3.0-only
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/olegiv/my-code-dashboard/internal/model"
)

type gitlab struct {
	api  *api
	base string
}
type glProject struct {
	ID            int64  `json:"id"`
	Path          string `json:"path_with_namespace"`
	WebURL        string `json:"web_url"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	Namespace     struct {
		FullPath string `json:"full_path"`
	} `json:"namespace"`
}

func (g *gitlab) repo(v glProject) (model.Repository, error) {
	if v.ID <= 0 || strings.TrimSpace(v.Path) == "" {
		return model.Repository{}, &Error{Kind: "error", Message: "GitLab returned incomplete project metadata. Previous data is retained."}
	}
	return model.Repository{ExternalID: strconv.FormatInt(v.ID, 10), Name: v.Path, Namespace: v.Namespace.FullPath, URL: SafeURL(v.WebURL, g.base), DefaultBranch: v.DefaultBranch, Archived: v.Archived}, nil
}
func (g *gitlab) Validate(ctx context.Context) (Identity, error) {
	var user struct {
		Username string `json:"username"`
	}
	_, err := g.api.request(ctx, http.MethodGet, "user", nil, &user)
	if err != nil {
		return Identity{}, err
	}
	if user.Username == "" {
		return Identity{}, errors.New("GitLab returned no account identity")
	}
	var metadata struct {
		Version string `json:"version"`
	}
	if _, err = g.api.request(ctx, http.MethodGet, "metadata", nil, &metadata); err != nil {
		_, _ = g.api.request(ctx, http.MethodGet, "version", nil, &metadata)
	}
	return Identity{Name: user.Username, Version: metadata.Version}, nil
}
func (g *gitlab) Discover(ctx context.Context) ([]model.Repository, error) {
	items, err := pages[glProject](ctx, g.api, "projects?pagination=keyset&order_by=id&sort=asc&per_page=100")
	if err != nil {
		return nil, err
	}
	result := make([]model.Repository, 0, len(items))
	for _, v := range items {
		repo, err := g.repo(v)
		if err != nil {
			return nil, err
		}
		result = append(result, repo)
	}
	return result, nil
}
func (g *gitlab) RefreshRepository(ctx context.Context, r model.Repository) (model.Repository, error) {
	var v glProject
	_, err := g.api.request(ctx, http.MethodGet, "projects/"+url.PathEscape(r.ExternalID), nil, &v)
	if err != nil {
		return r, err
	}
	fresh, err := g.repo(v)
	if err != nil {
		return r, err
	}
	if fresh.ExternalID != r.ExternalID {
		return r, &Error{Kind: "error", Message: "GitLab returned a different project identity. Previous data is retained."}
	}
	fresh.ID = r.ID
	fresh.ConnectionID = r.ConnectionID
	fresh.Provider = r.Provider
	fresh.Selected = r.Selected
	return fresh, nil
}

type gqlEnvelope struct {
	Data   json.RawMessage   `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

func (g *gitlab) gql(ctx context.Context, query string, variables any, out any) error {
	var envelope gqlEnvelope
	_, err := g.api.request(ctx, http.MethodPost, g.base+"/api/graphql", map[string]any{"query": query, "variables": variables}, &envelope)
	if err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		return &Error{Kind: "unavailable", Message: "GitLab rejected a read-only query. Check instance schema, vulnerability permissions, and security feature availability."}
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, out) != nil {
		return &Error{Kind: "unavailable", Message: "GitLab did not return accessible GraphQL data."}
	}
	return nil
}

type glField struct {
	Name string `json:"name"`
	Args []struct {
		Name string `json:"name"`
	} `json:"args"`
}
type glSchema struct {
	Vulnerability *struct {
		Fields []glField `json:"fields"`
	} `json:"vulnerability"`
	Project *struct {
		Fields []glField `json:"fields"`
	} `json:"project"`
	State *struct {
		Values []struct {
			Name string `json:"name"`
		} `json:"enumValues"`
	} `json:"state"`
	Scanners *struct {
		Fields []glField `json:"fields"`
	} `json:"scanners"`
	Scanner *struct {
		Fields []glField `json:"fields"`
	} `json:"scanner"`
}

const schemaQuery = `query DashboardCapabilities {
 vulnerability: __type(name:"Vulnerability") { fields { name } }
 project: __type(name:"Project") { fields { name args { name } } }
 state: __type(name:"VulnerabilityState") { enumValues { name } }
 scanners: __type(name:"SecurityScanners") { fields { name } }
 scanner: __type(name:"VulnerabilityScanner") { fields { name } }
}`

type glVulnerability struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Severity   string `json:"severity"`
	ReportType string `json:"reportType"`
	WebURL     string `json:"webUrl"`
	Scanner    struct {
		Name string `json:"name"`
	} `json:"scanner"`
}

func (g *gitlab) Collect(ctx context.Context, r model.Repository) []model.Snapshot {
	var schema glSchema
	err := g.gql(ctx, schemaQuery, map[string]any{}, &schema)
	if err != nil {
		if kind, _ := Failure(err); kind != "unavailable" {
			return glFailed(err)
		}
	}
	introspected := err == nil && schema.Vulnerability != nil && schema.Project != nil
	// Some self-managed instances hide introspection while allowing report reads.
	// The server validates this minimal query's fields, pagination, and all four
	// lifecycle states. Only a complete successful response establishes support.
	selection := "id title state severity reportType"
	filters := "first:100,after:$cursor,state:[DETECTED,CONFIRMED,RESOLVED,DISMISSED]"
	if introspected {
		fields := map[string]bool{}
		for _, f := range schema.Vulnerability.Fields {
			fields[f.Name] = true
		}
		for _, name := range []string{"id", "title", "state", "severity", "reportType"} {
			if !fields[name] {
				return glFailed(&Error{Kind: "unsupported", Message: "Required vulnerability fields are unavailable on this instance."})
			}
		}
		args := map[string]bool{}
		hasVulns := false
		for _, f := range schema.Project.Fields {
			if f.Name == "vulnerabilities" {
				hasVulns = true
				for _, a := range f.Args {
					args[a.Name] = true
				}
			}
		}
		if !hasVulns || !args["first"] || !args["after"] {
			return glFailed(&Error{Kind: "unsupported", Message: "Project vulnerability pagination is unavailable on this GitLab instance."})
		}
		if fields["webUrl"] {
			selection += " webUrl"
		}
		if fields["scanner"] && schema.Scanner != nil {
			for _, f := range schema.Scanner.Fields {
				if f.Name == "name" {
					selection += " scanner { name }"
					break
				}
			}
		}
		if args["state"] && schema.State != nil {
			states := []string{}
			for _, v := range schema.State.Values {
				switch v.Name {
				case "DETECTED", "CONFIRMED", "RESOLVED", "DISMISSED":
					states = append(states, v.Name)
				}
			}
			if len(states) == 4 {
				filters = "first:100,after:$cursor,state:[" + strings.Join(states, ",") + "]"
			} else {
				return glFailed(&Error{Kind: "unsupported", Message: "The instance cannot explicitly query all four vulnerability lifecycle states."})
			}
		} else {
			return glFailed(&Error{Kind: "unsupported", Message: "The instance does not expose the lifecycle filter needed for a complete vulnerability report."})
		}
	}
	// Only a fixed field allowlist is interpolated; provider values are variables.
	query := `query DashboardFindings($path:ID!,$cursor:String){project(fullPath:$path){vulnerabilities(` + filters + `){nodes{` + selection + `} pageInfo{hasNextPage endCursor}}}}`
	all := []glVulnerability{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 1001; page++ {
		if page == 1000 {
			return glFailed(&Error{Kind: "error", Message: "GitLab pagination exceeded the safe page limit."})
		}
		var data struct {
			Project *struct {
				Vulnerabilities *struct {
					Nodes    []glVulnerability `json:"nodes"`
					PageInfo *struct {
						HasNext *bool  `json:"hasNextPage"`
						Cursor  string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"vulnerabilities"`
			} `json:"project"`
		}
		var variableCursor any
		if cursor != "" {
			variableCursor = cursor
		}
		err = g.gql(ctx, query, map[string]any{"path": r.Name, "cursor": variableCursor}, &data)
		if err != nil {
			return glFailed(err)
		}
		if data.Project == nil || data.Project.Vulnerabilities == nil {
			return glFailed(&Error{Kind: "denied", Message: "Project vulnerability reports are inaccessible. Check your role and GitLab security license/features."})
		}
		v := data.Project.Vulnerabilities
		if v.Nodes == nil {
			return glFailed(&Error{Kind: "unavailable", Message: "GitLab returned null vulnerability nodes instead of an accessible report."})
		}
		if v.PageInfo == nil || v.PageInfo.HasNext == nil {
			return glFailed(&Error{Kind: "unavailable", Message: "GitLab did not return complete vulnerability pagination metadata. Previous findings are retained."})
		}
		all = append(all, v.Nodes...)
		if len(all) > 100000 {
			return glFailed(&Error{Kind: "error", Message: "GitLab project exceeds 100,000 vulnerability records."})
		}
		if !*v.PageInfo.HasNext {
			break
		}
		cursor = v.PageInfo.Cursor
		if cursor == "" || seen[cursor] {
			return glFailed(&Error{Kind: "error", Message: "GitLab returned incomplete or repeated pagination."})
		}
		seen[cursor] = true
	}
	result := map[string]*model.Snapshot{}
	for _, category := range model.Categories {
		result[category] = &model.Snapshot{Complete: true, Coverage: model.Coverage{Category: category, ScannerState: "unknown", Note: "Existing project vulnerability report. Scanner enablement and latest analysis time are not established by this report."}}
	}
	seenFinding := map[string]bool{}
	for _, v := range all {
		if v.ID == "" || v.State == "" || v.ReportType == "" {
			return glFailed(&Error{Kind: "error", Message: "GitLab vulnerability was missing its stable identifier."})
		}
		if seenFinding[v.ID] {
			continue
		}
		seenFinding[v.ID] = true
		category := glCategory(v.ReportType)
		s := result[category]
		title := v.Title
		if category == "secrets" {
			title = "Credential detected by GitLab secret detection"
		}
		link := SafeURL(v.WebURL, g.base)
		if link == "" {
			parts := strings.Split(v.ID, "/")
			number := parts[len(parts)-1]
			if _, e := strconv.ParseInt(number, 10, 64); e == nil {
				link = SafeURL(g.base+"/"+escapePath(r.Name)+"/-/security/vulnerabilities/"+number, g.base)
			}
		}
		scanner := v.Scanner.Name
		if scanner == "" {
			scanner = v.ReportType
		}
		s.Findings = append(s.Findings, model.Finding{ID: v.ID, Source: "gitlab-vulnerability", Category: category, Title: title, Severity: model.Severity(v.Severity), NativeSeverity: v.Severity, State: model.State(v.State), NativeState: v.State, URL: link, Scanner: scanner, Scope: "persistent project report; provider default/tracked refs"})
	}
	if !introspected {
		schema = glSchema{}
	}
	g.scannerCoverage(ctx, r, schema, result)
	out := make([]model.Snapshot, 0, len(model.Categories))
	for _, category := range model.Categories {
		if !introspected {
			result[category].Coverage.Note += " Report fields and lifecycle pagination were verified by a successful read-only query; schema introspection is unavailable."
		}
		out = append(out, *result[category])
	}
	return out
}

func glCategory(report string) string {
	switch strings.ToUpper(report) {
	case "DEPENDENCY_SCANNING":
		return "dependencies"
	case "SAST", "SARIF":
		return "code"
	case "SECRET_DETECTION":
		return "secrets"
	case "CONTAINER_SCANNING", "CLUSTER_IMAGE_SCANNING", "CONTAINER_SCANNING_FOR_REGISTRY":
		return "containers"
	case "DAST", "API_FUZZING", "COVERAGE_FUZZING":
		return "dast"
	case "IAC", "IAC_SCANNING":
		return "iac"
	default:
		return "other"
	}
}
func glFailed(err error) []model.Snapshot {
	out := make([]model.Snapshot, 0, len(model.Categories))
	for _, c := range model.Categories {
		out = append(out, failed(c, err))
	}
	return out
}

func (g *gitlab) scannerCoverage(ctx context.Context, r model.Repository, schema glSchema, result map[string]*model.Snapshot) {
	// With no introspection, this optional read-only query can still establish
	// scanner evidence. A rejected/null response leaves scanner state unknown.
	if schema.Project != nil {
		if schema.Scanners == nil {
			return
		}
		has := false
		for _, f := range schema.Project.Fields {
			if f.Name == "securityScanners" {
				has = true
			}
		}
		if !has {
			return
		}
		fields := map[string]bool{}
		for _, f := range schema.Scanners.Fields {
			fields[f.Name] = true
		}
		if !fields["available"] || !fields["enabled"] || !fields["pipelineRun"] {
			return
		}
	}
	var data struct {
		Project *struct {
			Scanners *struct {
				Available   []string `json:"available"`
				Enabled     []string `json:"enabled"`
				PipelineRun []string `json:"pipelineRun"`
			} `json:"securityScanners"`
		} `json:"project"`
	}
	query := `query DashboardScanners($path:ID!){project(fullPath:$path){securityScanners{available enabled pipelineRun}}}`
	if err := g.gql(ctx, query, map[string]any{"path": r.Name}, &data); err != nil {
		var failure *Error
		if errors.As(err, &failure) && !failure.RetryAt.IsZero() {
			for _, snapshot := range result {
				snapshot.Coverage.RetryAt = failure.RetryAt
				snapshot.Coverage.Note += " Optional scanner metadata reached the provider rate limit; the complete vulnerability report was imported and the retry deadline is retained."
			}
		}
		return
	}
	if data.Project == nil || data.Project.Scanners == nil {
		return
	}
	v := data.Project.Scanners
	// Null arrays are inaccessible metadata, not an empty set of analyzers.
	if v.Available == nil || v.Enabled == nil || v.PipelineRun == nil {
		return
	}
	available, enabled, ran := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, x := range v.Available {
		available[glCategory(x)] = true
	}
	for _, x := range v.Enabled {
		enabled[glCategory(x)] = true
	}
	for _, x := range v.PipelineRun {
		ran[glCategory(x)] = true
	}
	for category, s := range result {
		if category == "other" {
			continue
		}
		if category == "iac" && !available[category] {
			continue
		}
		s.Coverage.ScannerState = "unavailable"
		if available[category] {
			s.Coverage.ScannerState = "disabled"
		}
		if enabled[category] {
			s.Coverage.ScannerState = "enabled"
		}
		if ran[category] {
			s.Coverage.ScannerState = "observed"
		}
		s.Coverage.Note = "Scanner state comes from project securityScanners (available/enabled/latest pipeline success). The report does not expose a per-scanner analysis timestamp. IaC may be included in SAST; category parity is not implied."
		if s.Coverage.ScannerState == "enabled" {
			s.Coverage.Note += " The scanner is enabled, but no successful run is reported for the latest pipeline."
		}
	}
}
