// SPDX-License-Identifier: GPL-3.0-only
package model

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

var Categories = []string{"dependencies", "code", "secrets", "containers", "iac", "dast", "other"}
var Severities = []string{"critical", "high", "medium", "low", "info", "unknown"}

type Connection struct {
	ID                string    `json:"id"`
	Provider          string    `json:"provider"`
	Name              string    `json:"name"`
	BaseURL           string    `json:"base_url"`
	Identity          string    `json:"identity"`
	Version           string    `json:"version"`
	Status            string    `json:"status"`
	ReconnectRequired bool      `json:"reconnect_required,omitempty"`
	Message           string    `json:"message"`
	CreatedAt         time.Time `json:"created_at"`
}

type Repository struct {
	ID            string `json:"id"`
	ConnectionID  string `json:"connection_id"`
	Provider      string `json:"provider"`
	ExternalID    string `json:"external_id"`
	Name          string `json:"name"`
	Namespace     string `json:"namespace"`
	URL           string `json:"url"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	Selected      bool   `json:"selected"`
}

type Finding struct {
	ID                 string    `json:"id"`
	RepositoryID       string    `json:"repository_id"`
	Source             string    `json:"source"`
	Category           string    `json:"category"`
	Title              string    `json:"title"`
	Severity           string    `json:"severity"`
	NativeSeverity     string    `json:"native_severity"`
	State              string    `json:"state"`
	NativeState        string    `json:"native_state"`
	NativeResolution   string    `json:"native_resolution,omitempty"`
	DiagnosticSeverity string    `json:"diagnostic_severity,omitempty"`
	URL                string    `json:"url"`
	Package            string    `json:"package"`
	Identifiers        []string  `json:"identifiers"`
	Scanner            string    `json:"scanner"`
	Scope              string    `json:"scope"`
	Commit             string    `json:"commit"`
	SeenAt             time.Time `json:"seen_at"`
	Missing            bool      `json:"missing"`
}

type Coverage struct {
	RepositoryID string    `json:"repository_id"`
	Category     string    `json:"category"`
	Collection   string    `json:"collection"`
	ScannerState string    `json:"scanner_state"`
	Note         string    `json:"note"`
	LastAttempt  time.Time `json:"last_attempt"`
	LastSuccess  time.Time `json:"last_success"`
	AnalysisAt   time.Time `json:"analysis_at"`
	Ref          string    `json:"ref"`
	Commit       string    `json:"commit"`
	Count        int       `json:"count"`
	RetryAt      time.Time `json:"retry_at,omitzero"`
}

type Snapshot struct {
	Coverage Coverage
	Findings []Finding
	Complete bool
}

type Run struct {
	ID              string              `json:"id"`
	Trigger         string              `json:"trigger"`
	StartedAt       time.Time           `json:"started_at"`
	FinishedAt      time.Time           `json:"finished_at"`
	Status          string              `json:"status"`
	Message         string              `json:"message"`
	Succeeded       int                 `json:"succeeded"`
	Failed          int                 `json:"failed"`
	Disabled        int                 `json:"disabled"`
	NoAnalysis      int                 `json:"no_analysis"`
	Unsupported     int                 `json:"unsupported"`
	DetailsRecorded bool                `json:"details_recorded,omitempty"`
	Outcomes        []CollectionOutcome `json:"outcomes,omitempty"`
}

// CollectionOutcome records what happened during this refresh, independently of
// later changes to repository names, permissions, or coverage.
type CollectionOutcome struct {
	RepositoryID   string `json:"repository_id"`
	RepositoryName string `json:"repository_name"`
	Provider       string `json:"provider"`
	Category       string `json:"category"`
	Collection     string `json:"collection"`
	ScannerState   string `json:"scanner_state"`
	Reason         string `json:"reason"`
}

func (r Run) Attention() []CollectionOutcome {
	var outcomes []CollectionOutcome
	for _, outcome := range r.Outcomes {
		if outcome.Collection != "success" && outcome.Collection != "unsupported" || outcome.ScannerState == "warning" || outcome.ScannerState == "disabled" || outcome.ScannerState == "enabled" {
			outcomes = append(outcomes, outcome)
		}
	}
	return outcomes
}

type Change struct {
	ID           int64     `json:"id"`
	RepositoryID string    `json:"repository_id"`
	FindingID    string    `json:"finding_id"`
	Title        string    `json:"title"`
	Kind         string    `json:"kind"`
	Before       string    `json:"before"`
	After        string    `json:"after"`
	At           time.Time `json:"at"`
}

type Settings struct {
	Hour                    int       `json:"hour"`
	Minute                  int       `json:"minute"`
	Timezone                string    `json:"timezone"`
	Enabled                 bool      `json:"enabled"`
	LastSlot                time.Time `json:"last_slot"`
	Attempts                int       `json:"attempts"`
	RetryAt                 time.Time `json:"retry_at"`
	ResumeAfterRepositoryID string    `json:"resume_after_repository_id,omitempty"`
	ChangesDays             int       `json:"changes_days"`
	LogsDays                int       `json:"logs_days"`
	InstallationID          string    `json:"installation_id"`
}

func DefaultSettings() Settings {
	return Settings{Hour: 9, Timezone: "Europe/Zurich", Enabled: true, ChangesDays: 90, LogsDays: 30, InstallationID: NewID()}
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func State(native string) string {
	switch strings.ToLower(native) {
	case "open", "detected", "confirmed":
		return "open"
	case "fixed", "resolved":
		return "resolved"
	case "dismissed", "auto_dismissed":
		return "dismissed"
	default:
		return "unknown"
	}
}

func Severity(native string) string {
	s := strings.ToLower(native)
	if s == "moderate" {
		s = "medium"
	}
	if s == "informational" {
		s = "info"
	}
	for _, v := range Severities {
		if s == v {
			return s
		}
	}
	return "unknown"
}

// LatestSlot uses calendar arithmetic so a DST transition never moves 09:00.
func LatestSlot(now time.Time, s Settings) (time.Time, error) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	n := now.In(loc)
	date := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	for {
		slot := calendarSlot(date, s.Hour, s.Minute, loc)
		if !slot.After(now) {
			return slot.UTC(), nil
		}
		// Reconstruct the configured clock time on the preceding calendar date;
		// subtracting from a normalized gap time would preserve the wrong hour.
		date = date.AddDate(0, 0, -1)
	}
}

// calendarSlot uses the earlier occurrence of a repeated clock time and shifts
// a skipped time forward by the offset change (02:30 becomes 03:30 for a 1h gap).
func calendarSlot(date time.Time, hour, minute int, loc *time.Location) time.Time {
	wall := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, time.UTC)
	guess := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, loc)
	var best time.Time
	var exact bool
	for _, probe := range []time.Time{guess.Add(-24 * time.Hour), guess, guess.Add(24 * time.Hour)} {
		_, offset := probe.Zone()
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(loc)
		clock := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, time.UTC)
		if clock.Before(wall) {
			continue
		}
		matches := clock.Equal(wall)
		if best.IsZero() || (matches && !exact) || (matches == exact && candidate.Before(best)) {
			best, exact = candidate, matches
		}
	}
	return best
}
