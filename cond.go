package ablage

import (
	"fmt"
	"reflect"
	"strings"
)

// Op is the operator of a Cond.
type Op int

// The operators. The zero Cond has OpAll and matches every row.
const (
	OpAll    Op = iota // every row
	OpEq               // Field = Value
	OpNe               // Field differs from Value; NULL differs from every value
	OpLt               // Field < Value
	OpLte              // Field <= Value
	OpGt               // Field > Value
	OpGte              // Field >= Value
	OpIn               // Field is one of Value ([]any); an empty list matches nothing
	OpPrefix           // the String Field starts with Value
	OpNull             // Field is NULL
	OpAnd              // all of Conds; none matches every row
	OpOr               // any of Conds; none matches no row
	OpNot              // not Conds[0]
)

// Cond is a condition on the rows of a table, built with Eq, And, Not and
// the other functions below. Comparisons (Eq, Lt, In, Prefix, ...) are false
// for NULL, so Not(Eq(f, x)) matches NULL like Ne(f, x) does. Every store
// evaluates a Cond the same way.
type Cond struct {
	Op    Op
	Field string
	Value any
	Conds []Cond
}

// All matches every row.
var All = Cond{}

// Eq matches rows where field equals v; Eq(field, nil) is Null(field).
func Eq(field string, v any) Cond {
	if isNil(v) {
		return Null(field)
	}
	return Cond{Op: OpEq, Field: field, Value: v}
}

// Ne matches rows where field differs from v, including NULL; Ne(field, nil)
// is Not(Null(field)).
func Ne(field string, v any) Cond {
	if isNil(v) {
		return Not(Null(field))
	}
	return Cond{Op: OpNe, Field: field, Value: v}
}

func Lt(field string, v any) Cond  { return Cond{Op: OpLt, Field: field, Value: v} }
func Lte(field string, v any) Cond { return Cond{Op: OpLte, Field: field, Value: v} }
func Gt(field string, v any) Cond  { return Cond{Op: OpGt, Field: field, Value: v} }
func Gte(field string, v any) Cond { return Cond{Op: OpGte, Field: field, Value: v} }

// In matches rows where field is one of vs.
func In[V any](field string, vs ...V) Cond {
	list := make([]any, len(vs))
	for i, v := range vs {
		list[i] = v
	}
	return Cond{Op: OpIn, Field: field, Value: list}
}

// Prefix matches rows where the string field starts with p (case-sensitive).
func Prefix(field, p string) Cond { return Cond{Op: OpPrefix, Field: field, Value: p} }

// Null matches rows where field is NULL.
func Null(field string) Cond { return Cond{Op: OpNull, Field: field} }

// NotNull matches rows where field is not NULL.
func NotNull(field string) Cond { return Not(Null(field)) }

// And matches rows that match every c.
func And(c ...Cond) Cond {
	var out []Cond
	for _, x := range c {
		switch x.Op {
		case OpAll:
			continue
		case OpAnd:
			out = append(out, x.Conds...)
		default:
			out = append(out, x)
		}
	}
	switch len(out) {
	case 0:
		return All
	case 1:
		return out[0]
	}
	return Cond{Op: OpAnd, Conds: out}
}

// Or matches rows that match any c.
func Or(c ...Cond) Cond {
	if len(c) == 1 {
		return c[0]
	}
	return Cond{Op: OpOr, Conds: append([]Cond(nil), c...)}
}

// Not matches rows that do not match c.
func Not(c Cond) Cond { return Cond{Op: OpNot, Conds: []Cond{c}} }

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

// Fields returns the names of all fields the condition looks at.
func (c Cond) Fields() []string {
	var out []string
	var walk func(Cond)
	walk = func(c Cond) {
		if c.Field != "" {
			out = append(out, c.Field)
		}
		for _, x := range c.Conds {
			walk(x)
		}
	}
	walk(c)
	return out
}

// CheckCond validates a condition against the table and returns a copy with
// every value converted to the canonical type of its field.
func (t Table) CheckCond(c Cond) (Cond, error) {
	switch c.Op {
	case OpAll:
		return c, nil
	case OpAnd, OpOr, OpNot:
		if c.Op == OpNot && len(c.Conds) != 1 {
			return c, fmt.Errorf("ablage: Not needs exactly one condition")
		}
		out := Cond{Op: c.Op, Conds: make([]Cond, len(c.Conds))}
		for i, x := range c.Conds {
			nx, err := t.CheckCond(x)
			if err != nil {
				return c, err
			}
			out.Conds[i] = nx
		}
		return out, nil
	}
	f, ok := t.Field(c.Field)
	if !ok {
		return c, fmt.Errorf("ablage: unknown field %s.%s", t.Name, c.Field)
	}
	out := Cond{Op: c.Op, Field: c.Field}
	if f.Type == JSON && c.Op != OpNull {
		return c, fmt.Errorf("ablage: json field %s.%s can only be tested for NULL", t.Name, c.Field)
	}
	switch c.Op {
	case OpNull:
		return out, nil
	case OpEq, OpNe, OpLt, OpLte, OpGt, OpGte:
		if c.Op != OpEq && c.Op != OpNe && f.Type == Bool {
			return c, fmt.Errorf("ablage: cannot order the bool field %s.%s", t.Name, c.Field)
		}
		v, err := f.Value(c.Value, true)
		if err != nil {
			return c, err
		}
		if v == nil {
			return c, fmt.Errorf("ablage: comparison with NULL on %s.%s (use Null)", t.Name, c.Field)
		}
		out.Value = v
	case OpIn:
		list, ok := c.Value.([]any)
		if !ok {
			return c, fmt.Errorf("ablage: In on %s.%s needs a []any", t.Name, c.Field)
		}
		vals := make([]any, 0, len(list))
		for _, x := range list {
			v, err := f.Value(x, true)
			if err != nil {
				return c, err
			}
			if v == nil {
				return c, fmt.Errorf("ablage: In with NULL on %s.%s (use Null)", t.Name, c.Field)
			}
			vals = append(vals, v)
		}
		out.Value = vals
	case OpPrefix:
		if f.Type != String {
			return c, fmt.Errorf("ablage: Prefix on the %s field %s.%s", f.Type, t.Name, c.Field)
		}
		if _, ok := c.Value.(string); !ok {
			return c, fmt.Errorf("ablage: Prefix on %s.%s needs a string", t.Name, c.Field)
		}
		v, err := f.Value(c.Value, true)
		if err != nil {
			return c, err
		}
		out.Value = v
	default:
		return c, fmt.Errorf("ablage: invalid operator %d", c.Op)
	}
	return out, nil
}

// Match evaluates a checked condition (see Table.CheckCond) on a row of
// canonical values. It is how the in-memory store filters, and defines what
// every store must return.
func (c Cond) Match(row Row) bool {
	switch c.Op {
	case OpAll:
		return true
	case OpAnd:
		for _, x := range c.Conds {
			if !x.Match(row) {
				return false
			}
		}
		return true
	case OpOr:
		for _, x := range c.Conds {
			if x.Match(row) {
				return true
			}
		}
		return false
	case OpNot:
		return !c.Conds[0].Match(row)
	case OpNull:
		return row[c.Field] == nil
	}
	v := row[c.Field]
	if v == nil {
		return c.Op == OpNe
	}
	switch c.Op {
	case OpEq:
		return Compare(v, c.Value) == 0
	case OpNe:
		return Compare(v, c.Value) != 0
	case OpLt:
		return Compare(v, c.Value) < 0
	case OpLte:
		return Compare(v, c.Value) <= 0
	case OpGt:
		return Compare(v, c.Value) > 0
	case OpGte:
		return Compare(v, c.Value) >= 0
	case OpIn:
		for _, x := range c.Value.([]any) {
			if Compare(v, x) == 0 {
				return true
			}
		}
		return false
	case OpPrefix:
		return strings.HasPrefix(v.(string), c.Value.(string))
	}
	return false
}

// Order sorts by one field. NULL comes first in ascending order and last in
// descending order.
type Order struct {
	Field string
	Desc  bool
}

// Asc sorts by field, smallest first.
func Asc(field string) Order { return Order{Field: field} }

// Desc sorts by field, largest first.
func Desc(field string) Order { return Order{Field: field, Desc: true} }

// Query selects rows. Rows are sorted by OrderBy and then by id, so the
// order is always complete and the same in every store.
type Query struct {
	Where   Cond
	OrderBy []Order
	Limit   int // 0: no limit
	Offset  int
}

// CheckQuery validates a query and returns a copy with a checked condition
// and the order completed with id.
func (t Table) CheckQuery(q Query) (Query, error) {
	w, err := t.CheckCond(q.Where)
	if err != nil {
		return q, err
	}
	if q.Limit < 0 || q.Offset < 0 {
		return q, fmt.Errorf("ablage: negative limit or offset")
	}
	out := Query{Where: w, Limit: q.Limit, Offset: q.Offset}
	hasID := false
	for _, o := range q.OrderBy {
		f, ok := t.Field(o.Field)
		if !ok {
			return q, fmt.Errorf("ablage: order by unknown field %s.%s", t.Name, o.Field)
		}
		if !f.Type.ordered() {
			return q, fmt.Errorf("ablage: cannot order by the %s field %s.%s", f.Type, t.Name, o.Field)
		}
		out.OrderBy = append(out.OrderBy, o)
		if o.Field == "id" {
			hasID = true
			break // id is unique: later fields cannot change the order
		}
	}
	if !hasID {
		out.OrderBy = append(out.OrderBy, Asc("id"))
	}
	return out, nil
}

// Less reports whether row a sorts before row b in a checked query's order.
func (q Query) Less(a, b Row) bool {
	for _, o := range q.OrderBy {
		c := Compare(a[o.Field], b[o.Field])
		if o.Desc {
			c = -c
		}
		if c != 0 {
			return c < 0
		}
	}
	return false
}
