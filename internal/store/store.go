// SPDX-License-Identifier: GPL-3.0-only
package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	Path string
}

const schema = `
CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY CHECK(id=1), data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS connections (id TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS repositories (id TEXT PRIMARY KEY, connection_id TEXT NOT NULL REFERENCES connections(id), external_id TEXT NOT NULL, selected INTEGER NOT NULL DEFAULT 0, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS repo_connection ON repositories(connection_id);
CREATE TABLE IF NOT EXISTS coverage (repository_id TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE, category TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY(repository_id,category));
CREATE TABLE IF NOT EXISTS findings (repository_id TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE, source TEXT NOT NULL, external_id TEXT NOT NULL, category TEXT NOT NULL, severity TEXT NOT NULL, state TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY(repository_id,source,external_id));
CREATE INDEX IF NOT EXISTS finding_filter ON findings(state,severity,category);
CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, started_at TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS changes (id INTEGER PRIMARY KEY AUTOINCREMENT, repository_id TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE, at TEXT NOT NULL, data TEXT NOT NULL);
PRAGMA user_version=1;`

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("database must not be a symbolic link")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	if _, err = db.Exec("PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL;"); err != nil {
		return fail(err)
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version > 1 {
		return fail(errors.New("database belongs to a newer application; restore a compatible backup"))
	}
	if _, err = db.Exec(schema); err != nil {
		return fail(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		return fail(err)
	}
	s := &Store{db: db, Path: path}
	settings := model.DefaultSettings()
	b, _ := json.Marshal(settings)
	if _, err = db.Exec("INSERT OR IGNORE INTO settings VALUES(1,?)", string(b)); err != nil {
		return fail(err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func encode(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func queryList[T any](db *sql.DB, query string, args ...any) ([]T, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []T{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v T
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *Store) Settings() (model.Settings, error) {
	var raw string
	err := s.db.QueryRow("SELECT data FROM settings WHERE id=1").Scan(&raw)
	var v model.Settings
	if err == nil {
		err = json.Unmarshal([]byte(raw), &v)
	}
	return v, err
}
func (s *Store) SaveSettings(v model.Settings) error {
	_, err := s.db.Exec("UPDATE settings SET data=? WHERE id=1", encode(v))
	return err
}
func (s *Store) Connections() ([]model.Connection, error) {
	return queryList[model.Connection](s.db, "SELECT data FROM connections ORDER BY rowid")
}
func (s *Store) SaveConnection(v model.Connection) error {
	_, err := s.db.Exec("INSERT INTO connections VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", v.ID, encode(v))
	return err
}
func (s *Store) Connection(id string) (model.Connection, error) {
	var raw string
	err := s.db.QueryRow("SELECT data FROM connections WHERE id=?", id).Scan(&raw)
	var c model.Connection
	if err == nil {
		err = json.Unmarshal([]byte(raw), &c)
	}
	return c, err
}
func (s *Store) Repositories() ([]model.Repository, error) {
	return queryList[model.Repository](s.db, "SELECT data FROM repositories ORDER BY lower(json_extract(data,'$.name'))")
}
func (s *Store) Repository(id string) (model.Repository, error) {
	var raw string
	err := s.db.QueryRow("SELECT data FROM repositories WHERE id=?", id).Scan(&raw)
	var r model.Repository
	if err == nil {
		err = json.Unmarshal([]byte(raw), &r)
	}
	return r, err
}

func (s *Store) SaveDiscovered(connection model.Connection, repos []model.Repository) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range repos {
		// Canonical IDs include the provider host; names and paths can change.
		r.ID = connection.Provider + ":" + strings.TrimSuffix(connection.BaseURL, "/") + ":" + r.ExternalID
		r.ConnectionID = connection.ID
		r.Provider = connection.Provider
		var raw string
		err = tx.QueryRow("SELECT data FROM repositories WHERE id=?", r.ID).Scan(&raw)
		if err == nil {
			var old model.Repository
			if err = json.Unmarshal([]byte(raw), &old); err != nil {
				return err
			}
			r.Selected = old.Selected
			if old.Selected {
				r.ConnectionID = old.ConnectionID
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.Exec("INSERT INTO repositories(id,connection_id,external_id,selected,data) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET connection_id=excluded.connection_id, selected=excluded.selected, data=excluded.data", r.ID, r.ConnectionID, r.ExternalID, r.Selected, encode(r))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SaveRepository(r model.Repository) error {
	// Selection belongs to the GUI and may change while a refresh is in flight.
	result, err := s.db.Exec("UPDATE repositories SET data=json_set(?, '$.selected',json(CASE selected WHEN 1 THEN 'true' ELSE 'false' END),'$.connection_id',connection_id) WHERE id=? AND external_id=?", encode(r), r.ID, r.ExternalID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("repository identity changed or is unavailable")
	}
	return nil
}

func (s *Store) SelectRepositories(ids map[string]bool) error {
	repos, err := s.Repositories()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range repos {
		// Read the latest metadata under the write transaction, then serialize
		// the complete record so duplicate/case-aliased JSON keys cannot undo
		// a saved selection. Refreshes may have updated metadata since the list.
		var raw string
		if err = tx.QueryRow("SELECT data FROM repositories WHERE id=?", r.ID).Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return err
		}
		r.Selected = ids[r.ID]
		if _, err = tx.Exec("UPDATE repositories SET selected=?,data=? WHERE id=?", r.Selected, encode(r), r.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Coverage() ([]model.Coverage, error) {
	return queryList[model.Coverage](s.db, "SELECT data FROM coverage ORDER BY repository_id,category")
}
func (s *Store) Findings() ([]model.Finding, error) {
	return queryList[model.Finding](s.db, "SELECT data FROM findings ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 WHEN 'info' THEN 4 ELSE 5 END, json_extract(data,'$.title')")
}

func (s *Store) Apply(repo model.Repository, snap model.Snapshot, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c := snap.Coverage
	c.RepositoryID = repo.ID
	c.LastAttempt = now
	var oldRaw string
	var old model.Coverage
	err = tx.QueryRow("SELECT data FROM coverage WHERE repository_id=? AND category=?", repo.ID, c.Category).Scan(&oldRaw)
	if err == nil {
		if err = json.Unmarshal([]byte(oldRaw), &old); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !snap.Complete {
		c.LastSuccess = old.LastSuccess
		c.Count = old.Count
		if c.AnalysisAt.IsZero() {
			c.AnalysisAt = old.AnalysisAt
			c.Ref = old.Ref
			c.Commit = old.Commit
		}
	} else {
		c.LastSuccess = now
		c.Count = len(snap.Findings)
		c.Collection = "success"
		previous := map[string]model.Finding{}
		rows, e := tx.Query("SELECT data FROM findings WHERE repository_id=? AND category=?", repo.ID, c.Category)
		if e != nil {
			return e
		}
		for rows.Next() {
			var raw string
			if e = rows.Scan(&raw); e != nil {
				rows.Close()
				return e
			}
			var f model.Finding
			if e = json.Unmarshal([]byte(raw), &f); e != nil {
				rows.Close()
				return e
			}
			previous[f.Source+":"+f.ID] = f
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, f := range snap.Findings {
			f.RepositoryID = repo.ID
			f.Category = c.Category
			f.SeenAt = now
			f.Missing = false
			key := f.Source + ":" + f.ID
			prior, exists := previous[key]
			change := model.Change{RepositoryID: repo.ID, FindingID: f.ID, Title: f.Title, At: now}
			if !old.LastSuccess.IsZero() {
				switch {
				case !exists:
					change.Kind = "new"
					change.After = f.NativeState
				case prior.NativeState != f.NativeState:
					change.Kind = "status"
					change.Before = prior.NativeState
					change.After = f.NativeState
				case prior.Severity != f.Severity:
					change.Kind = "severity"
					change.Before = prior.Severity
					change.After = f.Severity
				}
			}
			if change.Kind != "" {
				if _, err = tx.Exec("INSERT INTO changes(repository_id,at,data) VALUES(?,?,?)", repo.ID, now.Format(time.RFC3339Nano), encode(change)); err != nil {
					return err
				}
			}
			if exists && !old.LastSuccess.IsZero() && prior.NativeState != f.NativeState && prior.Severity != f.Severity {
				severityChange := model.Change{RepositoryID: repo.ID, FindingID: f.ID, Title: f.Title, Kind: "severity", Before: prior.Severity, After: f.Severity, At: now}
				if _, err = tx.Exec("INSERT INTO changes(repository_id,at,data) VALUES(?,?,?)", repo.ID, now.Format(time.RFC3339Nano), encode(severityChange)); err != nil {
					return err
				}
			}
			_, err = tx.Exec("INSERT INTO findings VALUES(?,?,?,?,?,?,?) ON CONFLICT(repository_id,source,external_id) DO UPDATE SET category=excluded.category,severity=excluded.severity,state=excluded.state,data=excluded.data", repo.ID, f.Source, f.ID, f.Category, f.Severity, f.State, encode(f))
			if err != nil {
				return err
			}
			delete(previous, key)
		}
		for _, f := range previous {
			f.Missing = true
			if _, err = tx.Exec("UPDATE findings SET data=? WHERE repository_id=? AND source=? AND external_id=?", encode(f), repo.ID, f.Source, f.ID); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec("INSERT INTO coverage VALUES(?,?,?) ON CONFLICT(repository_id,category) DO UPDATE SET data=excluded.data", repo.ID, c.Category, encode(c)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveRun(v model.Run) error {
	_, err := s.db.Exec("INSERT INTO runs VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", v.ID, v.StartedAt.Format(time.RFC3339Nano), encode(v))
	return err
}
func (s *Store) Runs() ([]model.Run, error) {
	seconds, nanos := timestampParts("started_at")
	return queryList[model.Run](s.db, "SELECT data FROM runs ORDER BY "+seconds+" DESC,"+nanos+" DESC LIMIT 100")
}
func (s *Store) Changes() ([]model.Change, error) {
	return queryList[model.Change](s.db, "SELECT data FROM changes ORDER BY id DESC LIMIT 200")
}
func (s *Store) InterruptRuns(now time.Time) error {
	runs, err := s.Runs()
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Status == "running" {
			r.Status = "interrupted"
			r.FinishedAt = now
			r.Message = "Application stopped before collection finished; previous complete data was retained."
			if err = s.SaveRun(r); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) Prune(now time.Time, settings model.Settings) error {
	cutoff := now.AddDate(0, 0, -settings.ChangesDays)
	seconds, nanos := timestampParts("at")
	_, err := s.db.Exec("DELETE FROM changes WHERE ("+seconds+","+nanos+") < (?,?)", cutoff.Unix(), cutoff.Nanosecond())
	if err != nil {
		return err
	}
	cutoff = now.AddDate(0, 0, -settings.LogsDays)
	seconds, nanos = timestampParts("started_at")
	_, err = s.db.Exec("DELETE FROM runs WHERE ("+seconds+","+nanos+") < (?,?)", cutoff.Unix(), cutoff.Nanosecond())
	return err
}

// timestampParts compares existing RFC3339Nano values by instant, including
// variable-length fractions and offsets. Strip fractions before unixepoch to
// avoid SQLite rounding them; keep nanoseconds in a separate integer expression.
// Column names are fixed internal identifiers, never request or provider input.
func timestampParts(column string) (seconds, nanos string) {
	suffix := "CASE WHEN substr(" + column + ",-1)='Z' THEN 1 ELSE 6 END"
	seconds = "unixepoch(substr(" + column + ",1,19) || substr(" + column + ",-(" + suffix + ")))"
	nanos = "CASE WHEN substr(" + column + ",20,1)='.' THEN CAST(substr(substr(" + column + ",21,length(" + column + ")-20-(" + suffix + ")) || '000000000',1,9) AS INTEGER) ELSE 0 END"
	return seconds, nanos
}

func (s *Store) Backup(ctx context.Context, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return errors.New("backup destination already exists")
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(destination, "'", "''")+"'"); err != nil {
		return fmt.Errorf("create database backup: %w", err)
	}
	return os.Chmod(destination, 0600)
}

func (s *Store) Integrity() error {
	var result string
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New("database integrity check failed")
	}
	return nil
}

// ValidateBackup never creates or migrates schema in an imported database.
func ValidateBackup(path string) error {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA trusted_schema=OFF"); err != nil {
		return err
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 1 {
		return errors.New("backup has an unsupported schema version")
	}
	if err = validateBackupSchema(db); err != nil {
		return err
	}
	connections, err := validateBackupConnections(db)
	if err != nil {
		return err
	}
	var raw string
	if err = db.QueryRow("SELECT data FROM settings WHERE id=1").Scan(&raw); err != nil {
		return err
	}
	var settings model.Settings
	if err = json.Unmarshal([]byte(raw), &settings); err != nil || len(settings.InstallationID) != 32 {
		return errors.New("backup has invalid installation settings")
	}
	if decoded, e := hex.DecodeString(settings.InstallationID); e != nil || len(decoded) != 16 {
		return errors.New("backup has an invalid credential namespace")
	}
	if settings.Hour < 0 || settings.Hour > 23 || settings.Minute < 0 || settings.Minute > 59 || settings.ChangesDays < 1 || settings.ChangesDays > 3650 || settings.LogsDays < 1 || settings.LogsDays > 365 {
		return errors.New("backup has invalid schedule/retention settings")
	}
	if _, err = time.LoadLocation(settings.Timezone); err != nil {
		return errors.New("backup has an invalid timezone")
	}
	repositoryConnections := map[string]model.Connection{}
	if err = validateBackupRecords(db, "SELECT data,id,connection_id,external_id,selected FROM repositories", func(r model.Repository) []any {
		if !validBackupProviderURL(r.URL, connections[r.ConnectionID]) {
			return nil
		}
		repositoryConnections[r.ID] = connections[r.ConnectionID]
		var selected int64
		if r.Selected {
			selected = 1
		}
		return []any{r.ID, r.ConnectionID, r.ExternalID, selected}
	}); err != nil {
		return err
	}
	if err = validateBackupRecords(db, "SELECT data,repository_id,category FROM coverage", func(c model.Coverage) []any {
		return []any{c.RepositoryID, c.Category}
	}); err != nil {
		return err
	}
	if err = validateBackupRecords(db, "SELECT data,repository_id,source,external_id,category,severity,state FROM findings", func(f model.Finding) []any {
		if !validBackupProviderURL(f.URL, repositoryConnections[f.RepositoryID]) {
			return nil
		}
		return []any{f.RepositoryID, f.Source, f.ID, f.Category, f.Severity, f.State}
	}); err != nil {
		return err
	}
	if err = validateBackupRecords(db, "SELECT data,id,started_at FROM runs", func(r model.Run) []any {
		return []any{r.ID, r.StartedAt.Format(time.RFC3339Nano)}
	}); err != nil {
		return err
	}
	// Change IDs are allocated by SQLite after serialization; only the duplicated
	// repository and timestamp fields are part of the serialized record contract.
	if err = validateBackupRecords(db, "SELECT data,repository_id,at FROM changes", func(c model.Change) []any {
		return []any{c.RepositoryID, c.At.Format(time.RFC3339Nano)}
	}); err != nil {
		return err
	}
	var result string
	if err = db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New("backup integrity check failed")
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("backup contains inconsistent relationships")
	}
	return rows.Err()
}

// ReadSettings reads an existing installation without locking or changing it.
// It is used to stop a background service which itself owns the process lock.
func ReadSettings(path string) (model.Settings, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return model.Settings{}, err
	}
	defer db.Close()
	var raw string
	if err = db.QueryRow("SELECT data FROM settings WHERE id=1").Scan(&raw); err != nil {
		return model.Settings{}, err
	}
	var s model.Settings
	err = json.Unmarshal([]byte(raw), &s)
	return s, err
}
