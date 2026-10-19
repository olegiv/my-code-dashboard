// SPDX-License-Identifier: GPL-3.0-only
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"slices"

	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/provider"
)

type schemaObject struct {
	kind, name, table string
	definition        sql.NullString
}

// Inspect metadata before querying imported tables: views, triggers and altered
// constraints must never get authority over validation or quarantine writes.
func validateBackupSchema(db *sql.DB) error {
	trusted, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer trusted.Close()
	trusted.SetMaxOpenConns(1)
	if _, err = trusted.Exec(schema); err != nil {
		return err
	}
	expected, err := readSchema(trusted)
	if err != nil {
		return err
	}
	actual, err := readSchema(db)
	if err != nil {
		return err
	}
	if !slices.Equal(actual, expected) {
		return errors.New("backup has an unsupported database schema")
	}
	return nil
}

func readSchema(db *sql.DB) ([]schemaObject, error) {
	rows, err := db.Query("SELECT type,name,tbl_name,sql FROM main.sqlite_schema ORDER BY type,name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []schemaObject
	for rows.Next() {
		var object schemaObject
		if err = rows.Scan(&object.kind, &object.name, &object.table, &object.definition); err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

func validateBackupConnections(db *sql.DB) (map[string]model.Connection, error) {
	rows, err := db.Query("SELECT id,data FROM connections")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	connections := map[string]model.Connection{}
	for rows.Next() {
		var id, data any
		if err = rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		// Scan into native values: converting a BLOB key to string would hide
		// that text-key upserts do not update that same SQLite row.
		key, textKey := id.(string)
		raw, textData := data.(string)
		var connection model.Connection
		if !textKey || !textData || key == "" || json.Unmarshal([]byte(raw), &connection) != nil || connection.ID != key || connections[connection.ID].ID != "" {
			return nil, errors.New("backup has inconsistent connection identities")
		}
		connections[connection.ID] = connection
	}
	return connections, rows.Err()
}

func validBackupProviderURL(raw string, connection model.Connection) bool {
	if raw == "" {
		return true
	}
	base := connection.BaseURL
	switch connection.Provider {
	case "github":
		base = "https://github.com"
	case "gitlab":
	default:
		return false
	}
	origin, err := url.Parse(base)
	return err == nil && origin.Scheme == "https" && origin.Hostname() != "" && origin.User == nil && provider.SafeURL(raw, base) != ""
}

// Decode every row, including history outside the UI's paginated read limits.
// The queries and projections are fixed application definitions from ValidateBackup.
func validateBackupRecords[T any](db *sql.DB, query string, relational func(T) []any) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for i := range values {
		destinations[i] = &values[i]
	}
	for rows.Next() {
		if err = rows.Scan(destinations...); err != nil {
			return err
		}
		raw, text := values[0].(string)
		var record *T
		if !text || json.Unmarshal([]byte(raw), &record) != nil || record == nil {
			return errors.New("backup has invalid application records")
		}
		expected := relational(*record)
		if len(expected) == 0 || expected[0] == "" || !slices.Equal(expected, values[1:]) {
			return errors.New("backup has inconsistent application identities or indexed fields")
		}
	}
	return rows.Err()
}
