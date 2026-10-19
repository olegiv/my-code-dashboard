// SPDX-License-Identifier: GPL-3.0-only
package platform

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
			copies, _ := filepath.Glob(filepath.Join(dir, "before-restore-*"))
			if len(copies) != 0 {
				t.Fatal("rejected backup reached destination replacement preparation")
			}
		})
	}
}

func TestRestoredFindingLinksStayWithinProvider(t *testing.T) {
	for _, table := range []string{"findings", "repositories"} {
		for _, tc := range []struct {
			name, link, connection string
			valid                  bool
		}{
			{name: "empty", valid: true},
			{name: "GitLab HTTPS", link: "https://attacker.example/group/project/-/vulnerabilities/42", valid: true},
			{name: "GitLab host case", link: "https://ATTACKER.example/group/project/-/vulnerabilities/42", valid: true},
			{name: "GitLab explicit default port", link: "https://attacker.example:443/group/project/-/vulnerabilities/42", valid: true},
			{name: "GitLab omitted default port", link: "https://attacker.example/group/project/-/vulnerabilities/42", connection: `UPDATE connections SET data=json_set(data,'$.base_url','https://attacker.example:443');`, valid: true},
			{name: "GitLab nondefault port omitted", link: "https://attacker.example/group/project/-/vulnerabilities/42", connection: `UPDATE connections SET data=json_set(data,'$.base_url','https://attacker.example:8443');`},
			{name: "GitLab configured port and subpath", link: "https://attacker.example:8443/gitlab/group/project/-/vulnerabilities/42", connection: `UPDATE connections SET data=json_set(data,'$.base_url','https://attacker.example:8443/gitlab');`, valid: true},
			{name: "foreign host", link: "https://unrelated.example/phishing"},
			{name: "lookalike host", link: "https://attacker.example.unrelated.example/phishing"},
			{name: "HTTP", link: "http://attacker.example/vulnerabilities/42"},
			{name: "credentials", link: "https://user@attacker.example/vulnerabilities/42"},
			{name: "relative", link: "/vulnerabilities/42"},
			{name: "network path", link: "//attacker.example/vulnerabilities/42"},
			{name: "different port", link: "https://attacker.example:8443/vulnerabilities/42"},
			{name: "GitHub HTTPS", link: "https://github.com/example/project/security/code-scanning/42", connection: `UPDATE connections SET data=json_set(data,'$.provider','github','$.base_url','https://github.com');`, valid: true},
			{name: "GitHub explicit default port", link: "https://github.com:443/example/project/security/code-scanning/42", connection: `UPDATE connections SET data=json_set(data,'$.provider','github','$.base_url','https://github.com');`, valid: true},
			{name: "GitHub foreign", link: "https://unrelated.example/phishing", connection: `UPDATE connections SET data=json_set(data,'$.provider','github','$.base_url','https://github.com');`},
			{name: "GitHub forged base", link: "https://attacker.example/phishing", connection: `UPDATE connections SET data=json_set(data,'$.provider','github');`},
		} {
			t.Run(table+"/"+tc.name, func(t *testing.T) {
				archive := restoreSecurityArchive(t, tc.connection+`UPDATE repositories SET data=json_set(data,'$.provider',(SELECT json_extract(data,'$.provider') FROM connections WHERE id=repositories.connection_id)); UPDATE `+table+` SET data=json_set(data,'$.url','`+tc.link+`')`)
				dir, _ := restoreSecurityDestination(t)
				path := filepath.Join(dir, "dashboard.sqlite")
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				err = Restore(dir, archive)
				if tc.valid {
					if err != nil {
						t.Fatal(err)
					}
					s, err := store.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					var link string
					if table == "findings" {
						findings, err := s.Findings()
						if err != nil || len(findings) != 1 {
							t.Fatal("missing restored finding", err)
						}
						link = findings[0].URL
					} else {
						repos, err := s.Repositories()
						if err != nil || len(repos) != 1 {
							t.Fatal("missing restored repository", err)
						}
						link = repos[0].URL
					}
					if link != tc.link {
						t.Fatalf("valid restored link changed: %q", link)
					}
					return
				}
				if err == nil {
					t.Fatal("unsafe provider link was restored")
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("rejected archive changed the destination")
				}
				copies, _ := filepath.Glob(filepath.Join(dir, "before-restore-*"))
				if len(copies) != 0 {
					t.Fatal("unsafe archive reached destination replacement")
				}
			})
		}
	}
}

func TestRestoreValidatesConnectionsWithEmptyLinks(t *testing.T) {
	for _, tc := range []struct {
		name, provider, base string
		valid                bool
	}{
		{"GitHub", "github", "https://github.com", true},
		{"GitHub trailing slash", "github", "https://github.com/", true},
		{"GitLab", "gitlab", "https://gitlab.example", true},
		{"GitLab path and port", "gitlab", "https://GITLAB.example:8443/gitlab", true},
		{"empty target", "gitlab", "", false},
		{"relative target", "gitlab", "gitlab.example", false},
		{"HTTP", "gitlab", "http://gitlab.example", false},
		{"malformed", "gitlab", "https://gitlab.example:invalid", false},
		{"credentials", "gitlab", "https://user@gitlab.example", false},
		{"query", "gitlab", "https://gitlab.example?query=value", false},
		{"fragment", "gitlab", "https://gitlab.example#fragment", false},
		{"unknown provider", "unsupported", "https://gitlab.example", false},
		{"empty provider", "", "https://gitlab.example", false},
		{"GitHub foreign target", "github", "https://gitlab.example", false},
		{"GitHub subpath", "github", "https://github.com/subpath", false},
	} {
		for _, unreferenced := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unreferenced=%t", tc.name, unreferenced), func(t *testing.T) {
				mutation := `UPDATE repositories SET data=json_set(data,'$.url',''); UPDATE findings SET data=json_set(data,'$.url','');`
				if unreferenced {
					mutation += fmt.Sprintf(`INSERT INTO connections VALUES('unreferenced',json_object('id','unreferenced','provider','%s','base_url','%s'));`, tc.provider, tc.base)
				} else {
					mutation += fmt.Sprintf(`UPDATE connections SET data=json_set(data,'$.provider','%s','$.base_url','%s'); UPDATE repositories SET data=json_set(data,'$.provider','%s');`, tc.provider, tc.base, tc.provider)
				}
				archive := restoreSecurityArchive(t, mutation)
				dir, destinationID := restoreSecurityDestination(t)
				path := filepath.Join(dir, "dashboard.sqlite")
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				err = Restore(dir, archive)
				if !tc.valid {
					if err == nil {
						t.Fatal("invalid connection bypassed empty-link validation")
					}
					after, readErr := os.ReadFile(path)
					settings, settingsErr := store.ReadSettings(path)
					copies, globErr := filepath.Glob(filepath.Join(dir, "before-restore-*"))
					if readErr != nil || settingsErr != nil || globErr != nil || !bytes.Equal(before, after) || settings.InstallationID != destinationID || len(copies) != 0 {
						t.Fatal("invalid connection reached destination replacement")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				s, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				connections, err := s.Connections()
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range connections {
					if !c.ReconnectRequired {
						t.Fatal("valid connection lost credential quarantine")
					}
				}
				repos, repoErr := s.Repositories()
				findings, findingErr := s.Findings()
				if repoErr != nil || findingErr != nil || len(repos) != 1 || !repos[0].Selected || repos[0].URL != "" || len(findings) != 1 || findings[0].URL != "" || findings[0].NativeState != "DETECTED" {
					t.Fatal("valid empty links lost selection or active findings")
				}
				c, err := s.Connection(repos[0].ConnectionID)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.SaveDiscovered(c, []model.Repository{{ExternalID: "43", Name: "example/discovered"}}); err != nil {
					t.Fatal("valid restored target blocked later discovery", err)
				}
			})
		}
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

func TestRestoreRecoversUnhealthyDestination(t *testing.T) {
	const archiveID = "00112233445566778899aabbccddeeff"
	archive := restoreSecurityArchive(t, "UPDATE settings SET data=json_set(data,'$.installation_id','"+archiveID+"')")
	for _, condition := range []string{"new", "healthy", "corrupt with sidecars", "newer schema", "invalid settings", "invalid identity", "newer schema with WAL", "orphan sidecars"} {
		t.Run(condition, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "dashboard.sqlite")
			destinationID := ""
			if condition != "new" && condition != "corrupt with sidecars" && condition != "orphan sidecars" {
				seed := target
				if condition == "newer schema with WAL" {
					seed = filepath.Join(t.TempDir(), "dashboard.sqlite")
				}
				s, err := store.Open(seed)
				if err != nil {
					t.Fatal(err)
				}
				settings, err := s.Settings()
				if err != nil {
					t.Fatal(err)
				}
				destinationID = settings.InstallationID
				if condition == "invalid identity" {
					settings.InstallationID = "invalid-local-identity"
					if err = s.SaveSettings(settings); err != nil {
						t.Fatal(err)
					}
					destinationID = ""
				}
				if condition == "newer schema with WAL" {
					db, err := sql.Open("sqlite", seed)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE); PRAGMA user_version=2;"); err != nil {
						t.Fatal(err)
					}
					db.Close()
					settings.InstallationID = model.NewID()
					destinationID = settings.InstallationID
					if err = s.SaveSettings(settings); err != nil {
						t.Fatal(err)
					}
					for _, suffix := range []string{"", "-wal", "-shm"} {
						data, err := os.ReadFile(seed + suffix)
						if err != nil {
							t.Fatal(err)
						}
						if err = os.WriteFile(target+suffix, data, 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				s.Close()
				if condition == "newer schema" || condition == "invalid settings" {
					db, err := sql.Open("sqlite", target)
					if err != nil {
						t.Fatal(err)
					}
					mutation := "PRAGMA user_version=2"
					if condition == "invalid settings" {
						mutation = "UPDATE settings SET data='invalid-json'"
						destinationID = ""
					}
					if _, err = db.Exec(mutation); err != nil {
						t.Fatal(err)
					}
					db.Close()
				}
			}
			if condition == "corrupt with sidecars" || condition == "orphan sidecars" {
				for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
					if condition == "orphan sidecars" && suffix == "" {
						continue
					}
					if err := os.WriteFile(target+suffix, []byte("synthetic invalid SQLite bytes"+suffix), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := map[string][]byte{}
			for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
				data, err := os.ReadFile(target + suffix)
				if err == nil {
					before[suffix] = data
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
			if err := Restore(dir, archive); err != nil {
				t.Fatalf("valid backup cannot recover destination: %v", err)
			}
			copies, err := filepath.Glob(filepath.Join(dir, "before-restore-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(before) == 0 {
				if len(copies) != 0 {
					t.Fatal("new installation produced a recovery copy")
				}
			} else {
				if len(copies) != 1 {
					t.Fatalf("expected one recovery directory, got %d", len(copies))
				}
				info, err := os.Stat(copies[0])
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatalf("recovery directory is not private: %v", err)
				}
				for suffix, want := range before {
					data, err := os.ReadFile(filepath.Join(copies[0], "dashboard.sqlite"+suffix))
					if err != nil || (suffix != "-shm" && !bytes.Equal(data, want)) {
						t.Fatalf("previous database file %q was not preserved: %v", suffix, err)
					}
				}
			}
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				if _, err := os.Stat(target + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("old SQLite sidecar %q remains at the restored path", suffix)
				}
			}
			s, err := store.Open(target)
			if err != nil {
				t.Fatal(err)
			}
			settings, err := s.Settings()
			if err != nil || (destinationID != "" && settings.InstallationID != destinationID) || settings.InstallationID == archiveID {
				t.Fatalf("restore lost local identity or adopted imported identity: %+v %v", settings, err)
			}
			if decoded, err := hex.DecodeString(settings.InstallationID); err != nil || len(decoded) != 16 {
				t.Fatal("restore generated an invalid credential namespace")
			}
			credentials, calls := refreshRestoredSelection(t, s)
			s.Close()
			if credentials.reads != 0 || calls != 0 {
				t.Fatal("recovery reused a credential or contacted a provider")
			}
		})
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

func TestRestoreRejectsUnknownFindingFields(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{"raw response", `UPDATE findings SET data=json_set(data,'$.raw_response',json('{"secret":"synthetic-sensitive-value"}'))`},
		{"source snippet", `UPDATE findings SET data=json_set(data,'$.source_snippet','synthetic-sensitive-value')`},
		{"literal secret", `UPDATE findings SET data=json_set(data,'$.secret','synthetic-sensitive-value')`},
		{"sensitive field name", `UPDATE findings SET data=json_set(data,'$.synthetic-sensitive-key','synthetic-sensitive-value')`},
		{"escaped unknown field", `UPDATE findings SET data=substr(data,1,length(data)-1)||',"se\u0063ret":"synthetic-sensitive-value"}'`},
		{"null", `UPDATE findings SET data='null'`},
		{"malformed", `UPDATE findings SET data='{'`},
		{"trailing document", `UPDATE findings SET data=data||' {}'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := restoreSecurityArchive(t, tc.mutation)
			dir, identity := restoreSecurityDestination(t)
			path := filepath.Join(dir, "dashboard.sqlite")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			err = Restore(dir, archive)
			if err == nil {
				t.Fatal("finding JSON outside the supported schema was installed")
			}
			if strings.Contains(err.Error(), "synthetic-sensitive") {
				t.Fatal("validation error exposed imported finding data")
			}
			after, readErr := os.ReadFile(path)
			settings, settingsErr := store.ReadSettings(path)
			copies, globErr := filepath.Glob(filepath.Join(dir, "before-restore-*"))
			extracted, extractErr := filepath.Glob(filepath.Join(dir, "restore-*"))
			if readErr != nil || settingsErr != nil || globErr != nil || extractErr != nil || !bytes.Equal(before, after) || settings.InstallationID != identity || len(copies) != 0 || len(extracted) != 0 {
				t.Fatal("invalid finding JSON changed the destination or left import files")
			}
		})
	}
}

func TestRestorePreservesSupportedFindingFields(t *testing.T) {
	const metadata = `UPDATE findings SET data=json_set(data,
		'$.title','Synthetic finding','$.native_severity','CRITICAL',
		'$.native_resolution','not_applicable','$.diagnostic_severity','warning',
		'$.url','https://attacker.example/example/synthetic/-/security/vulnerabilities/1',
		'$.package','example/module','$.identifiers',json('["CVE-2099-0001"]'),
		'$.scanner','synthetic-scanner','$.scope','main','$.commit','synthetic-commit');`
	for _, tc := range []struct{ name, mutation string }{
		{"canonical", ""},
		{"case alias", `UPDATE findings SET data=json_set(data,'$.TITLE','Synthetic finding')`},
		{"escaped known field", `UPDATE findings SET data=substr(data,1,length(data)-1)||',"ti\u0074le":"Synthetic finding"}'`},
		{"whitespace", "UPDATE findings SET data=' '||data||char(10)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := restoreSecurityArchive(t, metadata+tc.mutation)
			dir, identity := restoreSecurityDestination(t)
			path := filepath.Join(dir, "dashboard.sqlite")
			if err := Restore(dir, archive); err != nil {
				t.Fatal("supported finding JSON was rejected", err)
			}
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			settings, settingsErr := s.Settings()
			repos, repoErr := s.Repositories()
			connections, connectionErr := s.Connections()
			findings, findingErr := s.Findings()
			if settingsErr != nil || repoErr != nil || connectionErr != nil || findingErr != nil || settings.InstallationID != identity || len(repos) != 1 || !repos[0].Selected || len(connections) != 1 || !connections[0].ReconnectRequired || len(findings) != 1 {
				t.Fatal("valid restore lost identity, selection, quarantine or findings")
			}
			f := findings[0]
			if f.ID != "synthetic-finding" || f.RepositoryID != repos[0].ID || f.Source != "fixture" || f.Category != "code" || f.Title != "Synthetic finding" || f.State != "open" || f.Severity != "critical" || f.NativeState != "DETECTED" || f.NativeSeverity != "CRITICAL" || f.NativeResolution != "not_applicable" || f.DiagnosticSeverity != "warning" || f.URL != "https://attacker.example/example/synthetic/-/security/vulnerabilities/1" || f.Package != "example/module" || !reflect.DeepEqual(f.Identifiers, []string{"CVE-2099-0001"}) || f.Scanner != "synthetic-scanner" || f.Scope != "main" || f.Commit != "synthetic-commit" || f.SeenAt.IsZero() || f.Missing {
				t.Fatal("supported finding fields changed during restore")
			}
			var exported bytes.Buffer
			if err = Export(t.Context(), s, &exported, "test"); err != nil {
				t.Fatal(err)
			}
			archive = filepath.Join(t.TempDir(), "roundtrip.tar.gz")
			if err = os.WriteFile(archive, exported.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			roundtrip := t.TempDir()
			if err = Restore(roundtrip, archive); err != nil {
				t.Fatal("subsequent backup is not restorable", err)
			}
			restored, err := store.Open(filepath.Join(roundtrip, "dashboard.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			again, err := restored.Findings()
			if err != nil || !reflect.DeepEqual(findings, again) {
				t.Fatal("subsequent backup lost supported finding metadata", err)
			}
			credentials, calls := refreshRestoredSelection(t, s)
			if credentials.reads != 0 || calls != 0 {
				t.Fatal("restored finding bypassed credential quarantine")
			}
		})
	}
}

func TestRestoreValidatesRepositoryMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, id, nameValue, provider string
		valid                         bool
	}{
		{"GitLab", "42", "example/synthetic", "gitlab", true},
		{"maximum ID", "9223372036854775807", "example/synthetic", "gitlab", true},
		{"empty ID", "", "example/synthetic", "gitlab", false},
		{"nonnumeric ID", "invalid", "example/synthetic", "gitlab", false},
		{"zero ID", "0", "example/synthetic", "gitlab", false},
		{"negative ID", "-1", "example/synthetic", "gitlab", false},
		{"overflow ID", "9223372036854775808", "example/synthetic", "gitlab", false},
		{"signed ID", "+42", "example/synthetic", "gitlab", false},
		{"zero-padded ID", "042", "example/synthetic", "gitlab", false},
		{"whitespace ID", " 42", "example/synthetic", "gitlab", false},
		{"empty name", "42", "", "gitlab", false},
		{"blank name", "42", "  ", "gitlab", false},
		{"provider mismatch", "42", "example/synthetic", "github", false},
		{"missing provider", "42", "example/synthetic", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep indexed and JSON identities consistent: the invalid metadata
			// must be rejected independently of the existing projection checks.
			mutation := fmt.Sprintf(`UPDATE repositories SET external_id='%s',data=json_set(data,'$.external_id','%s','$.name','%s','$.provider','%s');`, tc.id, tc.id, tc.nameValue, tc.provider)
			archive := restoreSecurityArchive(t, mutation)
			dir, identity := restoreSecurityDestination(t)
			path := filepath.Join(dir, "dashboard.sqlite")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			err = Restore(dir, archive)
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid repository metadata was installed")
				}
				after, readErr := os.ReadFile(path)
				settings, settingsErr := store.ReadSettings(path)
				copies, globErr := filepath.Glob(filepath.Join(dir, "before-restore-*"))
				if readErr != nil || settingsErr != nil || globErr != nil || !bytes.Equal(before, after) || settings.InstallationID != identity || len(copies) != 0 {
					t.Fatal("invalid repository metadata reached destination replacement")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			repos, repoErr := s.Repositories()
			findings, findingErr := s.Findings()
			if repoErr != nil || findingErr != nil || len(repos) != 1 || repos[0].ExternalID != tc.id || !repos[0].Selected || len(findings) != 1 {
				t.Fatal("valid restore lost repository identity, selection or findings")
			}
			c, err := s.Connection(repos[0].ConnectionID)
			if err != nil || !c.ReconnectRequired {
				t.Fatal("valid restore lost quarantine", err)
			}
			if err = s.SaveDiscovered(c, []model.Repository{{ExternalID: tc.id, Name: "example/renamed"}}); err != nil {
				t.Fatal("valid restored repository blocked rediscovery", err)
			}
			credentials, calls := refreshRestoredSelection(t, s)
			if credentials.reads != 0 || calls != 0 {
				t.Fatal("restored repository bypassed credential quarantine")
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
