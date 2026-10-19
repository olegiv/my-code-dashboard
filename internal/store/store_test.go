// SPDX-License-Identifier: GPL-3.0-only
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/model"
)

func fixture(t *testing.T) (*Store, model.Repository) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := model.Connection{ID: "fixture", Provider: "github", BaseURL: "https://github.com"}
	if err = s.SaveConnection(c); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveDiscovered(c, []model.Repository{{ExternalID: "42", Name: "example/service"}}); err != nil {
		t.Fatal(err)
	}
	repos, _ := s.Repositories()
	if err = s.SelectRepositories(map[string]bool{repos[0].ID: true}); err != nil {
		t.Fatal(err)
	}
	repos, _ = s.Repositories()
	return s, repos[0]
}
func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestSnapshotsRetainDataOnFailureAndTrackLifecycle(t *testing.T) {
	s, r := fixture(t)
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	f := model.Finding{ID: "1", Source: "fixture", Title: "Synthetic advisory", Severity: "high", NativeSeverity: "HIGH", State: "open", NativeState: "DETECTED"}
	snap := model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "dependencies", AnalysisAt: now.Add(-time.Hour)}, Findings: []model.Finding{f}}
	check(t, s.Apply(r, snap, now))
	changes, _ := s.Changes()
	if len(changes) != 0 {
		t.Fatal("initial import must establish a baseline")
	}
	check(t, s.Apply(r, model.Snapshot{Coverage: model.Coverage{Category: "dependencies", Collection: "denied"}}, now.Add(time.Hour)))
	findings, _ := s.Findings()
	cov, _ := s.Coverage()
	if len(findings) != 1 || findings[0].Missing || findings[0].State != "open" || !cov[0].LastSuccess.Equal(now) || !cov[0].AnalysisAt.Equal(snap.Coverage.AnalysisAt) {
		t.Fatalf("failure erased data: %+v %+v", findings, cov)
	}
	f.State = "resolved"
	f.NativeState = "RESOLVED"
	snap.Findings = []model.Finding{f}
	check(t, s.Apply(r, snap, now.Add(2*time.Hour)))
	changes, _ = s.Changes()
	if len(changes) != 1 || changes[0].Kind != "status" || changes[0].Before != "DETECTED" {
		t.Fatalf("lifecycle not recorded: %+v", changes)
	}
	f.Severity = "critical"
	snap.Findings = []model.Finding{f}
	check(t, s.Apply(r, snap, now.Add(3*time.Hour)))
	changes, _ = s.Changes()
	if changes[0].Kind != "severity" {
		t.Fatal("severity change missing")
	}
	snap.Findings = nil
	check(t, s.Apply(r, snap, now.Add(4*time.Hour)))
	findings, _ = s.Findings()
	cov, _ = s.Coverage()
	if !findings[0].Missing || findings[0].State != "resolved" || cov[0].Count != 0 {
		t.Fatal("missing alert must retain its prior native state")
	}
	f.ID = "2"
	snap.Findings = []model.Finding{f}
	check(t, s.Apply(r, snap, now.Add(5*time.Hour)))
	changes, _ = s.Changes()
	if changes[0].Kind != "new" {
		t.Fatal("new alert missing")
	}
}
func TestZeroVersusUnavailableAndAtomicRollback(t *testing.T) {
	s, r := fixture(t)
	now := time.Now().UTC()
	check(t, s.Apply(r, model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code", ScannerState: "unknown"}}, now))
	check(t, s.Apply(r, model.Snapshot{Coverage: model.Coverage{Category: "secrets", Collection: "unavailable"}}, now))
	cov, _ := s.Coverage()
	if len(cov) != 2 || cov[0].Collection != "success" || cov[0].LastSuccess.IsZero() || !cov[1].LastSuccess.IsZero() {
		t.Fatalf("zero and inaccessible collapsed: %+v", cov)
	}
	unknown := r
	unknown.ID = "not-discovered"
	if err := s.Apply(unknown, model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code"}, Findings: []model.Finding{{ID: "bad", Source: "fixture"}}}, now); err == nil {
		t.Fatal("orphan snapshot accepted")
	}
	findings, _ := s.Findings()
	if len(findings) != 0 {
		t.Fatal("failed transaction leaked findings")
	}
}

func TestCoverageGapsRetainPreviousFindingsAndAnalysis(t *testing.T) {
	for _, status := range []string{"disabled", "no_analysis"} {
		t.Run(status, func(t *testing.T) {
			s, repo := fixture(t)
			now := time.Now().UTC().Truncate(time.Second)
			before := model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code", AnalysisAt: now.Add(-time.Hour), Ref: "refs/heads/main", Commit: "synthetic-commit"}, Findings: []model.Finding{{ID: "1", Source: "fixture", State: "open", NativeState: "open", Title: "Synthetic finding"}}}
			check(t, s.Apply(repo, before, now))
			check(t, s.Apply(repo, model.Snapshot{Coverage: model.Coverage{Category: "code", Collection: status, Note: "Synthetic coverage gap."}}, now.Add(time.Hour)))
			findings, _ := s.Findings()
			coverage, _ := s.Coverage()
			if len(findings) != 1 || findings[0].State != "open" || findings[0].Missing || coverage[0].Collection != status || coverage[0].Count != 1 || !coverage[0].LastSuccess.Equal(now) || !coverage[0].AnalysisAt.Equal(before.Coverage.AnalysisAt) || coverage[0].Commit != "synthetic-commit" {
				t.Fatalf("coverage gap replaced valid prior evidence: %+v %+v", findings, coverage)
			}
			check(t, s.Apply(repo, model.Snapshot{Coverage: model.Coverage{Category: "secrets", Collection: status}}, now))
			coverage, _ = s.Coverage()
			if !coverage[1].LastSuccess.IsZero() || coverage[1].Collection != status {
				t.Fatal("first coverage gap was recorded as a successful zero report")
			}
			changes, _ := s.Changes()
			if len(changes) != 0 {
				t.Fatal("coverage gap generated finding lifecycle changes")
			}
		})
	}
}
func TestRenameAndSelectionSurviveRefresh(t *testing.T) {
	s, r := fixture(t)
	check(t, s.SelectRepositories(map[string]bool{}))
	r.Name = "example/renamed"
	check(t, s.SaveRepository(r))
	fresh, _ := s.Repository(r.ID)
	if fresh.Selected || fresh.Name != r.Name {
		t.Fatal("in-flight refresh overwrote GUI selection")
	}
	c, _ := s.Connection("fixture")
	check(t, s.SaveDiscovered(c, []model.Repository{{ExternalID: r.ExternalID, Name: "example/transferred", Archived: true}}))
	repos, _ := s.Repositories()
	if len(repos) != 1 || repos[0].ID != r.ID || repos[0].Selected || !repos[0].Archived {
		t.Fatalf("unstable identity: %+v", repos)
	}
}

func TestMetadataCannotChangeStoredExternalIdentity(t *testing.T) {
	s, before := fixture(t)
	for _, id := range []string{"0", "43", ""} {
		changed := before
		changed.ExternalID = id
		changed.Name = "example/wrong-repository"
		if err := s.SaveRepository(changed); err == nil {
			t.Errorf("accepted changed external identity %q", id)
		}
		fresh, err := s.Repository(before.ID)
		if err != nil || fresh != before {
			t.Fatalf("rejected metadata changed stored repository: %+v %v", fresh, err)
		}
	}
	// The ordinary rename still updates the same record and produces a backup
	// whose serialized identities agree with its indexed columns.
	renamed := before
	renamed.Name = "example/renamed"
	check(t, s.SaveRepository(renamed))
	path := filepath.Join(t.TempDir(), "backup.sqlite")
	check(t, s.Backup(t.Context(), path))
	check(t, ValidateBackup(path))
}
func TestIndependentInstallationsBackupAndPermissions(t *testing.T) {
	s, _ := fixture(t)
	other, err := Open(filepath.Join(t.TempDir(), "other.sqlite"))
	check(t, err)
	defer other.Close()
	a, _ := s.Settings()
	b, _ := other.Settings()
	if a.InstallationID == b.InstallationID {
		t.Fatal("independent installs share identity")
	}
	path := filepath.Join(t.TempDir(), "copy.sqlite")
	check(t, s.Backup(context.Background(), path))
	check(t, ValidateBackup(path))
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("backup is not private")
	}
	if err = s.Backup(context.Background(), path); err == nil {
		t.Fatal("existing backup overwritten")
	}
	check(t, os.WriteFile(filepath.Join(t.TempDir(), "unused"), nil, 0600))
	empty := filepath.Join(t.TempDir(), "empty.sqlite")
	check(t, os.WriteFile(empty, nil, 0600))
	if ValidateBackup(empty) == nil {
		t.Fatal("empty database accepted as backup")
	}
	check(t, s.SaveRun(model.Run{ID: "interrupted", StartedAt: time.Now(), Status: "running"}))
	check(t, s.InterruptRuns(time.Now()))
	runs, _ := s.Runs()
	if runs[0].Status != "interrupted" {
		t.Fatal("interrupted refresh not recovered")
	}
}

func TestSimultaneousStateSeverityChangesAndReadOnlySettings(t *testing.T) {
	s, r := fixture(t)
	now := time.Now().UTC()
	f := model.Finding{ID: "1", Source: "fixture", NativeState: "open", State: "open", Severity: "high"}
	snap := model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code"}, Findings: []model.Finding{f}}
	check(t, s.Apply(r, snap, now))
	f.NativeState = "fixed"
	f.State = "resolved"
	f.Severity = "critical"
	snap.Findings = []model.Finding{f}
	check(t, s.Apply(r, snap, now.Add(time.Hour)))
	changes, _ := s.Changes()
	if len(changes) != 2 {
		t.Fatal("simultaneous changes must both be recorded")
	}
	a, _ := s.Settings()
	b, err := ReadSettings(s.Path)
	check(t, err)
	if a.InstallationID != b.InstallationID {
		t.Fatal("read-only settings do not match")
	}
}

func TestBackupRejectsUntrustedSchemaAndConnectionIdentities(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"shadow_id", `INSERT INTO connections SELECT 'shadow', data FROM connections WHERE id='fixture'`},
		{"blob_id", `INSERT INTO connections SELECT CAST(id AS BLOB), data FROM connections WHERE id='fixture'`},
		{"null_id", `INSERT INTO connections SELECT NULL, data FROM connections WHERE id='fixture'`},
		{"escaped_shadow_id", `INSERT INTO connections VALUES('shadow', '{"id":"\u0066ixture","provider":"gitlab","base_url":"https://attacker.example"}')`},
		{"mismatched_id", `UPDATE connections SET data=json_set(data,'$.id','different')`},
		{"empty_id", `UPDATE connections SET data=json_set(data,'$.id','')`},
		{"null_record", `UPDATE connections SET data='null'`},
		{"blob_record", `UPDATE connections SET data=CAST(data AS BLOB)`},
		{"case_alias", `UPDATE connections SET data='{"id":"fixture","ID":"different"}'`},
		{"malformed_record", `UPDATE connections SET data='{'`},
		{"quarantine_trigger", `CREATE TRIGGER clear_quarantine AFTER UPDATE OF data ON connections BEGIN UPDATE connections SET data=json_set(NEW.data,'$.reconnect_required',json('false')) WHERE id=NEW.id; END`},
		{"delayed_trigger", `CREATE TRIGGER clear_on_refresh AFTER INSERT ON runs BEGIN UPDATE connections SET data=json_set(data,'$.reconnect_required',json('false')); END`},
		{"extra_view", `CREATE VIEW imported_view AS SELECT data FROM connections`},
		{"changed_index", `DROP INDEX repo_connection; CREATE INDEX repo_connection ON repositories(external_id)`},
		{"changed_table", `ALTER TABLE connections ADD COLUMN imported TEXT`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := fixture(t)
			_, err := s.db.Exec(tc.sql)
			check(t, err)
			path := filepath.Join(t.TempDir(), "import.sqlite")
			check(t, s.Backup(t.Context(), path))
			if ValidateBackup(path) == nil {
				t.Fatal("untrusted backup accepted")
			}
		})
	}
}

func TestBackupAcceptsCanonicalSchemaAndEncodedConnectionID(t *testing.T) {
	s, _ := fixture(t)
	c := model.Connection{ID: "synthetic-\"雪", Provider: "gitlab", BaseURL: "https://gitlab.example"}
	check(t, s.SaveConnection(c))
	raw, err := json.Marshal(c)
	check(t, err)
	// JSON escaping changes representation, not the decoded credential identity.
	_, err = s.db.Exec("UPDATE connections SET data=? WHERE id=?", string(raw), c.ID)
	check(t, err)
	_, err = s.db.Exec(`UPDATE connections SET data='{"ID":"ignored","id":"\u0066ixture"}' WHERE id='fixture'`)
	check(t, err)
	path := filepath.Join(t.TempDir(), "valid.sqlite")
	check(t, s.Backup(t.Context(), path))
	check(t, ValidateBackup(path))
}

func TestBackupValidatesRecordsBeyondUIHistoryLimit(t *testing.T) {
	s, _ := fixture(t)
	now := time.Now().UTC()
	for i := range 101 {
		check(t, s.SaveRun(model.Run{ID: fmt.Sprint(i), StartedAt: now.Add(time.Duration(i) * time.Minute), Status: "success"}))
	}
	path := filepath.Join(t.TempDir(), "genuine.sqlite")
	check(t, s.Backup(t.Context(), path))
	check(t, ValidateBackup(path))
	for _, bad := range []string{`{`, `null`, `[]`, `{"succeeded":"invalid integer"}`, `{}`, `{"id":"different"}`} {
		t.Run(bad, func(t *testing.T) {
			_, err := s.db.Exec("UPDATE runs SET data=? WHERE id='0'", bad)
			check(t, err)
			// Ordinary UI reads cap history at 100; import must check the rest.
			runs, err := s.Runs()
			check(t, err)
			if len(runs) != 100 {
				t.Fatal("history fixture did not exceed UI limit")
			}
			candidate := filepath.Join(t.TempDir(), "malformed.sqlite")
			check(t, s.Backup(t.Context(), candidate))
			if ValidateBackup(candidate) == nil {
				t.Fatal("invalid history beyond UI limit accepted")
			}
		})
	}
	_, err := s.db.Exec("UPDATE runs SET data=CAST('{}' AS BLOB) WHERE id='0'")
	check(t, err)
	candidate := filepath.Join(t.TempDir(), "blob.sqlite")
	check(t, s.Backup(t.Context(), candidate))
	if ValidateBackup(candidate) == nil {
		t.Fatal("non-text history record accepted")
	}
}

func TestHistoryOrdersAndPrunesExactInstants(t *testing.T) {
	base := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, cutoff := range []time.Time{base, base.Add(time.Nanosecond), base.Add(100 * time.Millisecond)} {
		t.Run(cutoff.Format(time.RFC3339Nano), func(t *testing.T) {
			s, repo := fixture(t)
			instants := []time.Time{base.Add(-time.Nanosecond), base, base.Add(time.Nanosecond), base.Add(100 * time.Millisecond), base.Add(time.Second - time.Nanosecond), base.Add(time.Second)}
			for i, at := range instants {
				// Existing offset-bearing records must sort like UTC records too.
				if i%2 != 0 {
					at = at.In(time.FixedZone("synthetic", 2*60*60))
				}
				check(t, s.SaveRun(model.Run{ID: fmt.Sprint(i), StartedAt: at, Status: "success"}))
				change := model.Change{RepositoryID: repo.ID, FindingID: fmt.Sprint(i), At: at, Kind: "new"}
				_, err := s.db.Exec("INSERT INTO changes(repository_id,at,data) VALUES(?,?,?)", repo.ID, at.Format(time.RFC3339Nano), encode(change))
				check(t, err)
			}
			runs, err := s.Runs()
			check(t, err)
			for i, run := range runs {
				if !run.StartedAt.Equal(instants[len(instants)-1-i]) {
					t.Fatal("run history is not ordered by exact instant")
				}
			}
			settings := model.DefaultSettings()
			settings.LogsDays, settings.ChangesDays = 1, 1
			check(t, s.Prune(cutoff.AddDate(0, 0, 1), settings))
			runs, err = s.Runs()
			check(t, err)
			changes, err := s.Changes()
			check(t, err)
			var expected int
			for _, at := range instants {
				if !at.Before(cutoff) {
					expected++
				}
			}
			if len(runs) != expected || len(changes) != expected {
				t.Fatalf("fractional retention boundary: %d runs, %d changes, want %d", len(runs), len(changes), expected)
			}
		})
	}
}
