package sqlstore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ablage"
	"github.com/danielgtmn/ablage/internal/testdb"
	"github.com/danielgtmn/ablage/sqlstore"
)

var ctx = context.Background()

type db struct {
	name    string
	open    func(t *testing.T) *sql.DB
	dialect sqlstore.Dialect
}

var dbs = []db{
	{"SQLite", testdb.SQLite, sqlstore.SQLite},
	{"Postgres", testdb.Postgres, sqlstore.Postgres},
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// An app ships a new version with more fields and indexes: Migrate adds
// them and the old rows get the zero value or NULL.
func TestMigrateAddsFields(t *testing.T) {
	for _, d := range dbs {
		t.Run(d.name, func(t *testing.T) {
			conn := d.open(t)
			v1 := ablage.Table{Name: "items", Fields: []ablage.Field{{Name: "name", Type: ablage.String}}}
			s1 := testdb.Store(t, conn, d.dialect, sqlstore.WithPrefix("app_"))
			must(t, s1.Declare(v1))
			must(t, s1.Migrate(ctx))
			must(t, s1.Insert(ctx, "items", ablage.Row{"id": "old", "name": "x"}))

			owners := ablage.Table{Name: "owners"}
			v2 := ablage.Table{Name: "items", Fields: []ablage.Field{
				{Name: "name", Type: ablage.String, Unique: true},
				{Name: "count", Type: ablage.Int},
				{Name: "seen", Type: ablage.Time},
				{Name: "data", Type: ablage.JSON},
				{Name: "blob", Type: ablage.Bytes},
				{Name: "flag", Type: ablage.Bool},
				{Name: "ratio", Type: ablage.Float},
				{Name: "note", Type: ablage.String, Nullable: true},
				{Name: "owner", Type: ablage.String, Nullable: true, Ref: "owners"},
			}}
			s2 := testdb.Store(t, conn, d.dialect, sqlstore.WithPrefix("app_"))
			must(t, s2.Declare(v2, owners))
			must(t, s2.Migrate(ctx))
			must(t, s2.Migrate(ctx))
			rows, err := s2.Find(ctx, "items", ablage.Query{})
			must(t, err)
			r := rows[0]
			if r["name"] != "x" || r["count"] != int64(0) || !r["seen"].(time.Time).IsZero() ||
				string(r["data"].(json.RawMessage)) != "null" || len(r["blob"].([]byte)) != 0 || r["flag"] != false ||
				r["ratio"] != 0.0 || r["note"] != nil || r["owner"] != nil {
				t.Errorf("old row after migration: %#v", r)
			}
			must(t, s2.Insert(ctx, "items", ablage.Row{"id": "new", "name": "y"}))
			if err := s2.Insert(ctx, "items", ablage.Row{"id": "dup", "name": "y"}); err == nil {
				t.Error("unique index on the existing table missing")
			}

			// A required reference cannot be added to an existing table.
			v3 := v2
			v3.Fields = append(append([]ablage.Field(nil), v2.Fields...), ablage.Field{Name: "must", Type: ablage.String, Ref: "owners"})
			s3 := testdb.Store(t, conn, d.dialect, sqlstore.WithPrefix("app_"))
			must(t, s3.Declare(v3, owners))
			if err := s3.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "nullable") {
				t.Errorf("adding a required reference: %v", err)
			}
		})
	}
}

func TestPrefixKeepsLibrariesApart(t *testing.T) {
	for _, d := range dbs {
		t.Run(d.name, func(t *testing.T) {
			conn := d.open(t)
			tb := ablage.Table{Name: "users", Fields: []ablage.Field{{Name: "name", Type: ablage.String}}}
			a := testdb.Store(t, conn, d.dialect, sqlstore.WithPrefix("a_"))
			b := testdb.Store(t, conn, d.dialect, sqlstore.WithPrefix("b_"))
			for _, s := range []*sqlstore.Store{a, b} {
				must(t, s.Declare(tb))
				must(t, s.Migrate(ctx))
			}
			must(t, a.Insert(ctx, "users", ablage.Row{"id": "1", "name": "a"}))
			if n, _ := b.Count(ctx, "users", ablage.All); n != 0 {
				t.Errorf("b sees a's rows: %d", n)
			}
		})
	}
	if _, err := sqlstore.New(nil, sqlstore.Postgres, sqlstore.WithPrefix("Bad-")); err == nil {
		t.Error("invalid prefix accepted")
	}
}

func TestSQLiteNeedsForeignKeys(t *testing.T) {
	conn, err := sql.Open("sqlite", t.TempDir()+"/x.db")
	must(t, err)
	defer conn.Close()
	if _, err := sqlstore.New(conn, sqlstore.SQLite); err == nil || !strings.Contains(err.Error(), "foreign keys") {
		t.Errorf("foreign keys off: %v", err)
	}
}

func TestDDL(t *testing.T) {
	long := strings.Repeat("x", 40)
	tables := []ablage.Table{
		{Name: "users", Fields: []ablage.Field{
			{Name: "email", Type: ablage.String, Unique: true},
			{Name: "deleted_at", Type: ablage.Time, Nullable: true},
			{Name: long, Type: ablage.Int, Index: true},
		}, SoftDelete: "deleted_at"},
		{Name: "posts", Fields: []ablage.Field{
			{Name: "author", Type: ablage.String, Ref: "users", OnDelete: ablage.Cascade},
			{Name: "body", Type: ablage.JSON},
		}},
	}
	for _, d := range dbs {
		var s *sqlstore.Store
		if d.dialect == sqlstore.SQLite {
			// New checks foreign keys on a real connection.
			s = testdb.Store(t, testdb.SQLite(t), d.dialect)
		} else {
			var err error
			s, err = sqlstore.New(nil, d.dialect)
			must(t, err)
		}
		must(t, s.Declare(tables[1], tables[0]))
		stmts, err := s.DDL()
		must(t, err)
		all := strings.Join(stmts, ";\n")
		for _, want := range []string{
			`CREATE TABLE "users"`, `CREATE TABLE "posts"`,
			`REFERENCES "users" ("id") ON DELETE CASCADE`,
			`WHERE "deleted_at" IS NULL`,
		} {
			if !strings.Contains(all, want) {
				t.Errorf("%s DDL lacks %q:\n%s", d.name, want, all)
			}
		}
		if strings.Index(all, `CREATE TABLE "users"`) > strings.Index(all, `CREATE TABLE "posts"`) {
			t.Errorf("%s: posts created before users", d.name)
		}
		for _, line := range stmts {
			if i := strings.Index(line, `INDEX IF NOT EXISTS "`); i >= 0 {
				name := line[i+len(`INDEX IF NOT EXISTS "`):]
				name = name[:strings.Index(name, `"`)]
				if len(name) > 63 {
					t.Errorf("index name longer than 63 bytes: %s", name)
				}
			}
		}
	}
}

// Adding soft delete to an existing table replaces its unique indexes by
// ones over live rows only.
func TestMigrateAddsSoftDelete(t *testing.T) {
	for _, d := range dbs {
		t.Run(d.name, func(t *testing.T) {
			conn := d.open(t)
			v1 := ablage.Table{Name: "users", Fields: []ablage.Field{{Name: "name", Type: ablage.String, Unique: true}}}
			s1 := testdb.Store(t, conn, d.dialect)
			must(t, s1.Declare(v1))
			must(t, s1.Migrate(ctx))
			must(t, s1.Insert(ctx, "users", ablage.Row{"id": "a", "name": "x"}))

			v2 := v1
			v2.Fields = append(append([]ablage.Field(nil), v1.Fields...), ablage.Field{Name: "deleted_at", Type: ablage.Time, Nullable: true})
			v2.SoftDelete = "deleted_at"
			s2 := testdb.Store(t, conn, d.dialect)
			must(t, s2.Declare(v2))
			must(t, s2.Migrate(ctx))
			_, err := s2.Update(ctx, "users", ablage.Row{"deleted_at": time.Now()}, ablage.Eq("id", "a"))
			must(t, err)
			must(t, s2.Insert(ctx, "users", ablage.Row{"id": "b", "name": "x"}))
			if err := s2.Insert(ctx, "users", ablage.Row{"id": "c", "name": "x"}); err == nil {
				t.Error("uniqueness among live rows lost")
			}
		})
	}
}
