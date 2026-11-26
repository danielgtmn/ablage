package ablage

import (
	"bytes"
	"cmp"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// Row is one row of a table, keyed by field name. Rows returned by a store
// hold every column, each value in the canonical Go type of its field (see
// Type) or nil for NULL.
type Row map[string]any

// Incr adds to an Int field in Store.Update and the update part of
// Store.Upsert, as one atomic step in the database.
type Incr int64

var timeType = reflect.TypeFor[time.Time]()

// NormalizeTime is how times are stored: UTC, rounded down to microseconds,
// without a monotonic clock reading.
func NormalizeTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond).Round(0) }

// Value converts v to the canonical type of the field. It accepts the
// canonical type, named types with the same underlying kind, any integer for
// Int and Float, pointers (nil is NULL), encoding.TextMarshaler for String,
// and for JSON a json.RawMessage, []byte or string holding JSON. A nil for a
// field that is not nullable is an error unless nullOK.
func (f Field) Value(v any, nullOK bool) (any, error) {
	if v == nil {
		if !nullOK && !f.Nullable {
			return nil, fmt.Errorf("ablage: field %s must not be NULL", f.Name)
		}
		return nil, nil
	}
	out, err := convert(f.Type, v)
	if err != nil {
		return nil, fmt.Errorf("ablage: field %s: %w", f.Name, err)
	}
	if out == nil && !nullOK && !f.Nullable {
		return nil, fmt.Errorf("ablage: field %s must not be NULL", f.Name)
	}
	return out, nil
}

// convert turns v into the canonical type and refuses values that not
// every store can keep the same way.
func convert(t Type, v any) (any, error) {
	out, err := convertKind(t, v)
	if err != nil || out == nil {
		return out, err
	}
	switch x := out.(type) {
	case string:
		if !utf8.ValidString(x) {
			return nil, errors.New("not valid UTF-8 (use a bytes field for binary data)")
		}
		if strings.IndexByte(x, 0) >= 0 {
			return nil, errors.New("contains a NUL character")
		}
	case float64:
		if math.IsNaN(x) {
			return nil, errors.New("NaN cannot be stored")
		}
	case time.Time:
		if y := x.Year(); y < 1 || y > 9999 {
			return nil, fmt.Errorf("year %d is outside 1 to 9999", y)
		}
	}
	return out, nil
}

func convertKind(t Type, v any) (any, error) {
	switch x := v.(type) {
	case string:
		switch t {
		case String:
			return x, nil
		case JSON:
			return checkJSON([]byte(x))
		}
	case int64:
		switch t {
		case Int:
			return x, nil
		case Float:
			return float64(x), nil
		}
	case float64:
		if t == Float {
			return x, nil
		}
	case bool:
		if t == Bool {
			return x, nil
		}
	case time.Time:
		if t == Time {
			return NormalizeTime(x), nil
		}
	case json.RawMessage:
		if t == JSON {
			return checkJSON(x)
		}
	case []byte:
		switch t {
		case Bytes:
			return append([]byte{}, x...), nil
		case JSON:
			return checkJSON(x)
		}
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil
		}
		return convertKind(t, rv.Elem().Interface())
	}
	if t == String {
		if m, ok := v.(encoding.TextMarshaler); ok && rv.Type() != timeType {
			b, err := m.MarshalText()
			if err != nil {
				return nil, err
			}
			return string(b), nil
		}
	}
	switch rv.Kind() {
	case reflect.String:
		if t == String {
			return rv.String(), nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch t {
		case Int:
			return rv.Int(), nil
		case Float:
			return float64(rv.Int()), nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if rv.Uint() > math.MaxInt64 {
			return nil, fmt.Errorf("%d does not fit into an int64", rv.Uint())
		}
		switch t {
		case Int:
			return int64(rv.Uint()), nil
		case Float:
			return float64(rv.Uint()), nil
		}
	case reflect.Float32, reflect.Float64:
		if t == Float {
			return rv.Float(), nil
		}
	case reflect.Bool:
		if t == Bool {
			return rv.Bool(), nil
		}
	case reflect.Slice:
		if t == Bytes && rv.Type().Elem().Kind() == reflect.Uint8 {
			return append([]byte{}, rv.Bytes()...), nil
		}
	}
	return nil, fmt.Errorf("is %s, got %T", t, v)
}

// checkJSON accepts valid JSON without NUL characters in strings (which
// Postgres cannot store in JSONB).
func checkJSON(b []byte) (any, error) {
	if !json.Valid(b) {
		return nil, errors.New("not valid JSON")
	}
	if bytes.Contains(b, []byte(`\u0000`)) {
		dec := json.NewDecoder(bytes.NewReader(b))
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if s, ok := tok.(string); ok && strings.IndexByte(s, 0) >= 0 {
				return nil, errors.New("JSON contains a NUL character")
			}
		}
	}
	return json.RawMessage(bytes.Clone(b)), nil
}

// Zero returns the zero value of a field that is not nullable, which it
// holds when a row is inserted without it.
func (f Field) Zero() any {
	switch f.Type {
	case String:
		return ""
	case Int:
		return int64(0)
	case Float:
		return float64(0)
	case Bool:
		return false
	case Time:
		return time.Time{}
	case Bytes:
		return []byte{}
	case JSON:
		return json.RawMessage("null")
	}
	return nil
}

// CheckInsert validates a row for Store.Insert and returns a normalized copy
// holding every column: missing fields are NULL if nullable, else their zero
// value. The id must be a non-empty string.
func (t Table) CheckInsert(row Row) (Row, error) {
	out := make(Row, len(t.Fields)+1)
	for name, v := range row {
		f, ok := t.Field(name)
		if !ok {
			return nil, fmt.Errorf("ablage: unknown field %s.%s", t.Name, name)
		}
		if _, ok := v.(Incr); ok {
			return nil, fmt.Errorf("ablage: Incr only in updates (%s.%s)", t.Name, name)
		}
		nv, err := f.Value(v, false)
		if err != nil {
			return nil, fmt.Errorf("%w (table %s)", err, t.Name)
		}
		out[name] = nv
	}
	if id, _ := out["id"].(string); id == "" {
		return nil, fmt.Errorf("ablage: row for %s needs an id", t.Name)
	}
	for _, f := range t.Fields {
		if _, ok := out[f.Name]; !ok {
			if f.Nullable {
				out[f.Name] = nil
			} else {
				out[f.Name] = f.Zero()
			}
		}
	}
	return out, nil
}

// CheckUpdate validates the fields to set in Store.Update or the update part
// of Store.Upsert and returns a normalized copy. It must not set the id; Incr is allowed on Int fields that are not nullable.
func (t Table) CheckUpdate(set Row) (Row, error) {
	out := make(Row, len(set))
	for name, v := range set {
		f, ok := t.Field(name)
		if !ok {
			return nil, fmt.Errorf("ablage: unknown field %s.%s", t.Name, name)
		}
		if name == "id" {
			return nil, fmt.Errorf("ablage: cannot change the id of a row in %s", t.Name)
		}
		if inc, ok := v.(Incr); ok {
			if f.Type != Int || f.Nullable {
				return nil, fmt.Errorf("ablage: Incr needs an int field that is not nullable (%s.%s)", t.Name, name)
			}
			out[name] = inc
			continue
		}
		nv, err := f.Value(v, false)
		if err != nil {
			return nil, fmt.Errorf("%w (table %s)", err, t.Name)
		}
		out[name] = nv
	}
	return out, nil
}

// Compare orders two canonical values of the same type; NULL (nil) comes
// first. Strings and bytes compare byte by byte, as every store does.
func Compare(a, b any) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	switch x := a.(type) {
	case string:
		return strings.Compare(x, b.(string))
	case int64:
		return cmp.Compare(x, b.(int64))
	case float64:
		return cmp.Compare(x, b.(float64))
	case bool:
		y := b.(bool)
		switch {
		case x == y:
			return 0
		case !x:
			return -1
		}
		return 1
	case time.Time:
		return x.Compare(b.(time.Time))
	case []byte:
		return bytes.Compare(x, b.([]byte))
	case json.RawMessage:
		return bytes.Compare(x, b.(json.RawMessage))
	}
	panic(fmt.Sprintf("ablage: cannot compare %T", a))
}

// CloneValue copies a canonical value so the copy shares no memory.
func CloneValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return bytes.Clone(x)
	case json.RawMessage:
		return json.RawMessage(bytes.Clone(x))
	}
	return v
}

// Clone copies a row.
func (r Row) Clone() Row {
	if r == nil {
		return nil
	}
	out := make(Row, len(r))
	for k, v := range r {
		out[k] = CloneValue(v)
	}
	return out
}
