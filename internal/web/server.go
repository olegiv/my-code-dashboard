// SPDX-License-Identifier: GPL-3.0-only
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/engine"
	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/platform"
	"github.com/olegiv/my-code-dashboard/internal/provider"
)

//go:embed templates/*.html assets
var content embed.FS

type Server struct {
	Engine                 *engine.Engine
	DataDir, Addr, Version string
	SecretFiles            bool
	csrf                   string
	templates              *template.Template
	mux                    *http.ServeMux
}
type RepoRow struct {
	model.Repository
	Open        int
	Critical    int
	High        int
	Issues      int
	LastSuccess time.Time
	Coverage    []model.Coverage
}
type FindingRow struct {
	model.Finding
	RepositoryName, Provider string
}
type ChangeRow struct {
	model.Change
	RepositoryName string
}
type View struct {
	Page, Title, CSRF, Version, Notice, Error, Active, DataDir, OS, Backend                                                    string
	Demo, SecretFiles, ServiceInstalled                                                                                        bool
	Connections                                                                                                                []model.Connection
	Repositories                                                                                                               []RepoRow
	Findings                                                                                                                   []FindingRow
	Changes                                                                                                                    []ChangeRow
	Runs                                                                                                                       []model.Run
	Settings                                                                                                                   model.Settings
	SelectedRepo                                                                                                               *RepoRow
	Severities, Categories                                                                                                     []string
	NativeStates, Namespaces                                                                                                   []string
	Query, Provider, Category, Severity, State, Repo, NativeState, Namespace                                                   string
	TotalRepos, Monitored, Open, Critical, High, Medium, Unknown, Issues, Stale, Observed, FindingCount, PageNumber, PageCount int
	PreviousURL, NextURL                                                                                                       string
	ConnectionFilter                                                                                                           string
	Now                                                                                                                        time.Time
}

func New(e *engine.Engine, dir, addr, version string) (*Server, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	s := &Server{Engine: e, DataDir: dir, Addr: addr, Version: version, csrf: hex.EncodeToString(token[:]), mux: http.NewServeMux()}
	functions := template.FuncMap{
		"trim": strings.TrimSpace,
		"age": func(t time.Time) string {
			if t.IsZero() {
				return "Not reported"
			}
			d := e.Now().Sub(t)
			if d < time.Minute {
				return "Just now"
			}
			if d < time.Hour {
				return fmt.Sprintf("%dm ago", int(d.Minutes()))
			}
			if d < 48*time.Hour {
				return fmt.Sprintf("%dh ago", int(d.Hours()))
			}
			return fmt.Sprintf("%dd ago", int(d.Hours()/24))
		},
		"date": func(t time.Time, zone string) string {
			if t.IsZero() {
				return "Not reported"
			}
			loc, err := time.LoadLocation(zone)
			if err != nil {
				loc = time.UTC
			}
			return t.In(loc).Format("02 Jan 2006 · 15:04 MST")
		},
		"clock":       func(hour, minute int) string { return fmt.Sprintf("%02d:%02d", hour, minute) },
		"stale":       func(t time.Time) bool { return !t.IsZero() && e.Now().Sub(t) > 48*time.Hour },
		"repoLink":    func(id string) string { return "/repositories/view?id=" + url.QueryEscape(id) },
		"findingLink": func(id string) string { return "/findings?repo=" + url.QueryEscape(id) + "&state=open" },
		"short": func(v string) string {
			if len(v) > 12 {
				return v[:12]
			}
			return v
		},
		"initials": func(v string) string {
			if v == "" {
				return "?"
			}
			return strings.ToUpper(string([]rune(v)[0]))
		},
		"title": func(v string) string {
			switch v {
			case "github":
				return "GitHub"
			case "gitlab":
				return "GitLab"
			case "iac":
				return "IaC"
			case "dast":
				return "DAST"
			case "code":
				return "Code / SAST"
			case "dependencies":
				return "Dependencies"
			case "secrets":
				return "Secrets"
			case "no_analysis":
				return "Missing analysis"
			case "not_observed":
				return "No analysis observed"
			case "coverage_gaps":
				return "Coverage gaps"
			case "rate_limited":
				return "Rate limited"
			case "denied":
				return "Access denied"
			case "error":
				return "Failed"
			}
			if v == "" {
				return "Unknown"
			}
			runes := []rune(v)
			return strings.ToUpper(string(runes[0])) + string(runes[1:])
		},
	}
	var err error
	s.templates, err = template.New("dashboard").Funcs(functions).ParseFS(content, "templates/*.html")
	if err != nil {
		return nil, err
	}
	assets, _ := fs.Sub(content, "assets")
	s.mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	s.mux.HandleFunc("GET /{$}", s.page("overview", "Security overview"))
	s.mux.HandleFunc("GET /repositories", s.page("repositories", "Repositories"))
	s.mux.HandleFunc("GET /repositories/view", s.page("repository", "Repository details"))
	s.mux.HandleFunc("GET /findings", s.page("findings", "Findings"))
	s.mux.HandleFunc("GET /connections", s.page("connections", "Connections"))
	s.mux.HandleFunc("GET /activity", s.page("activity", "Refresh activity"))
	s.mux.HandleFunc("GET /settings", s.page("settings", "Settings"))
	s.mux.HandleFunc("GET /status", s.status)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	})
	s.mux.HandleFunc("POST /refresh", s.refresh)
	s.mux.HandleFunc("POST /connections", s.connect)
	s.mux.HandleFunc("POST /connections/discover", s.discover)
	s.mux.HandleFunc("POST /connections/forget", s.forget)
	s.mux.HandleFunc("POST /repositories", s.selectRepos)
	s.mux.HandleFunc("POST /settings", s.settings)
	s.mux.HandleFunc("POST /backup", s.backup)
	s.mux.HandleFunc("POST /service/{action}", s.service)
	return s, nil
}

func (s *Server) Handler() http.Handler {
	protected := http.NewCrossOriginProtection().Handler(s.mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Vary", "HX-Request, HX-History-Restore-Request")
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil {
			host, port, err = net.SplitHostPort(r.Host + ":80")
		}
		_, expectedPort, _ := net.SplitHostPort(s.Addr)
		ip := net.ParseIP(host)
		if err != nil || (host != "localhost" && (ip == nil || !ip.IsLoopback())) || port != expectedPort {
			http.Error(w, "Invalid local host", http.StatusForbidden)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if err = r.ParseForm(); err != nil {
				http.Error(w, "Invalid or oversized form", http.StatusBadRequest)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(s.csrf)) != 1 {
				http.Error(w, "Invalid CSRF token. Reload the page and retry.", http.StatusForbidden)
				return
			}
		}
		protected.ServeHTTP(w, r)
	})
}

func (s *Server) view(r *http.Request, page, title string) (View, error) {
	v := View{Page: page, Title: title, CSRF: s.csrf, Version: s.Version, Demo: s.Engine.Demo, SecretFiles: s.SecretFiles, Active: s.Engine.Active(), DataDir: s.DataDir, OS: runtime.GOOS, Severities: model.Severities, Categories: model.Categories, Notice: r.URL.Query().Get("notice"), Error: r.URL.Query().Get("error"), Now: s.Engine.Now()}
	var err error
	if v.Connections, err = s.Engine.Store.Connections(); err != nil {
		return v, err
	}
	if v.Settings, err = s.Engine.Store.Settings(); err != nil {
		return v, err
	}
	if v.Runs, err = s.Engine.Store.Runs(); err != nil {
		return v, err
	}
	repos, err := s.Engine.Store.Repositories()
	if err != nil {
		return v, err
	}
	findings, err := s.Engine.Store.Findings()
	if err != nil {
		return v, err
	}
	coverage, err := s.Engine.Store.Coverage()
	if err != nil {
		return v, err
	}
	changes, err := s.Engine.Store.Changes()
	if err != nil {
		return v, err
	}
	lookup := map[string]model.Repository{}
	byRepoCoverage := map[string][]model.Coverage{}
	byRepoFindings := map[string][]model.Finding{}
	nativeStates, namespaces := map[string]bool{}, map[string]bool{}
	for _, c := range coverage {
		byRepoCoverage[c.RepositoryID] = append(byRepoCoverage[c.RepositoryID], c)
	}
	for _, f := range findings {
		byRepoFindings[f.RepositoryID] = append(byRepoFindings[f.RepositoryID], f)
		if f.NativeState != "" {
			nativeStates[f.NativeState] = true
		}
	}
	for _, repo := range repos {
		lookup[repo.ID] = repo
		namespaces[repo.Namespace] = true
	}
	for x := range nativeStates {
		v.NativeStates = append(v.NativeStates, x)
	}
	slices.Sort(v.NativeStates)
	for x := range namespaces {
		v.Namespaces = append(v.Namespaces, x)
	}
	slices.Sort(v.Namespaces)
	q := r.URL.Query()
	for _, connection := range v.Connections {
		if connection.ID == q.Get("connection") {
			v.ConnectionFilter = connection.ID
			break
		}
	}
	v.Query = q.Get("q")
	v.Provider = q.Get("provider")
	v.Category = q.Get("category")
	v.Severity = q.Get("severity")
	v.State = q.Get("state")
	if page == "repository" || page == "overview" || (page == "findings" && v.State == "") {
		v.State = "open"
		q.Set("state", v.State)
	}
	v.Repo = q.Get("repo")
	v.NativeState = q.Get("native_state")
	v.Namespace = q.Get("namespace")
	for _, repo := range repos {
		row := RepoRow{Repository: repo}
		for _, category := range model.Categories {
			if !slices.ContainsFunc(byRepoCoverage[repo.ID], func(c model.Coverage) bool { return c.Category == category }) {
				byRepoCoverage[repo.ID] = append(byRepoCoverage[repo.ID], model.Coverage{Category: category, Collection: "unknown", ScannerState: "unknown", Note: "No report has been collected for this category. Select this repository and refresh to establish access and coverage."})
			}
		}
		v.TotalRepos++
		if repo.Selected {
			v.Monitored++
		}
		for _, c := range byRepoCoverage[repo.ID] {
			row.Coverage = append(row.Coverage, c)
			if c.LastSuccess.After(row.LastSuccess) {
				row.LastSuccess = c.LastSuccess
			}
			if (c.Collection != "success" && c.Collection != "unsupported") || c.ScannerState == "warning" || c.ScannerState == "disabled" || c.ScannerState == "enabled" {
				row.Issues++
				if repo.Selected {
					v.Issues++
				}
			}
			if repo.Selected && c.ScannerState == "observed" {
				v.Observed++
			}
			if repo.Selected && !c.LastSuccess.IsZero() && v.Now.Sub(c.LastSuccess) > 48*time.Hour {
				v.Stale++
			}
		}
		for _, f := range byRepoFindings[repo.ID] {
			if f.RepositoryID == repo.ID && f.State == "open" {
				row.Open++
				if f.Severity == "critical" {
					row.Critical++
				}
				if f.Severity == "high" {
					row.High++
				}
			}
		}
		if page == "repository" && repo.ID == q.Get("id") {
			copy := row
			v.SelectedRepo = &copy
			v.Title = repo.Name
			v.Repo = repo.ID
			q.Set("repo", repo.ID)
			q.Set("state", v.State)
			q.Del("id")
		}
		v.Repositories = append(v.Repositories, row)
	}
	v.addFindings(findings, lookup)
	for _, c := range changes {
		if repo, ok := lookup[c.RepositoryID]; ok && repo.Selected && (v.Repo == "" || c.RepositoryID == v.Repo) {
			v.Changes = append(v.Changes, ChangeRow{Change: c, RepositoryName: repo.Name})
		}
	}
	if page == "overview" && len(v.Changes) > 8 {
		v.Changes = v.Changes[:8]
	}
	v.paginateFindings(q)
	if runtime.GOOS == "darwin" {
		path, e := platform.ServicePath(v.Settings.InstallationID)
		if e == nil {
			_, e = os.Stat(path)
			v.ServiceInstalled = e == nil
		}
	}
	if v.SecretFiles {
		v.Backend = "Provisioned credential files"
	} else {
		v.Backend = "System keychain"
	}
	if v.Demo {
		v.Backend = "Demo · no credentials"
	}
	return v, nil
}

func (s *Server) render(w http.ResponseWriter, name string, v View) {
	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, name, v); err != nil {
		http.Error(w, "Could not render dashboard", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}
func (s *Server) page(page, title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := s.view(r, page, title)
		if err != nil {
			http.Error(w, "Could not read local dashboard data", 500)
			return
		}
		if page == "repository" && v.SelectedRepo == nil {
			http.NotFound(w, r)
			return
		}
		if page == "findings" && r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true" {
			s.render(w, "finding-results", v)
		} else {
			s.render(w, "shell", v)
		}
	}
}
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, path, key, message string) {
	http.Redirect(w, r, path+"?"+url.Values{key: {message}}.Encode(), http.StatusSeeOther)
}
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	_, _, err := s.Engine.Start("manual")
	if err != nil {
		s.redirect(w, r, "/activity", "error", "Could not start refresh")
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		active := s.Engine.Active()
		if active == "" {
			w.Header().Set("HX-Refresh", "true")
		}
		s.render(w, "refresh-status", View{Active: active, CSRF: s.csrf})
		return
	}
	s.redirect(w, r, "/activity", "notice", "Refresh requested. Previous complete data remains available during collection.")
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	active := s.Engine.Active()
	if active == "" && r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
	}
	s.render(w, "refresh-status", View{Active: active, CSRF: s.csrf})
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if s.Engine.Demo {
		s.redirect(w, r, "/connections", "error", "Demo mode does not accept credentials.")
		return
	}
	if s.SecretFiles {
		s.redirect(w, r, "/connections", "error", "Provision headless credentials outside the browser application.")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 100 {
		s.redirect(w, r, "/connections", "error", "Provide a connection name of up to 100 characters.")
		return
	}
	base, err := provider.ValidateBase(r.FormValue("base_url"))
	if err != nil {
		s.redirect(w, r, "/connections", "error", err.Error())
		return
	}
	c := model.Connection{ID: r.FormValue("connection_id"), Name: name, Provider: r.FormValue("provider"), BaseURL: base}
	if c.ID != "" {
		old, e := s.Engine.Store.Connection(c.ID)
		if e != nil {
			s.redirect(w, r, "/connections", "error", "Connection not found.")
			return
		}
		savedBase, e := provider.ValidateBase(old.BaseURL)
		if e != nil || c.Provider != old.Provider || c.BaseURL != savedBase {
			s.redirect(w, r, "/connections", "error", "Select the existing connection and verify its provider URL before entering a token. The submitted provider and URL must match the saved connection.")
			return
		}
		c = old
		c.Name = name
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" || len(token) > 16384 {
		s.redirect(w, r, "/connections", "error", "Enter a provider access token.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err = s.Engine.Connect(ctx, c, token); err != nil {
		s.redirect(w, r, "/connections", "error", err.Error())
		return
	}
	s.redirect(w, r, "/connections", "notice", "Connection validated and credential saved in the system keychain. Discover repositories next.")
}
func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	id := r.PostForm.Get("connection_id")
	if err := s.Engine.Discover(ctx, id); err != nil {
		s.redirect(w, r, "/connections", "error", err.Error())
		return
	}
	s.redirectRepositories(w, r, id, "notice", "Discovery complete. Select repositories and save your selection.")
}

func (s *Server) redirectRepositories(w http.ResponseWriter, r *http.Request, connectionID, key, message string) {
	query := url.Values{key: {message}}
	if connectionID != "" {
		query.Set("connection", connectionID)
	}
	http.Redirect(w, r, "/repositories?"+query.Encode(), http.StatusSeeOther)
}
func (s *Server) forget(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.ForgetCredential(r.PostForm.Get("connection_id")); err != nil {
		s.redirect(w, r, "/connections", "error", err.Error())
		return
	}
	s.redirect(w, r, "/connections", "notice", "Local credential removed. Existing findings are retained.")
}
func (s *Server) selectRepos(w http.ResponseWriter, r *http.Request) {
	ids := map[string]bool{}
	for _, id := range r.PostForm["repository"] {
		ids[id] = true
	}
	if err := s.Engine.Store.SelectRepositories(ids); err != nil {
		s.redirectRepositories(w, r, r.PostForm.Get("connection"), "error", "Could not save repository selection.")
		return
	}
	s.redirectRepositories(w, r, r.PostForm.Get("connection"), "notice", "Monitoring selection saved. Refresh now to collect findings.")
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.FormValue("time"), ":")
	if len(parts) != 2 {
		s.redirect(w, r, "/settings", "error", "Use a valid refresh time.")
		return
	}
	hour, e1 := strconv.Atoi(parts[0])
	minute, e2 := strconv.Atoi(parts[1])
	changes, e3 := strconv.Atoi(r.FormValue("changes_days"))
	logs, e4 := strconv.Atoi(r.FormValue("logs_days"))
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		s.redirect(w, r, "/settings", "error", "Use valid schedule and retention values.")
		return
	}
	if err := s.Engine.SavePreferences(hour, minute, r.FormValue("timezone"), r.FormValue("enabled") == "on", changes, logs); err != nil {
		s.redirect(w, r, "/settings", "error", err.Error())
		return
	}
	s.redirect(w, r, "/settings", "notice", "Schedule and retention settings saved.")
}
func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp(s.DataDir, "export-")
	if err != nil {
		http.Error(w, "Could not prepare backup", 500)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "backup.tar.gz")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		http.Error(w, "Could not prepare backup", 500)
		return
	}
	defer f.Close()
	if err = platform.Export(r.Context(), s.Engine.Store, f, s.Version); err != nil {
		http.Error(w, "Could not export backup", 500)
		return
	}
	if _, err = f.Seek(0, 0); err != nil {
		http.Error(w, "Could not read backup", 500)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="my-code-dashboard.backup.tar.gz"`)
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeContent(w, r, "backup.tar.gz", time.Now(), f)
}
func (s *Server) service(w http.ResponseWriter, r *http.Request) {
	if s.Engine.Demo {
		s.redirect(w, r, "/settings", "error", "Service installation is disabled in demo mode.")
		return
	}
	settings, err := s.Engine.Store.Settings()
	if err != nil {
		http.Error(w, "Could not load settings", 500)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	switch r.PathValue("action") {
	case "install":
		err = platform.InstallService(ctx, s.DataDir, s.Addr, settings.InstallationID)
	case "remove":
		err = platform.RemoveService(ctx, settings.InstallationID)
	default:
		err = errors.New("unknown service action")
	}
	if err != nil {
		s.redirect(w, r, "/settings", "error", err.Error())
		return
	}
	s.redirect(w, r, "/settings", "notice", "Background service setting updated. An installed agent takes over when the foreground process exits.")
}

func (v *View) addFindings(findings []model.Finding, lookup map[string]model.Repository) {
	for _, f := range findings {
		repo, ok := lookup[f.RepositoryID]
		if !ok || (!repo.Selected && v.Repo != repo.ID) {
			continue
		}
		if repo.Selected && f.State == "open" {
			v.Open++
			switch f.Severity {
			case "critical":
				v.Critical++
			case "high":
				v.High++
			case "medium":
				v.Medium++
			case "unknown":
				v.Unknown++
			}
		}
		if (v.Repo != "" && f.RepositoryID != v.Repo) || (v.Provider != "" && repo.Provider != v.Provider) || (v.Category != "" && f.Category != v.Category) || (v.Severity != "" && f.Severity != v.Severity) || (v.State != "" && v.State != "all" && f.State != v.State) || (v.NativeState != "" && f.NativeState != v.NativeState) || (v.Namespace != "" && repo.Namespace != v.Namespace) {
			continue
		}
		if v.Query != "" && !strings.Contains(strings.ToLower(f.Title+" "+repo.Name+" "+f.Package+" "+strings.Join(f.Identifiers, " ")), strings.ToLower(v.Query)) {
			continue
		}
		v.Findings = append(v.Findings, FindingRow{Finding: f, RepositoryName: repo.Name, Provider: repo.Provider})
	}
}

func (v *View) paginateFindings(q url.Values) {
	v.FindingCount = len(v.Findings)
	v.PageNumber, _ = strconv.Atoi(q.Get("page"))
	if v.PageNumber < 1 {
		v.PageNumber = 1
	}
	v.PageCount = max(1, (len(v.Findings)+49)/50)
	if v.PageNumber > v.PageCount {
		v.PageNumber = v.PageCount
	}
	start := (v.PageNumber - 1) * 50
	end := min(start+50, len(v.Findings))
	v.Findings = v.Findings[start:end]
	if v.PageNumber > 1 {
		q.Set("page", strconv.Itoa(v.PageNumber-1))
		v.PreviousURL = "/findings?" + q.Encode()
	}
	if v.PageNumber < v.PageCount {
		q.Set("page", strconv.Itoa(v.PageNumber+1))
		v.NextURL = "/findings?" + q.Encode()
	}
}
