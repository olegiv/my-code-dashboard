// SPDX-License-Identifier: GPL-3.0-only
package platform

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/engine"
	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/provider"
	"github.com/olegiv/my-code-dashboard/internal/secret"
	"github.com/olegiv/my-code-dashboard/internal/store"
)

func TestRestoreRejectsCredentialQuarantineBypasses(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"shadow_identity", `INSERT INTO connections SELECT 'shadow', data FROM connections WHERE id='synthetic-credential'`},
		{"blob_identity", `INSERT INTO connections SELECT CAST(id AS BLOB), data FROM connections WHERE id='synthetic-credential'`},
		{"null_identity", `INSERT INTO connections SELECT NULL, data FROM connections WHERE id='synthetic-credential'`},
		{"quarantine_trigger", `CREATE TRIGGER clear_quarantine AFTER UPDATE OF data ON connections BEGIN UPDATE connections SET data=json_set(NEW.data,'$.reconnect_required',json('false')) WHERE id=NEW.id; END`},
		{"delayed_trigger", `CREATE TRIGGER clear_on_refresh AFTER INSERT ON runs BEGIN UPDATE connections SET data=json_set(data,'$.reconnect_required',json('false')); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := restoreSecurityArchive(t, tc.sql)
			dir, destinationID := restoreSecurityDestination(t)
			path := filepath.Join(dir, "dashboard.sqlite")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			restoreErr := Restore(dir, archive)
			if restoreErr == nil {
				s, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				credentials, calls := refreshRestoredSelection(t, s)
				s.Close()
				t.Fatalf("backup bypassed quarantine: credential reads=%d, provider calls=%d", credentials.reads, calls)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			settings, err := store.ReadSettings(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || settings.InstallationID != destinationID {
				t.Fatal("rejected backup changed the destination database or identity")
			}
			copies, _ := filepath.Glob(filepath.Join(dir, "before-restore-*.sqlite"))
			if len(copies) != 0 {
				t.Fatal("rejected backup reached destination replacement preparation")
			}
		})
	}
}

func TestGenuineRestoreBlocksCredentialsAcrossRefreshAndRestart(t *testing.T) {
	archive := restoreSecurityArchive(t, "")
	dir, destinationID := restoreSecurityDestination(t)
	if err := Restore(dir, archive); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s, err := store.Open(filepath.Join(dir, "dashboard.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		settings, err := s.Settings()
		if err != nil || settings.InstallationID != destinationID || settings.Hour != 10 || settings.ChangesDays != 123 {
			t.Fatalf("destination identity changed: %v", err)
		}
		connections, err := s.Connections()
		if err != nil || len(connections) != 1 || !connections[0].ReconnectRequired {
			t.Fatalf("genuine restore did not quarantine its connection: %v", err)
		}
		repos, repoErr := s.Repositories()
		findings, findingErr := s.Findings()
		changes, changeErr := s.Changes()
		if repoErr != nil || findingErr != nil || changeErr != nil || len(repos) != 1 || !repos[0].Selected || len(findings) != 1 || findings[0].NativeState != "DETECTED" || len(changes) != 1 {
			t.Fatal("genuine restore lost selected repositories, native findings or change history")
		}
		if err = s.SelectRepositories(map[string]bool{}); err != nil {
			t.Fatal(err)
		}
		reloaded, err := s.Repository(repos[0].ID)
		if err != nil || reloaded.Selected {
			t.Fatalf("restored repository cannot be deselected through its decoded identity: %v", err)
		}
		if err = s.SelectRepositories(map[string]bool{repos[0].ID: true}); err != nil {
			t.Fatal(err)
		}
		runs, err := s.Runs()
		if err != nil || len(runs) == 0 {
			t.Fatalf("genuine restore lost run history: %v", err)
		}
		credentials, calls := refreshRestoredSelection(t, s)
		s.Close()
		if credentials.reads != 0 || calls != 0 {
			t.Fatal("restored discovery or refresh consumed a destination credential")
		}
	}
}

func TestRestoreRejectsMalformedApplicationRecords(t *testing.T) {
	for _, table := range []string{"repositories", "coverage", "findings", "runs", "changes"} {
		t.Run(table, func(t *testing.T) {
			archive := restoreSecurityArchive(t, "UPDATE "+table+" SET data='{'")
			dir, _ := restoreSecurityDestination(t)
			path := filepath.Join(dir, "dashboard.sqlite")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = Restore(dir, archive); err == nil {
				t.Fatal("malformed application record was installed")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid application record changed destination")
			}
		})
	}
}

func TestRestoreSelectionWithJSONAliases(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE repositories SET data=json_set(data,'$.SELECTED',json('true'))`,
		`UPDATE repositories SET data=substr(data,1,length(data)-1)||',"selected":true}'`,
		`UPDATE repositories SET data=substr(data,1,length(data)-1)||',"se\u006cected":true}'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			archive := restoreSecurityArchive(t, mutation)
			dir, _ := restoreSecurityDestination(t)
			if err := Restore(dir, archive); err != nil {
				t.Fatal(err)
			}
			s, err := store.Open(filepath.Join(dir, "dashboard.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			repos, err := s.Repositories()
			if err != nil || len(repos) != 1 || !repos[0].Selected {
				t.Fatalf("valid aliased backup lost its selection: %v", err)
			}
			if err = s.SelectRepositories(map[string]bool{}); err != nil {
				t.Fatal(err)
			}
			reloaded, err := s.Repository(repos[0].ID)
			if err != nil || reloaded.Selected {
				t.Fatalf("imported JSON alias overrode saved deselection: %v", err)
			}
			e := engine.New(t.Context(), s, secret.NewMemory(), false)
			if _, _, err = e.Start("manual"); err != nil {
				t.Fatal(err)
			}
			e.Wait()
			runs, err := s.Runs()
			if err != nil || len(runs) == 0 || len(runs[0].Outcomes) != 0 || runs[0].Failed != 0 || runs[0].Succeeded != 0 {
				t.Fatalf("deselected restored repository was collected: %v", err)
			}
			if err = s.SelectRepositories(map[string]bool{repos[0].ID: true}); err != nil {
				t.Fatal(err)
			}
			reloaded, err = s.Repository(repos[0].ID)
			if err != nil || !reloaded.Selected || reloaded.Name != repos[0].Name || reloaded.ConnectionID != repos[0].ConnectionID {
				t.Fatalf("reselection lost repository metadata: %v", err)
			}
			backup := filepath.Join(t.TempDir(), "after-selection.sqlite")
			if err = s.Backup(t.Context(), backup); err != nil {
				t.Fatal(err)
			}
			if err = store.ValidateBackup(backup); err != nil {
				t.Fatalf("selection left a non-restorable database: %v", err)
			}
		})
	}
}

func TestRestoreRejectsInconsistentRelationalRecords(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE repositories SET data='{}'`,
		`UPDATE repositories SET data=json_set(data,'$.id','different')`,
		`UPDATE repositories SET data=json_set(data,'$.ID','different')`,
		`UPDATE repositories SET data=json_set(data,'$.connection_id','different')`,
		`UPDATE repositories SET data=json_set(data,'$.external_id','different')`,
		`UPDATE repositories SET data=json_set(data,'$.selected',json('false'))`,
		`UPDATE repositories SET selected=2`,
		`UPDATE coverage SET data='{}'`,
		`UPDATE coverage SET data=json_set(data,'$.repository_id','different')`,
		`UPDATE coverage SET data=json_set(data,'$.category','different')`,
		`UPDATE findings SET data='{}'`,
		`UPDATE findings SET data=json_set(data,'$.repository_id','different')`,
		`UPDATE findings SET data=json_set(data,'$.id','different')`,
		`UPDATE findings SET data=json_set(data,'$.source','different')`,
		`UPDATE findings SET data=json_set(data,'$.category','different')`,
		`UPDATE findings SET data=json_set(data,'$.severity','different')`,
		`UPDATE findings SET data=json_set(data,'$.state','different')`,
		`UPDATE runs SET data='{}'`,
		`UPDATE runs SET data=json_set(data,'$.id','different')`,
		`UPDATE runs SET data=json_set(data,'$.started_at','2020-01-01T00:00:00Z')`,
		`UPDATE changes SET data='{}'`,
		`UPDATE changes SET data=json_set(data,'$.repository_id','different')`,
		`UPDATE changes SET data=json_set(data,'$.at','2020-01-01T00:00:00Z')`,
	} {
		t.Run(mutation, func(t *testing.T) {
			archive := restoreSecurityArchive(t, mutation)
			dir, _ := restoreSecurityDestination(t)
			path := filepath.Join(dir, "dashboard.sqlite")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = Restore(dir, archive); err == nil {
				t.Fatal("inconsistent application identity or indexed field was installed")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("inconsistent application record changed destination")
			}
		})
	}
}

func restoreSecurityDestination(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SaveConnection(model.Connection{ID: "synthetic-credential", Provider: "github", BaseURL: "https://github.com", Name: "Destination"}); err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	return dir, settings.InstallationID
}

func restoreSecurityArchive(t *testing.T, mutation string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := model.Connection{ID: "synthetic-credential", Provider: "gitlab", BaseURL: "https://attacker.example", Name: "Synthetic source"}
	settings, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Hour, settings.ChangesDays = 10, 123
	if err = s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveConnection(c); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveDiscovered(c, []model.Repository{{ExternalID: "42", Name: "example/synthetic"}}); err != nil {
		t.Fatal(err)
	}
	repos, err := s.Repositories()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SelectRepositories(map[string]bool{repos[0].ID: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	snapshot := model.Snapshot{Complete: true, Coverage: model.Coverage{Category: "code"}, Findings: []model.Finding{{ID: "synthetic-finding", Source: "fixture", Category: "code", State: "open", NativeState: "DETECTED", Severity: "high"}}}
	if err = s.Apply(repos[0], snapshot, now); err != nil {
		t.Fatal(err)
	}
	snapshot.Findings[0].Severity = "critical"
	if err = s.Apply(repos[0], snapshot, now); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveRun(model.Run{ID: "synthetic-history", StartedAt: now, FinishedAt: now, Status: "complete"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if mutation != "" {
		if _, err = db.Exec(mutation); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"manifest.json", []byte(`{"format":1,"credentials_included":false}`)}, {"dashboard.sqlite", data}} {
		if err = tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0600, Size: int64(len(entry.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "synthetic.backup.tar.gz")
	if err = os.WriteFile(archive, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return archive
}

type observedCredentials struct {
	*secret.Memory
	reads int
}

func (s *observedCredentials) Get(id string) (string, error) {
	s.reads++
	return s.Memory.Get(id)
}

func refreshRestoredSelection(t *testing.T, s *store.Store) (*observedCredentials, int) {
	t.Helper()
	credentials := &observedCredentials{Memory: secret.NewMemory()}
	if err := credentials.Set("synthetic-credential", "synthetic sentinel only"); err != nil {
		t.Fatal(err)
	}
	e := engine.New(t.Context(), s, credentials, false)
	calls := 0
	e.Factory = func(model.Connection, string) (provider.Provider, error) {
		calls++
		return nil, errors.New("synthetic stop before any network request")
	}
	if _, _, err := e.Start("manual"); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	if err := e.Tick(e.Now()); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	_ = e.Discover(t.Context(), "synthetic-credential")
	return credentials, calls
}
