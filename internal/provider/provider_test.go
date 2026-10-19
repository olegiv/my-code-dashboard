// SPDX-License-Identifier: GPL-3.0-only
package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/model"
)

func jsonReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func mockAPI(t *testing.T, h http.HandlerFunc) *api {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	return newAPI(server.URL, "synthetic-token-not-real")
}

func TestPaginationLinkDelimiters(t *testing.T) {
	const target = "https://api.example/alerts?state=open,fixed&cursor=a;b&page=2"
	for _, tc := range []struct {
		name  string
		links []string
		want  string
	}{
		{"URI delimiters", []string{"<" + target + `>; rel="next"`}, target},
		{"encoded URI", []string{`<https://api.example/alerts?state=open%2Cfixed&page=2>; rel="next"`}, "https://api.example/alerts?state=open%2Cfixed&page=2"},
		{"no separator space", []string{`<https://api.example/alerts?page=1>; rel="prev",<` + target + `>; rel="next"`}, target},
		{"quoted attributes", []string{`<https://api.example/alerts?page=1>; title="literal , ; < > \" text"; rel="prev",` + "\t<" + target + `>; title="commas, semicolons; brackets<>"; rel="next"`}, target},
		{"relation list", []string{"<" + target + `>; rel="alternate next"`}, target},
		{"unquoted relation", []string{"<" + target + ">; rel=next"}, target},
		{"quoted pair", []string{"<" + target + `>; rel="\next"`}, target},
		{"first relation wins", []string{"<" + target + `>; rel="prev"; rel="next"`}, ""},
		{"header fields", []string{`<https://api.example/alerts?page=1>; rel="prev"`, "<" + target + `>; rel="next"`}, target},
		{"ordinary last page", []string{`<https://api.example/alerts?page=1>; rel="prev"`}, ""},
		{"no header", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{"Link": tc.links}
			if got := nextLink(h); got != tc.want {
				t.Fatalf("next Link = %q, want %q", got, tc.want)
			}
		})
	}
	for _, state := range []string{"open,fixed,dismissed", "open%2Cfixed%2Cdismissed"} {
		t.Run("two pages "+state, func(t *testing.T) {
			calls := 0
			var a *api
			a = mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("state") != "open,fixed,dismissed" {
					t.Error("pagination changed the state filter")
				}
				if r.URL.Query().Get("page") == "2" {
					jsonReply(w, []int{101})
					return
				}
				w.Header().Set("Link", "<"+a.base+"/alerts?state="+state+`&page=2>; rel="next"`)
				page := make([]int, 100)
				for i := range page {
					page[i] = i + 1
				}
				jsonReply(w, page)
			})
			items, err := pages[int](t.Context(), a, "/alerts?state="+state)
			if err != nil || calls != 2 || len(items) != 101 || items[100] != 101 {
				t.Fatalf("incomplete pagination: calls=%d items=%d err=%v", calls, len(items), err)
			}
		})
	}
}

func TestRepositoryMetadataPreservesImmutableIdentity(t *testing.T) {
	for _, kind := range []string{"github", "gitlab"} {
		t.Run(kind, func(t *testing.T) {
			pathKey := "full_name"
			if kind == "gitlab" {
				pathKey = "path_with_namespace"
			}
			cases := []struct {
				name, body string
				valid      bool
			}{
				{"null", `null`, false},
				{"empty", `{}`, false},
				{"missing id", fmt.Sprintf(`{%q:"example/renamed"}`, pathKey), false},
				{"negative id", fmt.Sprintf(`{"id":-42,%q:"example/renamed"}`, pathKey), false},
				{"different id", fmt.Sprintf(`{"id":43,%q:"example/renamed"}`, pathKey), false},
				{"missing path", `{"id":42}`, false},
				{"blank path", fmt.Sprintf(`{"id":42,%q:"  "}`, pathKey), false},
				{"renamed", fmt.Sprintf(`{"id":42,%q:"example/renamed","default_branch":"trunk","archived":true}`, pathKey), true},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
					var p Provider = &github{api: a, browser: "https://github.com"}
					if kind == "gitlab" {
						p = &gitlab{api: a, base: "https://gitlab.example"}
					}
					before := model.Repository{ID: "synthetic-id", ConnectionID: "fixture", ExternalID: "42", Provider: kind, Name: "example/original", DefaultBranch: "main", Selected: true}
					fresh, err := p.RefreshRepository(t.Context(), before)
					if !tc.valid {
						if err == nil || fresh != before {
							t.Fatalf("incomplete or changed identity replaced metadata: %+v %v", fresh, err)
						}
						// Discovery also rejects incomplete items without returning a
						// partially importable list. A different positive ID is valid there.
						if tc.name != "different id" {
							listAPI := mockAPI(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `[%s]`, tc.body) })
							if kind == "github" {
								p = &github{api: listAPI}
							} else {
								p = &gitlab{api: listAPI}
							}
							if repos, discoveryErr := p.Discover(t.Context()); discoveryErr == nil || len(repos) != 0 {
								t.Fatal("incomplete discovery item was accepted")
							}
						}
						return
					}
					if err != nil || fresh.ID != before.ID || fresh.ExternalID != before.ExternalID || fresh.ConnectionID != before.ConnectionID || fresh.Provider != kind || !fresh.Selected || fresh.Name != "example/renamed" || fresh.DefaultBranch != "trunk" || !fresh.Archived {
						t.Fatalf("valid rename lost identity or metadata: %+v %v", fresh, err)
					}
					listAPI := mockAPI(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `[%s]`, tc.body) })
					if kind == "github" {
						p = &github{api: listAPI}
					} else {
						p = &gitlab{api: listAPI}
					}
					if repos, discoveryErr := p.Discover(t.Context()); discoveryErr != nil || len(repos) != 1 || repos[0].ExternalID != "42" || repos[0].Name != fresh.Name {
						t.Fatal("valid discovery metadata was rejected")
					}
				})
			}
		})
	}
}

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

func TestTransportFailuresAreActionableAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"timeout", &net.DNSError{Err: "synthetic-private-detail", Name: "synthetic-private.example", IsTimeout: true}, "timed out"},
		{"certificate", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{Cert: &x509.Certificate{DNSNames: []string{"synthetic-private.example"}}}}, "certificate verification failed"},
		{"other", errors.New("synthetic-private-detail"), "Cannot reach provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newAPI("https://provider.example", "synthetic-token-not-real")
			a.client.Transport = failingTransport{err: &url.Error{Op: "Get", URL: "https://synthetic-private.example/?token=synthetic-secret", Err: tc.err}}
			_, err := a.request(context.Background(), http.MethodGet, "user", nil, nil)
			kind, message := Failure(err)
			if kind != "error" || !strings.Contains(message, tc.want) {
				t.Fatalf("unexpected failure: %s %s", kind, message)
			}
			for _, private := range []string{"synthetic-private", "synthetic-secret", "synthetic-token"} {
				if strings.Contains(message, private) || strings.Contains(err.Error(), private) {
					t.Fatal("transport failure exposed private details")
				}
			}
		})
	}
}

func TestReadOnlyPaginationAndCredentialIsolation(t *testing.T) {
	var calls atomic.Int32
	a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic-token-not-real" {
			t.Error("unexpected authentication or write")
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"id":2}]`)
			return
		}
		w.Header().Set("Link", `</items?page=2>; rel="next"`)
		fmt.Fprint(w, `[{"id":1}]`)
	})
	values, err := pages[struct{ ID int }](context.Background(), a, "items")
	if err != nil || len(values) != 2 || values[1].ID != 2 {
		t.Fatalf("pagination: %+v %v", values, err)
	}
	if _, err = pages[int](context.Background(), a, "https://unrelated.example/items"); err == nil || calls.Load() != 2 {
		t.Fatal("pagination sent credential to another origin")
	}
	var leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer sink.Close()
	source := mockAPI(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sink.URL, http.StatusFound) })
	_, err = source.request(context.Background(), "GET", "redirect", nil, nil)
	if err == nil || leaked.Load() != 0 || strings.Contains(err.Error(), "synthetic-token") {
		t.Fatal("unsafe redirect or credential leak")
	}
}
func TestErrorsNullResponsesRateLimitAndRetries(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   string
	}{{401, "denied"}, {403, "denied"}, {404, "unavailable"}, {429, "rate_limited"}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			var calls atomic.Int32
			a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.status == 429 {
					w.Header().Set("Retry-After", "3600")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, "synthetic-private-provider-body")
			})
			_, err := a.request(context.Background(), "GET", "items", nil, nil)
			kind, _ := Failure(err)
			if kind != tc.kind || strings.Contains(err.Error(), "private-provider-body") {
				t.Fatalf("%v", err)
			}
			if tc.status == 429 {
				var e *Error
				if !errors.As(err, &e) || e.RetryAt.Before(time.Now().Add(59*time.Minute)) {
					t.Fatal("retry deadline missing")
				}
				_, _ = a.request(context.Background(), "GET", "again", nil, nil)
				if calls.Load() != 1 {
					t.Fatal("rate limit did not suppress further calls")
				}
			}
		})
	}
	a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "null") })
	if _, err := pages[int](context.Background(), a, "null"); err == nil {
		t.Fatal("null collection was treated as zero alerts")
	}
	repeated := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `</loop>; rel="next"`)
		fmt.Fprint(w, "[]")
	})
	if _, err := pages[int](context.Background(), repeated, "loop"); err == nil {
		t.Fatal("pagination loop accepted")
	}
	var calls atomic.Int32
	retry := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(502)
			return
		}
		fmt.Fprint(w, "[]")
	})
	if _, err := pages[int](context.Background(), retry, "items"); err != nil || calls.Load() != 2 {
		t.Fatal("bounded retry failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pages[int](ctx, retry, "items"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestSecondaryRateLimitStopsFurtherRequests(t *testing.T) {
	for _, retryAfter := range []string{"93600", time.Now().Add(26 * time.Hour).UTC().Format(http.TimeFormat)} {
		t.Run(retryAfter, func(t *testing.T) {
			var calls atomic.Int32
			a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", retryAfter)
				w.Header().Set("X-RateLimit-Remaining", "4999")
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"message":"synthetic secondary rate limit"}`)
			})
			_, err := a.request(t.Context(), http.MethodGet, "first", nil, nil)
			var failure *Error
			if !errors.As(err, &failure) || failure.Kind != "rate_limited" || failure.RetryAt.Before(time.Now().Add(25*time.Hour)) {
				t.Fatalf("secondary rate limit lost its classification/deadline: %v", err)
			}
			_, err = a.request(t.Context(), http.MethodGet, "next-category", nil, nil)
			if !errors.As(err, &failure) || failure.Kind != "rate_limited" || calls.Load() != 1 {
				t.Fatal("secondary limit did not suppress subsequent provider calls")
			}
			g := &github{api: a, browser: "https://github.com"}
			var limited int
			for _, snapshot := range g.Collect(t.Context(), model.Repository{Name: "synthetic/project", DefaultBranch: "main"}) {
				if snapshot.Coverage.Collection == "rate_limited" {
					limited++
					if snapshot.Complete || snapshot.Coverage.RetryAt.Before(time.Now().Add(25*time.Hour)) {
						t.Fatal("category snapshot lost the secondary-limit deadline")
					}
				}
			}
			if limited != 3 || calls.Load() != 1 {
				t.Fatal("category collection continued after the secondary limit")
			}
		})
	}
}
func TestSafeBaseAndFindingURLs(t *testing.T) {
	for _, raw := range []string{"http://gitlab.example.com", "https://user:pass@gitlab.example.com", "https://gitlab.example.com/?token=bad", "https://gitlab.example.com/#fragment"} {
		if _, err := ValidateBase(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if got := SafeURL("javascript:alert(1)", "https://github.com"); got != "" {
		t.Fatal(got)
	}
	if got := SafeURL("https://github.com.evil.example/a", "https://github.com"); got != "" {
		t.Fatal(got)
	}
	if _, err := New(model.Connection{Provider: "github", BaseURL: "https://other.example"}, "test"); err == nil {
		t.Fatal("GitHub API host substitution allowed")
	}
}

type originFixtureTransport func(*http.Request) (*http.Response, error)

func (f originFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDefaultPortOrigins(t *testing.T) {
	for _, tc := range []struct {
		name, base, link string
		allowed          bool
	}{
		{"ordinary HTTPS", "https://gitlab.example", "https://gitlab.example/group/project", true},
		{"canonical port omitted", "https://gitlab.example:443", "https://gitlab.example/group/project", true},
		{"canonical port explicit", "https://gitlab.example", "https://gitlab.example:443/group/project", true},
		{"host case", "https://gitlab.example:443", "https://GITLAB.example/group/project", true},
		{"nondefault matching", "https://gitlab.example:8443", "https://gitlab.example:8443/group/project", true},
		{"IPv6 default port", "https://[::1]:443", "https://[::1]/group/project", true},
		{"foreign host", "https://gitlab.example:443", "https://unrelated.example/group/project", false},
		{"nondefault port added", "https://gitlab.example", "https://gitlab.example:8443/group/project", false},
		{"nondefault port removed", "https://gitlab.example:8443", "https://gitlab.example/group/project", false},
		{"HTTP downgrade", "https://gitlab.example:443", "http://gitlab.example:443/group/project", false},
		{"userinfo", "https://gitlab.example:443", "https://user@gitlab.example/group/project", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAPI(tc.base, "synthetic-token-not-real")
			var calls int
			a.client.Transport = originFixtureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if !tc.allowed || r.URL.String() != tc.link || r.Header.Get("Authorization") != "Bearer synthetic-token-not-real" {
					t.Error("request escaped the allowed origin or lost authentication")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`[]`)), Request: r}, nil
			})
			if got := SafeURL(tc.link, tc.base); (got != "") != tc.allowed || got != "" && got != tc.link {
				t.Errorf("SafeURL = %q, allowed = %v", got, tc.allowed)
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.link, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = a.client.CheckRedirect(req, nil); (err == nil) != tc.allowed {
				t.Errorf("redirect: %v, allowed = %v", err, tc.allowed)
			}
			_, err = a.request(t.Context(), http.MethodGet, tc.link, nil, nil)
			if (err == nil) != tc.allowed || tc.allowed && calls != 1 || !tc.allowed && calls != 0 {
				t.Fatalf("request: calls = %d, err = %v, allowed = %v", calls, err, tc.allowed)
			}
		})
	}
	t.Run("GitLab discovery redirect and pagination", func(t *testing.T) {
		const base = "https://gitlab.example:443"
		a := newAPI(base+"/api/v4", "synthetic-token-not-real")
		calls := 0
		a.client.Transport = originFixtureTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Scheme != "https" || r.URL.Hostname() != "gitlab.example" || r.Header.Get("Authorization") != "Bearer synthetic-token-not-real" {
				t.Error("unexpected provider recipient or missing authentication")
			}
			w := httptest.NewRecorder()
			switch {
			case r.URL.Port() == "443":
				w.Header().Set("Location", "https://gitlab.example/api/v4/projects")
				w.WriteHeader(http.StatusFound)
			case r.URL.Query().Get("page") == "2":
				fmt.Fprint(w, `[{"id":2,"path_with_namespace":"example/two","web_url":"https://gitlab.example/example/two"}]`)
			default:
				w.Header().Set("Link", `<https://gitlab.example/api/v4/projects?page=2>; rel="next"`)
				fmt.Fprint(w, `[{"id":1,"path_with_namespace":"example/one","web_url":"https://gitlab.example/example/one"}]`)
			}
			return w.Result(), nil
		})
		g := gitlab{api: a, base: base}
		repos, err := g.Discover(t.Context())
		if err != nil || calls != 3 || len(repos) != 2 || repos[0].URL != "https://gitlab.example/example/one" || repos[1].URL != "https://gitlab.example/example/two" {
			t.Fatalf("canonical GitLab discovery: repos=%+v calls=%d err=%v", repos, calls, err)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base, nil)
		if err != nil {
			t.Fatal(err)
		}
		if a.client.CheckRedirect(req, make([]*http.Request, 5)) == nil {
			t.Fatal("redirect limit was removed")
		}
	})
}
func TestGitHubSecretValuesAreNeverImported(t *testing.T) {
	var calls atomic.Int32
	a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("hide_secret") != "true" {
			t.Error("hide_secret not requested")
		}
		fmt.Fprint(w, `[{"number":17,"state":"resolved","resolution":"revoked","secret_type":"synthetic_pattern","secret_type_display_name":"Synthetic credential","secret":"SYNTHETIC_LITERAL_MUST_NOT_PERSIST","locations":[{"source":"PRIVATE_SOURCE_SNIPPET"}],"html_url":"https://github.com/example/repository/security/secret-scanning/17"}]`)
	})
	g := github{api: a, browser: "https://github.com"}
	s := g.secrets(context.Background(), "repos/example/repository/")
	raw, _ := json.Marshal(s)
	if !s.Complete || len(s.Findings) != 1 || s.Findings[0].NativeResolution != "revoked" || calls.Load() != 2 {
		t.Fatalf("secret streams: %+v (%d)", s, calls.Load())
	}
	if strings.Contains(string(raw), "SYNTHETIC_LITERAL") || strings.Contains(string(raw), "PRIVATE_SOURCE") {
		t.Fatal("secret value or snippet leaked")
	}
	g.api = mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("secret_type") != "" {
			w.WriteHeader(403)
			return
		}
		fmt.Fprint(w, `[{"number":17,"state":"open"}]`)
	})
	s = g.secrets(context.Background(), "repos/example/repository/")
	if s.Complete || s.Coverage.Collection != "denied" || len(s.Findings) != 0 {
		t.Fatal("partial secret stream committed as complete")
	}
}

func TestGitHubMySQLGenericSecretSelector(t *testing.T) {
	for _, hasAlert := range []bool{false, true} {
		t.Run(fmt.Sprint(hasAlert), func(t *testing.T) {
			want := 0
			if hasAlert {
				want = 1
			}
			a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				selectors := r.URL.Query().Get("secret_type")
				if selectors != "" && !slices.Contains(strings.Split(selectors, ","), "mysql_connection_url") {
					w.WriteHeader(http.StatusUnprocessableEntity)
					return
				}
				if selectors != "" && hasAlert {
					fmt.Fprint(w, `[{"number":19,"state":"open","secret_type":"mysql_connection_url","secret_type_display_name":"MySQL connection URL","secret":"SYNTHETIC_MYSQL_SECRET"}]`)
					return
				}
				fmt.Fprint(w, `[]`)
			})
			g := github{api: a, browser: "https://github.com"}
			snapshot := g.secrets(context.Background(), "repos/example/repository/")
			if !snapshot.Complete || len(snapshot.Findings) != want {
				t.Fatalf("MySQL stream: complete=%v findings=%d", snapshot.Complete, len(snapshot.Findings))
			}
			if hasAlert && (snapshot.Findings[0].State != "open" || snapshot.Findings[0].Title != "MySQL connection URL exposed") {
				t.Fatalf("active MySQL alert lost: %+v", snapshot.Findings)
			}
			raw, _ := json.Marshal(snapshot)
			if strings.Contains(string(raw), "SYNTHETIC_MYSQL_SECRET") {
				t.Fatal("literal MySQL credential imported")
			}
		})
	}
}

func TestGitHubCoverageGapsVersusCollectionFailures(t *testing.T) {
	for _, tc := range []struct {
		name, category, message, want string
		status                        int
		rateLimited                   bool
	}{
		{"code disabled", "code", "Code scanning is not enabled for this repository. Please enable code scanning in the repository settings.", "disabled", 403, false},
		{"code disabled 404", "code", "Code scanning is not enabled for this repository.", "disabled", 404, false},
		{"secrets disabled", "secrets", "Secret scanning is disabled on this repository.", "disabled", 404, false},
		{"missing analysis", "code", "no analysis found", "no_analysis", 404, false},
		{"permissions", "code", "Resource not accessible by personal access token", "denied", 403, false},
		{"ambiguous missing", "secrets", "Not Found", "unavailable", 404, false},
		{"expired credential", "code", "Code scanning is not enabled for this repository.", "denied", 401, false},
		{"rate limit wins", "code", "Code scanning is not enabled for this repository.", "rate_limited", 403, true},
		{"wrong category", "secrets", "no analysis found", "unavailable", 404, false},
		{"wrong dependency endpoint", "dependencies", "no analysis found", "unavailable", 404, false},
		{"untrusted message", "secrets", "Secret scanning is disabled on this repository. SYNTHETIC_PRIVATE_DETAIL", "unavailable", 404, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := github{api: mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("collection used a write method")
				}
				if tc.rateLimited {
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("Retry-After", "3600")
				}
				w.WriteHeader(tc.status)
				jsonReply(w, map[string]string{"message": tc.message, "secret": "SYNTHETIC_PRIVATE_DETAIL", "documentation_url": "https://private.example/secret"})
			}), browser: "https://github.com"}
			var snap model.Snapshot
			switch tc.category {
			case "code":
				snap = g.code(context.Background(), "repos/example/repository/", "main")
			case "secrets":
				snap = g.secrets(context.Background(), "repos/example/repository/")
			case "dependencies":
				snap = g.dependencies(context.Background(), "repos/example/repository/")
			}
			if snap.Complete || snap.Coverage.Collection != tc.want || len(snap.Findings) != 0 || !snap.Coverage.AnalysisAt.IsZero() {
				t.Fatalf("incorrect classification: %+v", snap)
			}
			if tc.want == "disabled" && snap.Coverage.ScannerState != "disabled" {
				t.Fatal("disabled scanner evidence was lost")
			}
			if tc.want == "no_analysis" && (snap.Coverage.ScannerState != "not_observed" || snap.Coverage.Ref != "refs/heads/main") {
				t.Fatal("missing analysis lost its scope or fabricated scanner evidence")
			}
			if tc.rateLimited && snap.Coverage.RetryAt.IsZero() {
				t.Fatal("rate limit was misclassified as a coverage gap")
			}
			data, _ := json.Marshal(snap)
			if strings.Contains(string(data), "SYNTHETIC_PRIVATE_DETAIL") || strings.Contains(string(data), "private.example") {
				t.Fatal("raw provider error content was retained")
			}
		})
	}
}

func TestCoverageErrorBodyIsBoundedAndDoesNotReclassifyOtherRequests(t *testing.T) {
	for _, body := range []string{
		`{"message":"Secret scanning is disabled on this repository.","secret":"` + strings.Repeat("x", 9<<10) + `"}`,
		`{"message":"Secret scanning is disabled on this repository.",`,
	} {
		g := github{api: mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
			fmt.Fprint(w, body)
		})}
		snap := g.secrets(context.Background(), "repos/example/repository/")
		if snap.Complete || snap.Coverage.Collection != "unavailable" {
			t.Fatal("invalid/oversized response established scanner coverage")
		}
	}
	a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"message":"Code scanning is not enabled for this repository."}`)
	})
	_, err := a.request(context.Background(), http.MethodGet, "projects/42", nil, nil)
	if kind, _ := Failure(err); kind != "denied" {
		t.Fatal("generic API error was reclassified as GitHub scanner coverage")
	}
	g := github{api: a}
	if _, err = g.RefreshRepository(context.Background(), model.Repository{ExternalID: "42"}); err == nil {
		t.Fatal("repository access error was suppressed")
	} else if kind, _ := Failure(err); kind != "denied" {
		t.Fatal("metadata permissions were reclassified as scanner coverage")
	}
}

func TestGitHubEmptySuccessfulReportIsNotMissingAnalysis(t *testing.T) {
	g := github{api: mockAPI(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) })}
	snap := g.code(context.Background(), "repos/example/repository/", "main")
	if !snap.Complete || len(snap.Findings) != 0 || snap.Coverage.ScannerState != "unknown" || !snap.Coverage.AnalysisAt.IsZero() {
		t.Fatalf("genuine empty report confused with unavailable analysis: %+v", snap)
	}
}
func TestGitHubLifecycleAnalysisEvidenceAndUnknownSeverity(t *testing.T) {
	g := github{browser: "https://github.com"}
	g.api = mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "dependabot"):
			if r.URL.Query().Get("state") != "open,fixed,dismissed,auto_dismissed" {
				t.Error("terminal states omitted")
			}
			fmt.Fprint(w, `[{"number":1,"state":"fixed","security_advisory":{"summary":"Synthetic advisory","severity":"moderate"}}]`)
		case strings.HasSuffix(r.URL.Path, "alerts"):
			fmt.Fprint(w, `[{"number":2,"state":"open","rule":{"description":"Synthetic rule","severity":"error","id":"SYNTHETIC-1"},"tool":{"name":"Example analyzer"}}]`)
		case strings.HasSuffix(r.URL.Path, "analyses"):
			if r.URL.Query().Get("ref") != "refs/heads/main" {
				t.Error("wrong analysis scope")
			}
			fmt.Fprint(w, `[{"created_at":"2026-10-18T08:00:00Z","ref":"refs/heads/main","commit_sha":"new","tool":{"name":"A"},"category":"go"},{"created_at":"2026-10-17T08:00:00Z","ref":"refs/heads/main","commit_sha":"old","tool":{"name":"B"},"category":"config"}]`)
		default:
			w.WriteHeader(403)
		}
	})
	d := g.dependencies(context.Background(), "repos/example/repository/")
	if !d.Complete || d.Findings[0].State != "resolved" || d.Findings[0].Severity != "medium" || !d.Coverage.AnalysisAt.IsZero() {
		t.Fatalf("dependency mapping %+v", d)
	}
	s := g.code(context.Background(), "repos/example/repository/", "main")
	if !s.Complete || s.Findings[0].Severity != "unknown" || s.Findings[0].DiagnosticSeverity != "error" || s.Coverage.Commit != "old" || s.Coverage.AnalysisAt.Format(time.RFC3339) != "2026-10-17T08:00:00Z" {
		t.Fatalf("analysis streams collapsed incorrectly: %+v", s)
	}
}

func TestGitHubLastSuccessfulAnalysisPerStream(t *testing.T) {
	const oldSuccess = `{"created_at":"2026-10-15T09:00:00Z","ref":"refs/heads/main","commit_sha":"old-success","tool":{"name":"A"},"category":"go"}`
	const newSuccess = `{"created_at":"2026-10-19T09:00:00Z","ref":"refs/heads/main","commit_sha":"new-success","tool":{"name":"B"},"category":"config"}`
	const failedUpload = `{"created_at":"2026-10-19T10:00:00Z","ref":"refs/heads/main","commit_sha":"failed-upload","error":"synthetic upload error","tool":{"name":"A"},"category":"go"}`
	const warningUpload = `{"created_at":"2026-10-19T10:00:00Z","ref":"refs/heads/main","commit_sha":"warning-success","warning":"synthetic warning","tool":{"name":"B"},"category":"config"}`
	slashTool := strings.ReplaceAll(strings.ReplaceAll(oldSuccess, `"name":"A"`, `"name":"A/B"`), `"category":"go"`, `"category":"C"`)
	slashCategory := strings.ReplaceAll(strings.ReplaceAll(newSuccess, `"name":"B"`, `"name":"A"`), `"category":"config"`, `"category":"B/C"`)
	slashRef := strings.ReplaceAll(strings.ReplaceAll(slashTool, `"category":"C"`, `"category":"C/refs"`), `"ref":"refs/heads/main"`, `"ref":"heads/main"`)
	slashRef = strings.ReplaceAll(strings.ReplaceAll(slashRef, "2026-10-15", "2026-10-19"), "old-success", "new-success")
	for _, tc := range []struct {
		name, analyses, date, commit, state string
	}{
		{"successful streams", oldSuccess + "," + newSuccess, "2026-10-15T09:00:00Z", "old-success", "observed"},
		{"failed upload retains stream success", newSuccess + "," + failedUpload + "," + oldSuccess, "2026-10-15T09:00:00Z", "old-success", "warning"},
		{"all latest uploads failed", failedUpload + "," + oldSuccess, "2026-10-15T09:00:00Z", "old-success", "warning"},
		{"stream without success", failedUpload + "," + newSuccess, "2026-10-19T09:00:00Z", "new-success", "warning"},
		{"no successful analysis", failedUpload, "", "", "warning"},
		{"successful warning", warningUpload, "2026-10-19T10:00:00Z", "warning-success", "warning"},
		{"slash tool and category stay separate", slashTool + "," + slashCategory, "2026-10-15T09:00:00Z", "old-success", "observed"},
		{"slash warning stays visible", strings.Replace(slashTool, `"commit_sha":"old-success"`, `"commit_sha":"old-success","warning":"synthetic warning"`, 1) + "," + slashCategory, "2026-10-15T09:00:00Z", "old-success", "warning"},
		{"slash category and ref stay separate", slashTool + "," + slashRef, "2026-10-15T09:00:00Z", "old-success", "observed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := github{browser: "https://github.com"}
			g.api = mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "analyses"):
					fmt.Fprint(w, "["+tc.analyses+"]")
				case strings.HasSuffix(r.URL.Path, "alerts"):
					fmt.Fprint(w, `[]`)
				default:
					w.WriteHeader(http.StatusForbidden)
				}
			})
			snap := g.code(t.Context(), "repos/example/repository/", "main")
			wantTime := time.Time{}
			if tc.date != "" {
				var err error
				wantTime, err = time.Parse(time.RFC3339, tc.date)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !snap.Complete || !snap.Coverage.AnalysisAt.Equal(wantTime) || snap.Coverage.Commit != tc.commit || snap.Coverage.ScannerState != tc.state {
				t.Fatalf("last successful stream evidence = %+v", snap.Coverage)
			}
			if tc.date != "" && snap.Coverage.Ref != "refs/heads/main" {
				t.Fatal("analysis ref omitted")
			}
			if strings.HasPrefix(tc.name, "slash") && !strings.Contains(snap.Coverage.Note, "2 tool/category analysis streams;") {
				t.Fatal("distinct streams merged", snap.Coverage.Note)
			}
		})
	}
}

const syntheticSchema = `{"data":{"vulnerability":{"fields":[{"name":"id"},{"name":"title"},{"name":"state"},{"name":"severity"},{"name":"reportType"},{"name":"scanner"}]},"project":{"fields":[{"name":"vulnerabilities","args":[{"name":"first"},{"name":"after"},{"name":"state"}]},{"name":"securityScanners"}]},"state":{"enumValues":[{"name":"DETECTED"},{"name":"CONFIRMED"},{"name":"RESOLVED"},{"name":"DISMISSED"}]},"scanners":{"fields":[{"name":"available"},{"name":"enabled"},{"name":"pipelineRun"}]},"scanner":{"fields":[{"name":"name"}]}}}`

func TestGitLabCapabilityPaginationRedactionAndCoverage(t *testing.T) {
	var queries atomic.Int32
	var g gitlab
	g.api = mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || strings.Contains(r.URL.Path, "vulnerabilities") {
			t.Error("not using read-only GraphQL")
		}
		var body struct {
			Query     string
			Variables map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.Contains(body.Query, "DashboardCapabilities"):
			fmt.Fprint(w, syntheticSchema)
		case strings.Contains(body.Query, "DashboardScanners"):
			fmt.Fprint(w, `{"data":{"project":{"securityScanners":{"available":["SAST","SECRET_DETECTION"],"enabled":["SAST"],"pipelineRun":["SAST"]}}}}`)
		default:
			queries.Add(1)
			if !strings.Contains(body.Query, "state:[DETECTED,CONFIRMED,RESOLVED,DISMISSED]") || strings.Contains(body.Query, "description") || strings.Contains(body.Query, "location") {
				t.Error("unsafe/incomplete query")
			}
			if body.Variables["cursor"] == nil {
				fmt.Fprint(w, `{"data":{"project":{"vulnerabilities":{"nodes":[{"id":"gid://gitlab/Vulnerability/1","title":"Synthetic source containing SYNTHETIC_SECRET_LITERAL","state":"DETECTED","severity":"HIGH","reportType":"SECRET_DETECTION"}],"pageInfo":{"hasNextPage":true,"endCursor":"synthetic-next"}}}}}`)
			} else {
				fmt.Fprint(w, `{"data":{"project":{"vulnerabilities":{"nodes":[{"id":"gid://gitlab/Vulnerability/2","title":"Synthetic dependency advisory","state":"DISMISSED","severity":"LOW","reportType":"DEPENDENCY_SCANNING","scanner":{"name":"Example scanner"}}],"pageInfo":{"hasNextPage":false}}}}}`)
			}
		}
	})
	g.base = g.api.base
	snapshots := g.Collect(context.Background(), model.Repository{Name: "example/project"})
	if queries.Load() != 2 || len(snapshots) != 7 {
		t.Fatal("pagination or categories missing")
	}
	byCategory := map[string]model.Snapshot{}
	for _, s := range snapshots {
		if !s.Complete || !s.Coverage.AnalysisAt.IsZero() {
			t.Fatal("fabricated analysis time")
		}
		byCategory[s.Coverage.Category] = s
	}
	if byCategory["code"].Coverage.ScannerState != "observed" || byCategory["secrets"].Coverage.ScannerState != "disabled" || byCategory["containers"].Coverage.ScannerState != "unavailable" {
		t.Fatalf("scanner metadata lost: %+v", byCategory)
	}
	if f := byCategory["dependencies"].Findings[0]; f.State != "dismissed" || f.Scanner != "Example scanner" {
		t.Fatal("native lifecycle/scanner not preserved")
	}
	raw, _ := json.Marshal(snapshots)
	if strings.Contains(string(raw), "SYNTHETIC_SECRET_LITERAL") {
		t.Fatal("secret title was persisted")
	}
}
func TestGitLabSASTScannerTypesPreserveCoverageAndFindings(t *testing.T) {
	for _, tc := range []struct{ scanner, category string }{
		{"SAST_ADVANCED", "code"}, {"SAST_IAC", "iac"}, {"SAST", "code"}, {"IAC_SCANNING", "iac"},
	} {
		for _, state := range []string{"observed", "enabled", "disabled", "unavailable"} {
			t.Run(tc.scanner+"/"+state, func(t *testing.T) {
				a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
					var body struct{ Query string }
					_ = json.NewDecoder(r.Body).Decode(&body)
					switch {
					case strings.Contains(body.Query, "DashboardCapabilities"):
						fmt.Fprint(w, syntheticSchema)
					case strings.Contains(body.Query, "DashboardScanners"):
						available, enabled, ran := []string{}, []string{}, []string{}
						if state != "unavailable" {
							available = append(available, tc.scanner)
						}
						if state == "enabled" || state == "observed" {
							enabled = append(enabled, tc.scanner)
						}
						if state == "observed" {
							ran = append(ran, tc.scanner)
						}
						jsonReply(w, map[string]any{"data": map[string]any{"project": map[string]any{"securityScanners": map[string]any{"available": available, "enabled": enabled, "pipelineRun": ran}}}})
					default:
						fmt.Fprintf(w, `{"data":{"project":{"vulnerabilities":{"nodes":[{"id":"gid://gitlab/Vulnerability/1","title":"Synthetic advisory","state":"DETECTED","severity":"HIGH","reportType":%q}],"pageInfo":{"hasNextPage":false}}}}}`, tc.scanner)
					}
				})
				g := gitlab{api: a, base: a.base}
				snapshots := g.Collect(t.Context(), model.Repository{Name: "example/project"})
				found := false
				for _, snap := range snapshots {
					if snap.Coverage.Category == "other" && len(snap.Findings) != 0 {
						t.Fatal("known SAST type fell through to other")
					}
					if snap.Coverage.Category != tc.category {
						continue
					}
					found = true
					wantState := state
					if tc.category == "iac" && state == "unavailable" {
						wantState = "unknown"
					}
					if !snap.Complete || snap.Coverage.ScannerState != wantState || !snap.Coverage.AnalysisAt.IsZero() || len(snap.Findings) != 1 {
						t.Fatalf("scanner coverage or findings lost: %+v", snap)
					}
					f := snap.Findings[0]
					if f.Category != tc.category || f.State != "open" || f.NativeState != "DETECTED" || f.NativeSeverity != "HIGH" || f.Scanner != tc.scanner {
						t.Fatal("native finding evidence was changed", f)
					}
					run := model.Run{Outcomes: []model.CollectionOutcome{{Collection: "success", ScannerState: snap.Coverage.ScannerState}}}
					if (len(run.Attention()) != 0) != (state == "enabled" || state == "disabled") {
						t.Fatal("enabled/disabled coverage gap lost downstream")
					}
				}
				if !found {
					t.Fatal("expected category was omitted")
				}
			})
		}
	}
}

func TestGitLabZeroInaccessibleErrorsAndSchemaGaps(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		complete       bool
	}{{"zero", `{"data":{"project":{"vulnerabilities":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`, true}, {"denied", `{"data":{"project":null}}`, false}, {"null-nodes", `{"data":{"project":{"vulnerabilities":{"nodes":null,"pageInfo":{"hasNextPage":false}}}}}`, false}, {"graphql-error", `{"data":{"project":null},"errors":[{"message":"SYNTHETIC_PRIVATE_ERROR"}]}`, false}} {
		t.Run(tc.name, func(t *testing.T) {
			a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct{ Query string }
				_ = json.NewDecoder(r.Body).Decode(&body)
				if strings.Contains(body.Query, "DashboardCapabilities") {
					fmt.Fprint(w, syntheticSchema)
				} else {
					fmt.Fprint(w, tc.response)
				}
			})
			g := gitlab{api: a, base: a.base}
			snaps := g.Collect(context.Background(), model.Repository{Name: "example/project"})
			for _, s := range snaps {
				if s.Complete != tc.complete || strings.Contains(s.Coverage.Note, "SYNTHETIC_PRIVATE_ERROR") {
					t.Fatalf("%+v", s)
				}
			}
		})
	}
	a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"vulnerability":null,"project":null}}`)
	})
	g := gitlab{api: a, base: a.base}
	s := g.Collect(context.Background(), model.Repository{Name: "example/project"})
	if s[0].Complete || s[0].Coverage.Collection != "denied" {
		t.Fatal("inaccessible report after a schema gap was treated as zero")
	}
}

func TestGitLabReportsWithoutIntrospection(t *testing.T) {
	for _, schemaResponse := range []string{
		`{"data":{"vulnerability":null,"project":null}}`,
		`{"data":null,"errors":[{"message":"Field '__type' doesn't exist on type 'Query'"}]}`,
	} {
		t.Run(schemaResponse, func(t *testing.T) {
			var pages atomic.Int32
			a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Query     string
					Variables map[string]any
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if r.Method != http.MethodPost || strings.Contains(body.Query, "mutation") {
					t.Error("fallback must remain read-only")
				}
				switch {
				case strings.Contains(body.Query, "DashboardCapabilities"):
					fmt.Fprint(w, schemaResponse)
				case strings.Contains(body.Query, "DashboardScanners"):
					fmt.Fprint(w, `{"data":{"project":{"securityScanners":{"available":["SAST","SECRET_DETECTION"],"enabled":["SAST"],"pipelineRun":["SAST"]}}}}`)
				default:
					pages.Add(1)
					if !strings.Contains(body.Query, "state:[DETECTED,CONFIRMED,RESOLVED,DISMISSED]") || !strings.Contains(body.Query, "first:100,after:$cursor") || !strings.Contains(body.Query, "nodes{id title state severity reportType}") || strings.Contains(body.Query, "description") || strings.Contains(body.Query, "location") || body.Variables["path"] != "example/project" {
						t.Error("fallback query does not verify a complete minimal report")
					}
					if body.Variables["cursor"] == nil {
						fmt.Fprint(w, `{"data":{"project":{"vulnerabilities":{"nodes":[{"id":"gid://gitlab/Vulnerability/1","title":"SYNTHETIC_SECRET_LITERAL","state":"DETECTED","severity":"HIGH","reportType":"SECRET_DETECTION"}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}`)
					} else {
						fmt.Fprint(w, `{"data":{"project":{"vulnerabilities":{"nodes":[{"id":"gid://gitlab/Vulnerability/2","title":"Synthetic dependency advisory","state":"DISMISSED","severity":"LOW","reportType":"DEPENDENCY_SCANNING"}],"pageInfo":{"hasNextPage":false}}}}}`)
					}
				}
			})
			g := gitlab{api: a, base: a.base}
			snaps := g.Collect(context.Background(), model.Repository{Name: "example/project"})
			if len(snaps) != 7 || pages.Load() != 2 {
				t.Fatal("fallback did not paginate the full report")
			}
			for _, snap := range snaps {
				if !snap.Complete || !snap.Coverage.AnalysisAt.IsZero() || !strings.Contains(snap.Coverage.Note, "verified by a successful read-only query") {
					t.Fatalf("unverified or fabricated coverage: %+v", snap)
				}
				if snap.Coverage.Category == "code" && snap.Coverage.ScannerState != "observed" || snap.Coverage.Category == "secrets" && snap.Coverage.ScannerState != "disabled" {
					t.Fatal("optional scanner evidence was lost")
				}
				if snap.Coverage.Category == "dependencies" && (len(snap.Findings) != 1 || snap.Findings[0].State != "dismissed" || snap.Findings[0].Scanner != "DEPENDENCY_SCANNING") {
					t.Fatal("fallback lost native lifecycle or scanner fallback")
				}
			}
			raw, _ := json.Marshal(snaps)
			if strings.Contains(string(raw), "SYNTHETIC_SECRET_LITERAL") {
				t.Fatal("fallback persisted secret report content")
			}
		})
	}
}

func TestGitLabFallbackRejectsUnavailableOrPartialReports(t *testing.T) {
	for _, tc := range []struct {
		name, report, collection string
		complete                 bool
	}{
		{"empty accessible report", `{"data":{"project":{"vulnerabilities":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`, "", true},
		{"denied report", `{"data":{"project":{"vulnerabilities":null}}}`, "denied", false},
		{"null nodes", `{"data":{"project":{"vulnerabilities":{"nodes":null,"pageInfo":{"hasNextPage":false}}}}}`, "unavailable", false},
		{"missing page info", `{"data":{"project":{"vulnerabilities":{"nodes":[]}}}}`, "unavailable", false},
		{"missing page flag", `{"data":{"project":{"vulnerabilities":{"nodes":[],"pageInfo":{}}}}}`, "unavailable", false},
		{"incomplete pagination", `{"data":{"project":{"vulnerabilities":{"nodes":[],"pageInfo":{"hasNextPage":true}}}}}`, "error", false},
		{"unsupported query", `{"data":null,"errors":[{"message":"SYNTHETIC_PRIVATE_ERROR"}]}`, "unavailable", false},
		{"partial response", `{"data":{"project":{"vulnerabilities":{"nodes":[],"pageInfo":{"hasNextPage":false}}}},"errors":[{"message":"SYNTHETIC_PRIVATE_ERROR"}]}`, "unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct{ Query string }
				_ = json.NewDecoder(r.Body).Decode(&body)
				switch {
				case strings.Contains(body.Query, "DashboardCapabilities"):
					fmt.Fprint(w, `{"data":{"project":null,"vulnerability":null}}`)
				case strings.Contains(body.Query, "DashboardScanners"):
					fmt.Fprint(w, `{"data":null,"errors":[{"message":"Unsupported scanner metadata"}]}`)
				default:
					fmt.Fprint(w, tc.report)
				}
			})
			g := gitlab{api: a, base: a.base}
			for _, snap := range g.Collect(context.Background(), model.Repository{Name: "example/project"}) {
				if snap.Complete != tc.complete || snap.Coverage.Collection != tc.collection || strings.Contains(snap.Coverage.Note, "SYNTHETIC_PRIVATE_ERROR") || len(snap.Findings) != 0 || !snap.Coverage.AnalysisAt.IsZero() {
					t.Fatalf("invalid fallback result: %+v", snap)
				}
				if tc.complete && snap.Coverage.ScannerState != "unknown" {
					t.Fatal("inaccessible scanner metadata fabricated scanner evidence")
				}
			}
		})
	}
}

func TestGitLabIntrospectionAuthenticationAndRateLimitsDoNotFallback(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(status) })
			g := gitlab{api: a, base: a.base}
			for _, snap := range g.Collect(context.Background(), model.Repository{Name: "example/project"}) {
				if snap.Complete || snap.Coverage.Collection == "unsupported" {
					t.Fatal("credential/rate limit failure became missing coverage")
				}
			}
			if calls.Load() != 1 {
				t.Fatal("fallback ignored authentication or rate limit failure")
			}
		})
	}
}

func TestGitLabOptionalScannerRateLimitPreservesReportAndDeadline(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable metadata", true: "rate limited metadata"}[limited], func(t *testing.T) {
			a := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct{ Query string }
				_ = json.NewDecoder(r.Body).Decode(&body)
				switch {
				case strings.Contains(body.Query, "DashboardCapabilities"):
					fmt.Fprint(w, syntheticSchema)
				case strings.Contains(body.Query, "DashboardScanners"):
					if limited {
						w.Header().Set("Retry-After", "93600")
						w.WriteHeader(http.StatusTooManyRequests)
					} else {
						fmt.Fprint(w, `{"data":null,"errors":[{"message":"Unsupported scanner metadata"}]}`)
					}
				default:
					fmt.Fprint(w, `{"data":{"project":{"vulnerabilities":{"nodes":[{"id":"gid://gitlab/Vulnerability/1","title":"Synthetic advisory","state":"DETECTED","severity":"HIGH","reportType":"SAST"}],"pageInfo":{"hasNextPage":false}}}}}`)
				}
			})
			g := gitlab{api: a, base: a.base}
			snapshots := g.Collect(context.Background(), model.Repository{Name: "example/project"})
			if len(snapshots) != len(model.Categories) {
				t.Fatal("optional metadata failure discarded report categories")
			}
			findings := 0
			for _, snap := range snapshots {
				if !snap.Complete || snap.Coverage.ScannerState != "unknown" || !snap.Coverage.AnalysisAt.IsZero() {
					t.Fatalf("metadata failure discarded complete report or fabricated evidence: %+v", snap)
				}
				if limited && (snap.Coverage.RetryAt.Before(time.Now().Add(25*time.Hour)) || !strings.Contains(snap.Coverage.Note, "rate limit")) || !limited && !snap.Coverage.RetryAt.IsZero() {
					t.Fatalf("optional metadata lost or fabricated retry deadline: %+v", snap.Coverage)
				}
				findings += len(snap.Findings)
			}
			if findings != 1 {
				t.Fatal("successful vulnerability import was lost")
			}
		})
	}
}
