package ablage

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
)

// role marks fields the repository manages itself.
type role int

const (
	roleNone role = iota
	roleID
	roleVersion
	roleCreated
	roleUpdated
	roleDeleted
)

// conv says how a Go value is turned into a canonical value and back.
type conv int

const (
	convString conv = iota
	convInt
	convUint
	convFloat
	convBool
	convTime
	convBytes
	convText // encoding.TextMarshaler / TextUnmarshaler, stored as String
	convJSON
)

type column struct {
	index  []int // reflect.Value.FieldByIndex
	goName string
	field  Field
	conv   conv
	ptr    bool // the Go field is a pointer: nil is NULL
	role   role
}

type mapping struct {
	typ     reflect.Type
	cols    []column
	byName  map[string]*column
	id      *column
	version *column
	created *column
	updated *column
	deleted *column
}

var (
	textMarshaler   = reflect.TypeFor[encoding.TextMarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// newMapping reads the struct type T. Every exported field is a column,
// named by the tag `db:"name"` or else in snake_case; `db:"-"` skips a
// field. Embedded structs without a tag are flattened. Tag options after the
// name:
//
//	id          the primary key (default: the field named ID); a string
//	version     optimistic concurrency: an integer the repository increments
//	created     a time.Time set on create
//	updated     a time.Time set on every write
//	deleted     a *time.Time; enables soft delete
//	unique      a unique index
//	index       an index
//	ref=table   holds the id of a row in table
//	cascade     with ref: delete this row with the referenced one
//	setnull     with ref: set to NULL when the referenced row is deleted
//	json        store as JSON even if the type has another mapping
//
// Pointers are nullable. Strings, integers, floats, bools, time.Time and
// []byte map to their types; types implementing encoding.TextMarshaler and
// TextUnmarshaler (e.g. netip.Addr) are stored as strings; everything else
// (structs, maps, slices) as JSON.
func newMapping(t reflect.Type, table string) (*mapping, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("ablage: %s is not a struct", t)
	}
	m := &mapping{typ: t, byName: map[string]*column{}}
	if err := m.walk(t, nil); err != nil {
		return nil, err
	}
	hasID := false
	for _, c := range m.cols {
		hasID = hasID || c.role == roleID
	}
	if !hasID {
		for i := range m.cols {
			if m.cols[i].goName == "ID" && m.cols[i].role == roleNone {
				m.cols[i].role = roleID
				hasID = true
			}
		}
	}
	if !hasID {
		return nil, fmt.Errorf("ablage: %s has no id field (name it ID or tag it `db:\",id\"`)", t)
	}
	for i := range m.cols {
		c := &m.cols[i]
		if err := c.check(t); err != nil {
			return nil, err
		}
		m.byName[c.field.Name] = c
		var slot **column
		switch c.role {
		case roleID:
			slot = &m.id
		case roleVersion:
			slot = &m.version
		case roleCreated:
			slot = &m.created
		case roleUpdated:
			slot = &m.updated
		case roleDeleted:
			slot = &m.deleted
		default:
			continue
		}
		if *slot != nil {
			return nil, fmt.Errorf("ablage: %s has two fields with the same role as %s", t, c.goName)
		}
		*slot = c
	}
	if len(m.byName) != len(m.cols) {
		seen := map[string]bool{}
		for _, c := range m.cols {
			if seen[c.field.Name] {
				return nil, fmt.Errorf("ablage: %s maps two fields to the column %s", t, c.field.Name)
			}
			seen[c.field.Name] = true
		}
	}
	return m, nil
}

func (m *mapping) walk(t reflect.Type, index []int) error {
	for i := range t.NumField() {
		sf := t.Field(i)
		tag, hasTag := sf.Tag.Lookup("db")
		if tag == "-" {
			continue
		}
		idx := append(append([]int(nil), index...), i)
		if sf.Anonymous && !hasTag && sf.Type.Kind() == reflect.Struct {
			if err := m.walk(sf.Type, idx); err != nil {
				return err
			}
			continue
		}
		if !sf.IsExported() {
			continue
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "" {
			name = snakeCase(sf.Name)
		}
		c := column{index: idx, goName: sf.Name, field: Field{Name: name}}
		forceJSON := false
		for _, opt := range parts[1:] {
			key, val, _ := strings.Cut(strings.TrimSpace(opt), "=")
			setRole := func(r role) error {
				if c.role != roleNone {
					return fmt.Errorf("ablage: %s.%s has two roles", t, sf.Name)
				}
				c.role = r
				return nil
			}
			var err error
			switch key {
			case "id":
				err = setRole(roleID)
			case "version":
				err = setRole(roleVersion)
			case "created":
				err = setRole(roleCreated)
			case "updated":
				err = setRole(roleUpdated)
			case "deleted":
				err = setRole(roleDeleted)
			case "unique":
				c.field.Unique = true
			case "index":
				c.field.Index = true
			case "ref":
				c.field.Ref = val
			case "cascade":
				c.field.OnDelete = Cascade
			case "setnull":
				c.field.OnDelete = SetNull
			case "json":
				forceJSON = true
			case "":
			default:
				err = fmt.Errorf("ablage: unknown tag option %q on %s.%s", key, t, sf.Name)
			}
			if err != nil {
				return err
			}
		}
		ft := sf.Type
		if ft.Kind() == reflect.Pointer {
			c.ptr = true
			c.field.Nullable = true
			ft = ft.Elem()
		}
		if forceJSON {
			c.conv, c.field.Type = convJSON, JSON
		} else if err := c.classify(ft); err != nil {
			return fmt.Errorf("ablage: %s.%s: %w", t, sf.Name, err)
		}
		m.cols = append(m.cols, c)
	}
	return nil
}

func (c *column) classify(t reflect.Type) error {
	if t == timeType {
		c.conv, c.field.Type = convTime, Time
		return nil
	}
	if t.Implements(textMarshaler) && reflect.PointerTo(t).Implements(textUnmarshaler) {
		c.conv, c.field.Type = convText, String
		return nil
	}
	switch t.Kind() {
	case reflect.String:
		c.conv, c.field.Type = convString, String
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		c.conv, c.field.Type = convInt, Int
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		c.conv, c.field.Type = convUint, Int
	case reflect.Float32, reflect.Float64:
		c.conv, c.field.Type = convFloat, Float
	case reflect.Bool:
		c.conv, c.field.Type = convBool, Bool
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			c.conv, c.field.Type = convBytes, Bytes
			return nil
		}
		c.conv, c.field.Type = convJSON, JSON
	case reflect.Struct, reflect.Map, reflect.Array:
		c.conv, c.field.Type = convJSON, JSON
	default:
		return fmt.Errorf("cannot store a %s", t)
	}
	return nil
}

func (c *column) check(t reflect.Type) error {
	where := fmt.Sprintf("%s.%s", t, c.goName)
	switch c.role {
	case roleID:
		if c.field.Name != "id" && c.field.Name != snakeCase(c.goName) {
			return fmt.Errorf("ablage: %s: the id column is always named id", where)
		}
		c.field.Name = "id"
		if c.conv != convString || c.ptr {
			return fmt.Errorf("ablage: %s: the id must be a string", where)
		}
	case roleVersion:
		if (c.conv != convInt && c.conv != convUint) || c.ptr {
			return fmt.Errorf("ablage: %s: the version must be an integer", where)
		}
	case roleCreated, roleUpdated:
		if c.conv != convTime {
			return fmt.Errorf("ablage: %s: created and updated must be a time.Time", where)
		}
	case roleDeleted:
		if c.conv != convTime || !c.ptr {
			return fmt.Errorf("ablage: %s: deleted must be a *time.Time", where)
		}
	}
	if c.field.Name == "id" && c.role != roleID {
		return fmt.Errorf("ablage: %s: the column id is reserved for the id field", where)
	}
	return nil
}

// table returns the schema of the mapped struct.
func (m *mapping) table(name string, indexes []Index) Table {
	t := Table{Name: name, Indexes: indexes}
	for _, c := range m.cols {
		if c.role == roleID {
			continue
		}
		t.Fields = append(t.Fields, c.field)
	}
	if m.deleted != nil {
		t.SoftDelete = m.deleted.field.Name
	}
	return t
}

// row converts an entity to a row of canonical values.
func (m *mapping) row(v reflect.Value) (Row, error) {
	row := make(Row, len(m.cols))
	for i := range m.cols {
		c := &m.cols[i]
		val, err := c.get(v.FieldByIndex(c.index))
		if err != nil {
			return nil, fmt.Errorf("ablage: %s.%s: %w", m.typ, c.goName, err)
		}
		row[c.field.Name] = val
	}
	return row, nil
}

func (c *column) get(f reflect.Value) (any, error) {
	if c.ptr {
		if f.IsNil() {
			return nil, nil
		}
		f = f.Elem()
	}
	switch c.conv {
	case convString:
		return f.String(), nil
	case convInt:
		return f.Int(), nil
	case convUint:
		u := f.Uint()
		if u > 1<<63-1 {
			return nil, fmt.Errorf("%d does not fit into an int64", u)
		}
		return int64(u), nil
	case convFloat:
		return f.Float(), nil
	case convBool:
		return f.Bool(), nil
	case convTime:
		return NormalizeTime(f.Interface().(time.Time)), nil
	case convBytes:
		return append([]byte{}, f.Bytes()...), nil
	case convText:
		b, err := f.Interface().(encoding.TextMarshaler).MarshalText()
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case convJSON:
		b, err := json.Marshal(f.Interface())
		if err != nil {
			return nil, err
		}
		return json.RawMessage(b), nil
	}
	return nil, fmt.Errorf("unknown conversion")
}

// value converts a value given for a column (in Patch) the way get does for
// struct fields: typed Go values are accepted as in the struct.
func (c *column) value(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil
		}
		rv = rv.Elem()
	}
	switch c.conv {
	case convJSON:
		if raw, ok := v.(json.RawMessage); ok {
			return c.field.Value(raw, true)
		}
		b, err := json.Marshal(rv.Interface())
		if err != nil {
			return nil, err
		}
		return json.RawMessage(b), nil
	case convText:
		if s, ok := rv.Interface().(string); ok {
			return s, nil
		}
		if m, ok := rv.Interface().(encoding.TextMarshaler); ok {
			b, err := m.MarshalText()
			if err != nil {
				return nil, err
			}
			return string(b), nil
		}
	}
	return c.field.Value(rv.Interface(), true)
}

// set writes a canonical value into the struct field.
func (c *column) set(f reflect.Value, v any) error {
	if v == nil {
		f.SetZero()
		return nil
	}
	if c.ptr {
		p := reflect.New(f.Type().Elem())
		f.Set(p)
		f = p.Elem()
	}
	switch c.conv {
	case convString:
		f.SetString(v.(string))
	case convInt:
		n := v.(int64)
		if f.OverflowInt(n) {
			return fmt.Errorf("%d does not fit into a %s", n, f.Type())
		}
		f.SetInt(n)
	case convUint:
		n := v.(int64)
		if n < 0 || f.OverflowUint(uint64(n)) {
			return fmt.Errorf("%d does not fit into a %s", n, f.Type())
		}
		f.SetUint(uint64(n))
	case convFloat:
		f.SetFloat(v.(float64))
	case convBool:
		f.SetBool(v.(bool))
	case convTime:
		f.Set(reflect.ValueOf(v.(time.Time)))
	case convBytes:
		f.SetBytes(append([]byte{}, v.([]byte)...))
	case convText:
		return f.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(v.(string)))
	case convJSON:
		p := reflect.New(f.Type())
		if err := json.Unmarshal(v.(json.RawMessage), p.Interface()); err != nil {
			return err
		}
		f.Set(p.Elem())
	}
	return nil
}

// fill writes a row into the entity v points to.
func (m *mapping) fill(v reflect.Value, row Row) error {
	for i := range m.cols {
		c := &m.cols[i]
		if err := c.set(v.FieldByIndex(c.index), row[c.field.Name]); err != nil {
			return fmt.Errorf("ablage: read %s.%s: %w", m.typ, c.goName, err)
		}
	}
	return nil
}

// snakeCase turns a Go name into a column name: TenantID -> tenant_id,
// HTTPServer -> http_server.
func snakeCase(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) {
			if i > 0 && (unicode.IsLower(r[i-1]) || unicode.IsDigit(r[i-1]) ||
				(i+1 < len(r) && unicode.IsLower(r[i+1]) && unicode.IsUpper(r[i-1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(c))
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}
