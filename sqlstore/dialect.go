package sqlstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/danielgtmn/ablage"
)

// Dialect is the SQL flavour of a database.
type Dialect struct{ d dialect }

// The supported dialects.
var (
	// SQLite, e.g. with modernc.org/sqlite or github.com/mattn/go-sqlite3.
	// Foreign keys must be on (modernc: _pragma=foreign_keys(1)). Times are
	// stored as INTEGER microseconds since 1970 (UTC), bools as 0/1, JSON as
	// TEXT.
	SQLite = Dialect{sqliteDialect{}}
	// Postgres, e.g. with github.com/jackc/pgx/v5/stdlib. Strings use the
	// collation "C" (byte order), so sorting and comparing is the same as in
	// SQLite and memory; JSON is stored as JSONB.
	Postgres = Dialect{postgresDialect{}}
)

func (d Dialect) String() string {
	if d.d == nil {
		return "none"
	}
	return d.d.name()
}

type dialect interface {
	name() string
	placeholder(n int) string // n starts at 1
	columnType(t ablage.Type) string
	zero(t ablage.Type) string // SQL literal of the zero value
	falseLit() string
	ne(col, ph string) string // col differs from ph, NULL included
	nullsOrder(desc bool) string
	noLimit() string
	// selectExpr is how a column is read.
	selectExpr(col string, t ablage.Type) string
	encode(t ablage.Type, v any) any
	decode(t ablage.Type, v any) (any, error)
	columnsQuery() string // one argument: the table name
	indexesQuery() string // one argument: the table name
	// errKind maps a driver error to ablage.ErrDuplicate or
	// ablage.ErrReference, or nil.
	errKind(err error) error
}

type sqliteDialect struct{}

func (sqliteDialect) name() string           { return "sqlite" }
func (sqliteDialect) placeholder(int) string { return "?" }
func (sqliteDialect) falseLit() string       { return "0" }
func (sqliteDialect) noLimit() string        { return "-1" }
func (sqliteDialect) ne(col, ph string) string {
	return col + " IS NOT " + ph
}
func (sqliteDialect) nullsOrder(desc bool) string {
	if desc {
		return " DESC NULLS LAST"
	}
	return " ASC NULLS FIRST"
}
func (sqliteDialect) columnsQuery() string                        { return `SELECT name FROM pragma_table_info(?)` }
func (sqliteDialect) indexesQuery() string                        { return `SELECT name FROM pragma_index_list(?)` }
func (sqliteDialect) selectExpr(col string, _ ablage.Type) string { return col }

func (sqliteDialect) columnType(t ablage.Type) string {
	switch t {
	case ablage.String, ablage.JSON:
		return "TEXT"
	case ablage.Float:
		return "REAL"
	case ablage.Bytes:
		return "BLOB"
	}
	return "INTEGER"
}

func (sqliteDialect) zero(t ablage.Type) string {
	switch t {
	case ablage.String:
		return "''"
	case ablage.Float:
		return "0.0"
	case ablage.Time:
		return fmt.Sprint(time.Time{}.UnixMicro())
	case ablage.Bytes:
		return "x''"
	case ablage.JSON:
		return "'null'"
	}
	return "0"
}

func (sqliteDialect) encode(t ablage.Type, v any) any {
	switch x := v.(type) {
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	case time.Time:
		return x.UnixMicro()
	case json.RawMessage:
		return string(x)
	}
	return v
}

func (sqliteDialect) decode(t ablage.Type, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch t {
	case ablage.String:
		switch x := v.(type) {
		case string:
			return x, nil
		case []byte:
			return string(x), nil
		}
	case ablage.JSON:
		switch x := v.(type) {
		case string:
			return json.RawMessage(x), nil
		case []byte:
			return json.RawMessage(append([]byte{}, x...)), nil
		}
	case ablage.Bytes:
		switch x := v.(type) {
		case []byte:
			return append([]byte{}, x...), nil
		case string:
			return []byte(x), nil
		}
	case ablage.Float:
		switch x := v.(type) {
		case float64:
			return x, nil
		case int64:
			return float64(x), nil
		}
	case ablage.Int, ablage.Bool, ablage.Time:
		n, ok := v.(int64)
		if !ok {
			break
		}
		switch t {
		case ablage.Bool:
			return n != 0, nil
		case ablage.Time:
			return time.UnixMicro(n).UTC(), nil
		}
		return n, nil
	}
	return nil, fmt.Errorf("sqlstore: cannot read %T as %s", v, t)
}

// SQLite extended result codes.
const (
	sqliteConstraintForeignKey = 787
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
)

func (sqliteDialect) errKind(err error) error {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		switch coded.Code() {
		case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
			return ablage.ErrDuplicate
		case sqliteConstraintForeignKey:
			return ablage.ErrReference
		}
	}
	// Drivers without a Code method (e.g. mattn/go-sqlite3) still say so.
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return ablage.ErrDuplicate
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		return ablage.ErrReference
	}
	return nil
}

type postgresDialect struct{}

func (postgresDialect) name() string             { return "postgres" }
func (postgresDialect) placeholder(n int) string { return fmt.Sprintf("$%d", n) }
func (postgresDialect) falseLit() string         { return "FALSE" }
func (postgresDialect) noLimit() string          { return "ALL" }
func (postgresDialect) ne(col, ph string) string {
	return col + " IS DISTINCT FROM " + ph
}
func (postgresDialect) nullsOrder(desc bool) string {
	if desc {
		return " DESC NULLS LAST"
	}
	return " ASC NULLS FIRST"
}
func (postgresDialect) columnsQuery() string {
	return `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1`
}

func (postgresDialect) indexesQuery() string {
	return `SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename = $1`
}

func (postgresDialect) selectExpr(col string, t ablage.Type) string {
	if t == ablage.JSON {
		return col + "::text"
	}
	return col
}

func (postgresDialect) columnType(t ablage.Type) string {
	switch t {
	case ablage.String:
		return `TEXT COLLATE "C"`
	case ablage.Int:
		return "BIGINT"
	case ablage.Float:
		return "DOUBLE PRECISION"
	case ablage.Bool:
		return "BOOLEAN"
	case ablage.Time:
		return "TIMESTAMPTZ"
	case ablage.Bytes:
		return "BYTEA"
	case ablage.JSON:
		return "JSONB"
	}
	return "TEXT"
}

func (postgresDialect) zero(t ablage.Type) string {
	switch t {
	case ablage.String:
		return "''"
	case ablage.Float:
		return "0"
	case ablage.Bool:
		return "FALSE"
	case ablage.Time:
		return "'0001-01-01 00:00:00+00'"
	case ablage.Bytes:
		return `'\x'::bytea`
	case ablage.JSON:
		return "'null'::jsonb"
	}
	return "0"
}

func (postgresDialect) encode(t ablage.Type, v any) any {
	if x, ok := v.(json.RawMessage); ok {
		return string(x)
	}
	return v
}

func (postgresDialect) decode(t ablage.Type, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch t {
	case ablage.String:
		switch x := v.(type) {
		case string:
			return x, nil
		case []byte:
			return string(x), nil
		}
	case ablage.JSON:
		switch x := v.(type) {
		case string:
			return json.RawMessage(x), nil
		case []byte:
			return json.RawMessage(append([]byte{}, x...)), nil
		}
	case ablage.Bytes:
		if x, ok := v.([]byte); ok {
			return append([]byte{}, x...), nil
		}
	case ablage.Int:
		switch n := v.(type) {
		case int64:
			return n, nil
		case int32:
			return int64(n), nil
		}
	case ablage.Float:
		switch n := v.(type) {
		case float64:
			return n, nil
		case float32:
			return float64(n), nil
		}
	case ablage.Bool:
		if b, ok := v.(bool); ok {
			return b, nil
		}
	case ablage.Time:
		if tm, ok := v.(time.Time); ok {
			return tm.UTC(), nil
		}
	}
	return nil, fmt.Errorf("sqlstore: cannot read %T as %s", v, t)
}

func (postgresDialect) errKind(err error) error {
	// pgx (*pgconn.PgError) and lib/pq (*pq.Error) both report SQLSTATE.
	var coded interface{ SQLState() string }
	state := ""
	if errors.As(err, &coded) {
		state = coded.SQLState()
	} else if i := strings.Index(err.Error(), "SQLSTATE "); i >= 0 {
		state = strings.TrimRight(err.Error()[i+9:], ")")
	}
	switch {
	case strings.HasPrefix(state, "23505"):
		return ablage.ErrDuplicate
	case strings.HasPrefix(state, "23503"):
		return ablage.ErrReference
	}
	return nil
}
