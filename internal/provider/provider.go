// SPDX-License-Identifier: GPL-3.0-only
package provider

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/model"
)

type Identity struct{ Name, Version string }
type Provider interface {
	Validate(context.Context) (Identity, error)
	Discover(context.Context) ([]model.Repository, error)
	RefreshRepository(context.Context, model.Repository) (model.Repository, error)
	Collect(context.Context, model.Repository) []model.Snapshot
}

type Error struct {
	Kind, Message string
	RetryAt       time.Time
	// Only an allowlisted identifier is retained, never arbitrary error content.
	code string
}

func (e *Error) Error() string { return e.Message }
func Failure(err error) (string, string) {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind, e.Message
	}
	return "error", "Collection failed. Previous complete data has been retained."
}

func ValidateBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("use an HTTPS provider URL without credentials, a query, or a fragment")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func New(c model.Connection, token string) (Provider, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("credential is missing or the keychain is locked")
	}
	base, err := ValidateBase(c.BaseURL)
	if err != nil {
		return nil, err
	}
	switch c.Provider {
	case "github":
		if base != "https://github.com" {
			return nil, errors.New("GitHub connections use https://github.com")
		}
		return &github{api: newAPI("https://api.github.com", token), browser: base}, nil
	case "gitlab":
		return &gitlab{api: newAPI(base+"/api/v4", token), base: base}, nil
	default:
		return nil, errors.New("unsupported provider")
	}
}

type api struct {
	base, token string
	client      *http.Client
	mu          sync.Mutex
	retryAt     time.Time
}

func newAPI(base, token string) *api {
	u, _ := url.Parse(base)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != u.Scheme || !strings.EqualFold(req.URL.Host, u.Host) || req.URL.User != nil {
			return errors.New("cross-origin redirect rejected")
		}
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}}
	return &api{base: strings.TrimSuffix(base, "/"), token: token, client: client}
}

func sameOrigin(a, b *url.URL) bool { return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host) }

func connectionFailure(err error) *Error {
	message := "Cannot reach provider. Check network access and TLS trust."
	var certificateError *tls.CertificateVerificationError
	var networkError net.Error
	switch {
	case errors.As(err, &certificateError):
		message = "Provider TLS certificate verification failed. Check the HTTPS hostname, certificate validity, and system trust store or corporate CA."
	case errors.As(err, &networkError) && networkError.Timeout():
		message = "Provider connection timed out. Check network access and TLS compatibility; rebuild with the project's pinned Go toolchain."
	}
	// Never expose transport errors: they may contain URLs or private certificate details.
	return &Error{Kind: "error", Message: message}
}

func (a *api) request(ctx context.Context, method, path string, body any, out any) (http.Header, error) {
	a.mu.Lock()
	retryAt := a.retryAt
	a.mu.Unlock()
	if retryAt.After(time.Now()) {
		return nil, &Error{Kind: "rate_limited", Message: "Provider rate limit reached; collection will retry later.", RetryAt: retryAt}
	}
	base, _ := url.Parse(a.base + "/")
	u, err := url.Parse(path)
	if err != nil {
		return nil, errors.New("invalid provider path")
	}
	if !u.IsAbs() {
		u = base.ResolveReference(u)
	}
	if !sameOrigin(u, base) || u.User != nil {
		return nil, &Error{Kind: "error", Message: "Provider returned an unsafe pagination or redirect URL."}
	}
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+a.token)
		req.Header.Set("User-Agent", "my-code-dashboard/0.1")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := a.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt < 2 {
				continue
			}
			return nil, connectionFailure(err)
		}
		if resp.StatusCode >= 500 && attempt < 2 {
			resp.Body.Close()
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			failure := &Error{Kind: "error", Message: fmt.Sprintf("Provider returned HTTP %d; previous data was retained.", resp.StatusCode)}
			switch resp.StatusCode {
			case 401:
				failure.Kind = "denied"
				failure.Message = "Credential expired or was rejected. Replace it in Connections."
			case 403:
				failure.Kind = "denied"
				failure.Message = "Access denied. Check token permissions, project roles, and organization approval."
			case 404:
				failure.Kind = "unavailable"
				failure.Message = "Unavailable: repository, feature, or permission could not be confirmed."
			case 429:
				failure.Kind = "rate_limited"
			}
			if resp.StatusCode == 403 || resp.StatusCode == 404 {
				failure.code = knownCoverageError(resp.Body)
			}
			resp.Body.Close()
			if resp.StatusCode == 429 || (resp.StatusCode == 403 && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "")) {
				failure.Kind = "rate_limited"
				failure.Message = "Provider rate limit reached; collection will retry later."
				failure.RetryAt = time.Now().Add(time.Minute)
				if sec, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && sec > 0 {
					failure.RetryAt = time.Now().Add(time.Duration(sec) * time.Second)
				} else if date, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil {
					failure.RetryAt = date
				} else if sec, e := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); e == nil && sec > 0 {
					failure.RetryAt = time.Unix(sec, 0)
				}
				a.mu.Lock()
				a.retryAt = failure.RetryAt
				a.mu.Unlock()
			}
			return resp.Header, failure
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20+1))
		resp.Body.Close()
		if readErr != nil {
			return nil, &Error{Kind: "error", Message: "Provider response was interrupted."}
		}
		if len(data) > 16<<20 {
			return nil, &Error{Kind: "error", Message: "Provider response exceeded the safe size limit."}
		}
		if out != nil && json.Unmarshal(data, out) != nil {
			return nil, &Error{Kind: "error", Message: "Provider response was not valid expected JSON."}
		}
		return resp.Header, nil
	}
	return nil, errors.New("request attempts exhausted")
}

func nextLink(h http.Header) string {
	for _, header := range h.Values("Link") {
		for _, part := range linkParts(header, ',') {
			target, params, ok := strings.Cut(strings.TrimSpace(part), ">")
			if !ok || !strings.HasPrefix(target, "<") {
				continue
			}
			for _, param := range linkParts(params, ';') {
				name, value, ok := strings.Cut(param, "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(name), "rel") {
					continue
				}
				value = strings.TrimSpace(value)
				if strings.HasPrefix(value, `"`) {
					if len(value) < 2 || value[len(value)-1] != '"' {
						break
					}
					var unquoted strings.Builder
					quoted := value[1 : len(value)-1]
					for i := 0; i < len(quoted); i++ {
						if quoted[i] == '\\' && i+1 < len(quoted) {
							i++
						}
						unquoted.WriteByte(quoted[i])
					}
					value = unquoted.String()
				}
				for _, relation := range strings.Fields(value) {
					if strings.EqualFold(relation, "next") {
						return strings.TrimPrefix(target, "<")
					}
				}
				break // RFC 8288: only the first rel parameter has meaning.
			}
		}
	}
	return ""
}

// Link delimiters only separate values outside URI references and quoted strings.
func linkParts(value string, separator byte) []string {
	var parts []string
	start := 0
	inURI, quoted, escaped := false, false, false
	for i := 0; i < len(value); i++ {
		switch {
		case escaped:
			escaped = false
		case quoted && value[i] == '\\':
			escaped = true
		case !inURI && value[i] == '"':
			quoted = !quoted
		case !quoted && value[i] == '<':
			inURI = true
		case !quoted && value[i] == '>':
			inURI = false
		case !inURI && !quoted && value[i] == separator:
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	return append(parts, value[start:])
}

func pages[T any](ctx context.Context, a *api, path string) ([]T, error) {
	all := []T{}
	seen := map[string]bool{}
	for path != "" {
		if seen[path] || len(seen) >= 1000 {
			return nil, &Error{Kind: "error", Message: "Pagination did not complete within safe limits."}
		}
		seen[path] = true
		var page []T
		h, err := a.request(ctx, http.MethodGet, path, nil, &page)
		if err != nil {
			return nil, err
		}
		if page == nil {
			return nil, &Error{Kind: "error", Message: "Provider returned a null collection instead of an alert list."}
		}
		all = append(all, page...)
		if len(all) > 100000 {
			return nil, &Error{Kind: "error", Message: "Collection exceeded 100,000 records in one category."}
		}
		path = nextLink(h)
	}
	return all, nil
}

func failed(category string, err error) model.Snapshot {
	kind, message := Failure(err)
	s := model.Snapshot{Coverage: model.Coverage{Category: category, Collection: kind, ScannerState: "unknown", Note: message}}
	switch kind {
	case "disabled":
		s.Coverage.ScannerState = "disabled"
	case "no_analysis":
		s.Coverage.ScannerState = "not_observed"
	}
	var e *Error
	if errors.As(err, &e) {
		s.Coverage.RetryAt = e.RetryAt
	}
	return s
}

func knownCoverageError(body io.Reader) string {
	const limit = 8 << 10
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || len(data) > limit {
		return ""
	}
	var response struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &response) != nil {
		return ""
	}
	// Exact known messages only. A generic 403/404 cannot establish enablement.
	switch strings.ToLower(strings.TrimSpace(response.Message)) {
	case "code scanning is not enabled for this repository. please enable code scanning in the repository settings.", "code scanning is not enabled for this repository.":
		return "github_code_disabled"
	case "secret scanning is disabled on this repository.":
		return "github_secrets_disabled"
	case "no analysis found":
		return "github_no_analysis"
	default:
		return ""
	}
}
func unavailable(category, note string) model.Snapshot {
	return model.Snapshot{Coverage: model.Coverage{Category: category, Collection: "unsupported", ScannerState: "unknown", Note: note}}
}

func SafeURL(raw, base string) string {
	u, err := url.Parse(raw)
	b, e := url.Parse(base)
	if err != nil || e != nil || u.Scheme != "https" || !sameOrigin(u, b) || u.User != nil {
		return ""
	}
	return u.String()
}
