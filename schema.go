package ablage

import (
	"fmt"
	"regexp"
	"slices"
)

// Type is the type of a field. Every store maps each type to a column type
// of its own and hands values back in one canonical Go type, listed with
// each constant.
type Type int

// The field types.
const (
	String Type = iota + 1 // string
	Int                    // int64
	Float                  // float64
	Bool                   // bool
	Time                   // time.Time, UTC, microsecond precision
	Bytes                  // []byte
	JSON                   // json.RawMessage; cannot be filtered on except with Null
)

func (t Type) String() string {
	switch t {
	case String:
		return "string"
	case Int:
		return "int"
	case Float:
		return "float"
	case Bool:
		return "bool"
	case Time:
		return "time"
	case Bytes:
		return "bytes"
	case JSON:
		return "json"
	}
	return fmt.Sprintf("Type(%d)", int(t))
}

func (t Type) valid() bool { return t >= String && t <= JSON }

// ordered reports whether values of the type can be compared with Lt, Gt
// and their kin, and sorted.
func (t Type) ordered() bool { return t != JSON }

// OnDelete says what happens to a row when the row its reference points to
// is deleted for good. A soft delete is an update and triggers nothing.
type OnDelete int

const (
	Restrict OnDelete = iota // the delete fails with ErrReference
	Cascade                  // the referencing row is deleted as well
	SetNull                  // the reference is set to NULL; the field must be nullable
)

func (o OnDelete) String() string {
	switch o {
	case Restrict:
		return "restrict"
	case Cascade:
		return "cascade"
	case SetNull:
		return "set null"
	}
	return fmt.Sprintf("OnDelete(%d)", int(o))
}

// Field is a column of a table. Every table also has the field "id", a
// string primary key, which is not listed in Fields.
type Field struct {
	Name     string
	Type     Type
	Nullable bool // may hold NULL; a field that is not nullable defaults to its zero value
	Unique   bool // unique among non-NULL values (and among live rows, see Table.SoftDelete)
	Index    bool // a plain index
	// Ref names the table whose id this field holds. The field must be a
	// String. Writing an id that does not exist fails with ErrReference.
	Ref      string
	OnDelete OnDelete // only with Ref
}

// Index is an index over one or more fields.
type Index struct {
	Fields []string
	Unique bool // unique among rows where no field is NULL (and among live rows, see Table.SoftDelete)
}

// Table describes a table. Declare it on a store with Store.Declare and
// create it with Store.Migrate.
type Table struct {
	Name    string
	Fields  []Field
	Indexes []Index
	// SoftDelete names a nullable Time field that marks a row as deleted
	// when set. Unique fields and indexes then apply only to rows where it
	// is NULL, so a deleted row does not block its name. Leave empty for
	// tables without soft delete.
	SoftDelete string
}

// IDField is the primary key every table has.
var IDField = Field{Name: "id", Type: String}

// Field returns the named field, including "id".
func (t Table) Field(name string) (Field, bool) {
	if name == "id" {
		return IDField, true
	}
	for _, f := range t.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// Columns returns the names of all fields, "id" first.
func (t Table) Columns() []string {
	cols := make([]string, 0, len(t.Fields)+1)
	cols = append(cols, "id")
	for _, f := range t.Fields {
		cols = append(cols, f.Name)
	}
	return cols
}

// AllIndexes returns the indexes of the table: those declared on single
// fields with Unique or Index first, then Indexes.
func (t Table) AllIndexes() []Index {
	var out []Index
	for _, f := range t.Fields {
		if f.Unique {
			out = append(out, Index{Fields: []string{f.Name}, Unique: true})
		} else if f.Index {
			out = append(out, Index{Fields: []string{f.Name}})
		}
	}
	for _, ix := range t.Indexes {
		out = append(out, Index{Fields: slices.Clone(ix.Fields), Unique: ix.Unique})
	}
	return out
}

var namePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,47}$`)

// Validate checks the table on its own; references to other tables are
// checked when the store migrates.
func (t Table) Validate() error {
	if !namePattern.MatchString(t.Name) {
		return fmt.Errorf("ablage: invalid table name %q (lower case letters, digits and _, at most 48)", t.Name)
	}
	seen := map[string]bool{"id": true}
	for _, f := range t.Fields {
		if !namePattern.MatchString(f.Name) {
			return fmt.Errorf("ablage: invalid field name %s.%q", t.Name, f.Name)
		}
		if seen[f.Name] {
			if f.Name == "id" {
				return fmt.Errorf("ablage: %s declares the field id; every table has it already", t.Name)
			}
			return fmt.Errorf("ablage: duplicate field %s.%s", t.Name, f.Name)
		}
		seen[f.Name] = true
		if !f.Type.valid() {
			return fmt.Errorf("ablage: %s.%s has an invalid type", t.Name, f.Name)
		}
		if f.Type == JSON && (f.Unique || f.Index) {
			return fmt.Errorf("ablage: %s.%s: json fields cannot be indexed", t.Name, f.Name)
		}
		if f.Ref != "" && f.Type != String {
			return fmt.Errorf("ablage: %s.%s references %s and must be a string", t.Name, f.Name, f.Ref)
		}
		if f.Ref == "" && f.OnDelete != Restrict {
			return fmt.Errorf("ablage: %s.%s has OnDelete without Ref", t.Name, f.Name)
		}
		if f.OnDelete == SetNull && !f.Nullable {
			return fmt.Errorf("ablage: %s.%s is set to NULL on delete and must be nullable", t.Name, f.Name)
		}
		if f.OnDelete < Restrict || f.OnDelete > SetNull {
			return fmt.Errorf("ablage: %s.%s has an invalid OnDelete", t.Name, f.Name)
		}
	}
	for _, ix := range t.Indexes {
		if len(ix.Fields) == 0 {
			return fmt.Errorf("ablage: index without fields on %s", t.Name)
		}
		for _, name := range ix.Fields {
			f, ok := t.Field(name)
			if !ok {
				return fmt.Errorf("ablage: index on unknown field %s.%s", t.Name, name)
			}
			if f.Type == JSON {
				return fmt.Errorf("ablage: %s.%s: json fields cannot be indexed", t.Name, name)
			}
		}
	}
	if t.SoftDelete != "" {
		f, ok := t.Field(t.SoftDelete)
		if !ok || f.Type != Time || !f.Nullable {
			return fmt.Errorf("ablage: soft delete field %s.%s must be a nullable time field", t.Name, t.SoftDelete)
		}
	}
	return nil
}

func (t Table) equal(o Table) bool {
	norm := func(t Table) Table {
		t.Fields = slices.Clone(t.Fields)
		if len(t.Fields) == 0 {
			t.Fields = nil
		}
		t.Indexes = t.AllIndexes()
		for i := range t.Fields {
			t.Fields[i].Unique, t.Fields[i].Index = false, false
		}
		return t
	}
	a, b := norm(t), norm(o)
	return a.Name == b.Name && a.SoftDelete == b.SoftDelete &&
		slices.Equal(a.Fields, b.Fields) &&
		slices.EqualFunc(a.Indexes, b.Indexes, func(x, y Index) bool {
			return x.Unique == y.Unique && slices.Equal(x.Fields, y.Fields)
		})
}

// Catalog collects the tables declared on a store. Stores embed it; it is
// exported for implementations of Store outside this module. It is not safe
// for concurrent use.
type Catalog struct {
	tables map[string]Table
	order  []string
}

// Declare adds tables. Declaring a table again with the same definition does
// nothing; a different definition under the same name is an error.
func (c *Catalog) Declare(tables ...Table) error {
	for _, t := range tables {
		if err := t.Validate(); err != nil {
			return err
		}
		if have, ok := c.tables[t.Name]; ok {
			if !have.equal(t) {
				return fmt.Errorf("ablage: table %s is declared twice with different definitions", t.Name)
			}
			continue
		}
		if c.tables == nil {
			c.tables = map[string]Table{}
		}
		t.Fields = slices.Clone(t.Fields)
		t.Indexes = slices.Clone(t.Indexes)
		c.tables[t.Name] = t
		c.order = append(c.order, t.Name)
	}
	return nil
}

// Table returns a declared table.
func (c *Catalog) Table(name string) (Table, error) {
	t, ok := c.tables[name]
	if !ok {
		return Table{}, fmt.Errorf("ablage: unknown table %q (declare it first)", name)
	}
	return t, nil
}

// Tables returns all declared tables, each after the tables it references
// (a table may reference itself). It fails if a reference names an
// undeclared table or references form a cycle.
func (c *Catalog) Tables() ([]Table, error) {
	var out []Table
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(name string, from string) error
	visit = func(name, from string) error {
		t, ok := c.tables[name]
		if !ok {
			return fmt.Errorf("ablage: %s references the undeclared table %s", from, name)
		}
		switch state[name] {
		case 1:
			return fmt.Errorf("ablage: tables %s and %s reference each other in a cycle", from, name)
		case 2:
			return nil
		}
		state[name] = 1
		for _, f := range t.Fields {
			if f.Ref != "" && f.Ref != name {
				if err := visit(f.Ref, name+"."+f.Name); err != nil {
					return err
				}
			}
		}
		state[name] = 2
		out = append(out, t)
		return nil
	}
	for _, name := range c.order {
		if err := visit(name, name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Referencing returns, for the named table, every field of a declared table
// that references it.
func (c *Catalog) Referencing(table string) []RefField {
	var out []RefField
	for _, name := range c.order {
		for _, f := range c.tables[name].Fields {
			if f.Ref == table {
				out = append(out, RefField{Table: name, Field: f})
			}
		}
	}
	return out
}

// RefField is a field of Table that references another table.
type RefField struct {
	Table string
	Field Field
}
