// Package testdb opens the databases the tests of this module run against.
// It is the only package that imports database drivers, and only tests
// import it.
package testdb

import (
	"crypto/rand"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx"
	_ "modernc.org/sqlite"             // registers "sqlite"

	"github.com/danielgtmn/ablage"
	"github.com/danielgtmn/ablage/memstore"
	"github.com/danielgtmn/ablage/sqlstore"
)

// PostgresEnv names the variable holding a Postgres DSN for the tests, e.g.
// postgres://postgres@localhost:55432/postgres?sslmode=disable. Without it
// the Postgres tests skip.
const PostgresEnv = "ABLAGE_TEST_POSTGRES"

// Backend opens an empty store for a test.
type Backend struct {
	Name string
	Open func(t *testing.T) ablage.Store
}

// All returns every backend: memory, SQLite (pool and one connection) and
// Postgres (skips without PostgresEnv).
func All() []Backend {
	return []Backend{
		{"Memory", func(*testing.T) ablage.Store { return memstore.New() }},
		{"SQLite", func(t *testing.T) ablage.Store { return Store(t, SQLite(t), sqlstore.SQLite) }},
		{"SQLiteOneConnection", func(t *testing.T) ablage.Store {
			db := SQLite(t)
			db.SetMaxOpenConns(1)
			return Store(t, db, sqlstore.SQLite)
		}},
		{"Postgres", func(t *testing.T) ablage.Store { return Store(t, Postgres(t), sqlstore.Postgres) }},
	}
}

// Store wraps db in a sqlstore.
func Store(t *testing.T, db *sql.DB, d sqlstore.Dialect, opts ...sqlstore.Option) *sqlstore.Store {
	t.Helper()
	s, err := sqlstore.New(db, d, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// SQLiteDSN is how apps should open SQLite with modernc.org/sqlite.
func SQLiteDSN(file string) string {
	return file + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_txlock=immediate"
}

// SQLite opens an empty SQLite database in a temporary file.
func SQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", SQLiteDSN(filepath.Join(t.TempDir(), "test.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Postgres opens a connection whose search_path is a new schema, dropped
// after the test. It skips the test without PostgresEnv.
func Postgres(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(PostgresEnv)
	if dsn == "" {
		t.Skip(PostgresEnv + " not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	name := "ablage_test_" + strings.ToLower(rand.Text()[:10])
	if _, err := admin.Exec("CREATE SCHEMA " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA " + name + " CASCADE") })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
