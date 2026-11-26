package ablage

import (
	"context"
	"fmt"
	"math/big"
	"slices"
)

// AggOp is the function of an Agg.
type AggOp int

// The aggregate functions. Each ignores NULL values of its field.
const (
	AggCount AggOp = iota + 1 // rows (no Field), or non-NULL values of Field; an int64
	AggSum                    // sum of an Int field as an int64; 0 if there are no values
	AggMin                    // smallest value of a String, Int, Float or Time field; NULL if there are none
	AggMax                    // largest value, as AggMin
)

func (o AggOp) String() string {
	switch o {
	case AggCount:
		return "count"
	case AggSum:
		return "sum"
	case AggMin:
		return "min"
	case AggMax:
		return "max"
	}
	return fmt.Sprintf("AggOp(%d)", int(o))
}

// Agg is one value computed over the rows of a group, named As in the
// result rows.
type Agg struct {
	Op    AggOp
	Field string
	As    string
}

// CountAll counts the rows.
func CountAll(as string) Agg { return Agg{Op: AggCount, As: as} }

// Count counts the rows where field is not NULL.
func Count(field, as string) Agg { return Agg{Op: AggCount, Field: field, As: as} }

// Sum adds up an Int field, ignoring NULL. The sum of no values is 0.
func Sum(field, as string) Agg { return Agg{Op: AggSum, Field: field, As: as} }

// Min is the smallest value of field, ignoring NULL; NULL if there is none.
func Min(field, as string) Agg { return Agg{Op: AggMin, Field: field, As: as} }

// Max is the largest value of field, ignoring NULL; NULL if there is none.
func Max(field, as string) Agg { return Agg{Op: AggMax, Field: field, As: as} }

// Aggregation computes Values over the rows matching Where, for each
// combination of the GroupBy fields' values, e.g. the number of messages
// and their total size per folder:
//
//	ablage.Aggregation{
//		Where:   ablage.Eq("mailbox_id", id),
//		GroupBy: []string{"folder_id"},
//		Values:  []ablage.Agg{ablage.CountAll("n"), ablage.Sum("size", "bytes")},
//	}
//
// The result has one row per group, holding the GroupBy fields and the
// values under their names, sorted by the GroupBy fields in order (as
// Query sorts: NULL first, strings by bytes). NULL is a group of its own.
// Without GroupBy the result is one row, also if no row matches: counts and
// sums are 0, Min and Max NULL. With GroupBy and no matching row, it is
// empty.
type Aggregation struct {
	Where   Cond
	GroupBy []string
	Values  []Agg
}

// Aggregator is a Store that computes aggregations. Every store of this
// module is one; use Aggregate or Repository.Aggregate, which work on any
// Store and fail for those that are not.
type Aggregator interface {
	// Aggregate returns the result of the aggregation over the rows of
	// table, as Aggregation.Apply defines it.
	Aggregate(ctx context.Context, table string, a Aggregation) ([]Row, error)
}

// Aggregate runs the aggregation on table, if the store is an Aggregator.
func Aggregate(ctx context.Context, s Store, table string, a Aggregation) ([]Row, error) {
	ag, ok := s.(Aggregator)
	if !ok {
		return nil, fmt.Errorf("%w: aggregate (%T)", ErrUnsupported, s)
	}
	return ag.Aggregate(ctx, table, a)
}

// CheckAggregation validates an aggregation and returns a copy with a
// checked condition.
func (t Table) CheckAggregation(a Aggregation) (Aggregation, error) {
	w, err := t.CheckCond(a.Where)
	if err != nil {
		return a, err
	}
	out := Aggregation{Where: w, GroupBy: slices.Clone(a.GroupBy), Values: slices.Clone(a.Values)}
	if len(out.GroupBy) == 0 && len(out.Values) == 0 {
		return a, fmt.Errorf("ablage: aggregation on %s without values or groups", t.Name)
	}
	names := map[string]bool{}
	for _, g := range out.GroupBy {
		f, ok := t.Field(g)
		if !ok {
			return a, fmt.Errorf("ablage: group by unknown field %s.%s", t.Name, g)
		}
		if f.Type == JSON {
			return a, fmt.Errorf("ablage: cannot group by the json field %s.%s", t.Name, g)
		}
		if names[g] {
			return a, fmt.Errorf("ablage: %s.%s is grouped by twice", t.Name, g)
		}
		names[g] = true
	}
	for _, v := range out.Values {
		if !namePattern.MatchString(v.As) {
			return a, fmt.Errorf("ablage: invalid name %q for an aggregate on %s", v.As, t.Name)
		}
		if names[v.As] {
			return a, fmt.Errorf("ablage: the name %s is used twice in an aggregation on %s", v.As, t.Name)
		}
		names[v.As] = true
		if v.Op == AggCount && v.Field == "" {
			continue
		}
		f, ok := t.Field(v.Field)
		if !ok {
			return a, fmt.Errorf("ablage: %s of unknown field %s.%s", v.Op, t.Name, v.Field)
		}
		switch v.Op {
		case AggCount:
		case AggSum:
			if f.Type != Int {
				return a, fmt.Errorf("ablage: sum of the %s field %s.%s (only int fields)", f.Type, t.Name, v.Field)
			}
		case AggMin, AggMax:
			switch f.Type {
			case String, Int, Float, Time:
			default:
				return a, fmt.Errorf("ablage: %s of the %s field %s.%s", v.Op, f.Type, t.Name, v.Field)
			}
		default:
			return a, fmt.Errorf("ablage: invalid aggregate %d on %s", v.Op, t.Name)
		}
	}
	return out, nil
}

// Apply computes a checked aggregation (see Table.CheckAggregation) over
// rows of canonical values that already match its Where. It is how the
// in-memory store aggregates, and defines what every store must return. A
// sum outside the range of an int64 is an error.
func (a Aggregation) Apply(rows []Row) ([]Row, error) {
	type group struct {
		key  Row
		rows []Row
	}
	var groups []*group
	byKey := func(x, y Row) int {
		for _, g := range a.GroupBy {
			if c := Compare(x[g], y[g]); c != 0 {
				return c
			}
		}
		return 0
	}
	if len(a.GroupBy) == 0 {
		groups = []*group{{key: Row{}, rows: rows}}
	} else {
		sorted := slices.Clone(rows)
		slices.SortStableFunc(sorted, byKey)
		for _, r := range sorted {
			if n := len(groups); n > 0 && byKey(groups[n-1].key, r) == 0 {
				groups[n-1].rows = append(groups[n-1].rows, r)
				continue
			}
			key := Row{}
			for _, g := range a.GroupBy {
				key[g] = CloneValue(r[g])
			}
			groups = append(groups, &group{key: key, rows: []Row{r}})
		}
	}
	out := make([]Row, 0, len(groups))
	for _, g := range groups {
		row := g.key
		for _, v := range a.Values {
			val, err := v.apply(g.rows)
			if err != nil {
				return nil, err
			}
			row[v.As] = val
		}
		out = append(out, row)
	}
	return out, nil
}

func (v Agg) apply(rows []Row) (any, error) {
	switch v.Op {
	case AggCount:
		var n int64
		for _, r := range rows {
			if v.Field == "" || r[v.Field] != nil {
				n++
			}
		}
		return n, nil
	case AggSum:
		sum := new(big.Int)
		for _, r := range rows {
			if x, ok := r[v.Field].(int64); ok {
				sum.Add(sum, big.NewInt(x))
			}
		}
		if !sum.IsInt64() {
			return nil, fmt.Errorf("ablage: the sum of %s does not fit into an int64", v.Field)
		}
		return sum.Int64(), nil
	}
	var best any
	for _, r := range rows {
		x := r[v.Field]
		if x == nil {
			continue
		}
		if c := Compare(x, best); best == nil || (v.Op == AggMin && c < 0) || (v.Op == AggMax && c > 0) {
			best = x
		}
	}
	return CloneValue(best), nil
}
