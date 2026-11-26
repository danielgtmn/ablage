// Package storetest is the conformance suite for ablage.Store
// implementations. Every store in this module passes it; run it for your own:
//
//	func TestMyStore(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) ablage.Store { return mystore.New(...) })
//	}
//
// open must return an empty store with nothing declared.
package storetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/danielgtmn/ablage"
)

// Opener returns a fresh, empty store for one test.
type Opener func(t *testing.T) ablage.Store

// Run runs every conformance test against stores made by open.
func Run(t *testing.T, open Opener) {
	tests := []struct {
		name string
		fn   func(t *testing.T, open Opener)
	}{
		{"Declare", testDeclare},
		{"InsertFind", testInsertFind},
		{"Types", testTypes},
		{"Conditions", testConditions},
		{"Order", testOrder},
		{"LimitOffset", testLimitOffset},
		{"Update", testUpdate},
		{"Delete", testDelete},
		{"Duplicate", testDuplicate},
		{"SoftDeleteUnique", testSoftDeleteUnique},
		{"References", testReferences},
		{"Upsert", testUpsert},
		{"Tx", testTx},
		{"Errors", testErrors},
		{"IndexNames", testIndexNames},
		{"TxContinuesAfterError", testTxContinuesAfterError},
		{"TxPanic", testTxPanic},
		{"UniqueRowByRow", testUniqueRowByRow},
		{"InvalidValues", testInvalidValues},
		{"CascadeCount", testCascadeCount},
		{"LongIn", testLongIn},
		{"LongInTypes", testLongInTypes},
		{"Aggregate", testAggregate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, open) })
	}
}

var ctx = context.Background()

// people is the main table of the suite.
var people = ablage.Table{
	Name: "people",
	Fields: []ablage.Field{
		{Name: "name", Type: ablage.String},
		{Name: "email", Type: ablage.String, Nullable: true, Unique: true},
		{Name: "age", Type: ablage.Int, Nullable: true, Index: true},
		{Name: "score", Type: ablage.Float, Nullable: true},
		{Name: "active", Type: ablage.Bool},
		{Name: "born", Type: ablage.Time, Nullable: true},
		{Name: "avatar", Type: ablage.Bytes, Nullable: true},
		{Name: "tags", Type: ablage.JSON, Nullable: true},
		{Name: "visits", Type: ablage.Int},
	},
}

func setup(t *testing.T, open Opener, tables ...ablage.Table) ablage.Store {
	t.Helper()
	s := open(t)
	if len(tables) == 0 {
		tables = []ablage.Table{people}
	}
	if err := s.Declare(tables...); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func insert(t *testing.T, s ablage.Store, table string, rows ...ablage.Row) {
	t.Helper()
	for _, r := range rows {
		must(t, s.Insert(ctx, table, r))
	}
}

func ids(t *testing.T, s ablage.Store, table string, q ablage.Query) []string {
	t.Helper()
	rows, err := s.Find(ctx, table, q)
	must(t, err)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r["id"].(string)
	}
	return out
}

func expectIDs(t *testing.T, s ablage.Store, table string, q ablage.Query, want ...string) {
	t.Helper()
	got := ids(t, s, table, q)
	if want == nil {
		want = []string{}
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func get(t *testing.T, s ablage.Store, table, id string) ablage.Row {
	t.Helper()
	rows, err := s.Find(ctx, table, ablage.Query{Where: ablage.Eq("id", id)})
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("%s %s: %d rows", table, id, len(rows))
	}
	return rows[0]
}

func testDeclare(t *testing.T, open Opener) {
	s := open(t)
	must(t, s.Declare(people))
	must(t, s.Declare(people)) // the same again is fine
	changed := people
	changed.Fields = append(slices.Clone(people.Fields), ablage.Field{Name: "extra", Type: ablage.String})
	if err := s.Declare(changed); err == nil {
		t.Error("a different definition under the same name was accepted")
	}
	if err := s.Declare(ablage.Table{Name: "Bad-Name"}); err == nil {
		t.Error("invalid table name accepted")
	}
	if err := s.Insert(ctx, "people", ablage.Row{"id": "a"}); err == nil {
		t.Error("insert before Migrate succeeded")
	}
	must(t, s.Migrate(ctx))
	must(t, s.Migrate(ctx)) // twice is fine
	// Tables declared later are created by the next Migrate.
	must(t, s.Declare(ablage.Table{Name: "later", Fields: []ablage.Field{{Name: "x", Type: ablage.Int}}}))
	must(t, s.Migrate(ctx))
	insert(t, s, "later", ablage.Row{"id": "1", "x": 1})
	if err := s.Declare(ablage.Table{Name: "orphan", Fields: []ablage.Field{{Name: "p", Type: ablage.String, Ref: "nowhere"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err == nil {
		t.Error("Migrate with a reference to an undeclared table succeeded")
	}
}

func testInsertFind(t *testing.T, open Opener) {
	s := setup(t, open)
	insert(t, s, "people", ablage.Row{"id": "a", "name": "Ada"})
	r := get(t, s, "people", "a")
	want := ablage.Row{
		"id": "a", "name": "Ada", "email": nil, "age": nil, "score": nil, "active": false,
		"born": nil, "avatar": nil, "tags": nil, "visits": int64(0),
	}
	if fmt.Sprint(r) != fmt.Sprint(want) {
		t.Errorf("row\n got %v\nwant %v", r, want)
	}
	if err := s.Insert(ctx, "people", ablage.Row{"name": "no id"}); err == nil {
		t.Error("row without id accepted")
	}
	if err := s.Insert(ctx, "people", ablage.Row{"id": "b", "nope": 1}); err == nil {
		t.Error("unknown field accepted")
	}
	if err := s.Insert(ctx, "people", ablage.Row{"id": "b", "name": nil}); err == nil {
		t.Error("NULL in a field that is not nullable accepted")
	}
	if err := s.Insert(ctx, "people", ablage.Row{"id": "b", "age": "old"}); err == nil {
		t.Error("string in an int field accepted")
	}
	n, err := s.Count(ctx, "people", ablage.All)
	must(t, err)
	if n != 1 {
		t.Errorf("count %d, want 1", n)
	}
	// A row handed out is a copy.
	r["name"] = "changed"
	if get(t, s, "people", "a")["name"] != "Ada" {
		t.Error("changing a returned row changed the store")
	}
}

func testTypes(t *testing.T, open Opener) {
	s := setup(t, open)
	born := time.Date(1815, 12, 10, 8, 30, 15, 123456789, time.FixedZone("x", 3600))
	type named string
	insert(t, s, "people", ablage.Row{
		"id": "a", "name": named("Ada"), "email": "ada@example.org", "age": 36, "score": 9.75,
		"active": true, "born": born, "avatar": []byte{0, 1, 2, 255}, "tags": json.RawMessage(`{"b":[1,2],"a":"x"}`),
		"visits": uint8(3),
	})
	r := get(t, s, "people", "a")
	if r["name"] != "Ada" || r["email"] != "ada@example.org" || r["age"] != int64(36) ||
		r["score"] != 9.75 || r["active"] != true || r["visits"] != int64(3) {
		t.Errorf("scalars: %v", r)
	}
	if got := r["born"].(time.Time); !got.Equal(ablage.NormalizeTime(born)) || got.Location() != time.UTC || got.Nanosecond()%1000 != 0 {
		t.Errorf("time %v (%s), want %v in UTC to the microsecond", got, got.Location(), born)
	}
	if !bytes.Equal(r["avatar"].([]byte), []byte{0, 1, 2, 255}) {
		t.Errorf("bytes %v", r["avatar"])
	}
	var tags map[string]any
	must(t, json.Unmarshal(r["tags"].(json.RawMessage), &tags))
	if fmt.Sprint(tags) != "map[a:x b:[1 2]]" {
		t.Errorf("json %v", tags)
	}
	// Zero values of every type survive, distinct from NULL.
	zero := ablage.Table{Name: "zeros", Fields: []ablage.Field{
		{Name: "s", Type: ablage.String}, {Name: "i", Type: ablage.Int}, {Name: "f", Type: ablage.Float},
		{Name: "b", Type: ablage.Bool}, {Name: "t", Type: ablage.Time}, {Name: "y", Type: ablage.Bytes},
		{Name: "j", Type: ablage.JSON},
	}}
	must(t, s.Declare(zero))
	must(t, s.Migrate(ctx))
	insert(t, s, "zeros", ablage.Row{"id": "z"})
	z := get(t, s, "zeros", "z")
	if z["s"] != "" || z["i"] != int64(0) || z["f"] != 0.0 || z["b"] != false ||
		!z["t"].(time.Time).IsZero() || len(z["y"].([]byte)) != 0 || z["y"] == nil || string(z["j"].(json.RawMessage)) != "null" {
		t.Errorf("zero values: %#v", z)
	}
	// Large and negative numbers, extreme times.
	insert(t, s, "zeros", ablage.Row{"id": "big", "i": int64(-1 << 63), "f": -1.5e300, "t": time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)})
	big := get(t, s, "zeros", "big")
	if big["i"] != int64(-1<<63) || big["f"] != -1.5e300 || big["t"].(time.Time).Year() != 9999 {
		t.Errorf("extremes: %v", big)
	}
}

func seedPeople(t *testing.T, s ablage.Store) {
	insert(t, s, "people",
		ablage.Row{"id": "1", "name": "Ada", "email": "ada@x", "age": 36, "active": true, "score": 1.5},
		ablage.Row{"id": "2", "name": "Alan", "email": "alan@x", "age": 41, "active": false},
		ablage.Row{"id": "3", "name": "Grace", "age": 85, "active": true, "score": -2.0},
		ablage.Row{"id": "4", "name": "grace", "active": false},
		ablage.Row{"id": "5", "name": "Ünal", "email": "u@x", "age": 36, "active": true},
	)
}

func testConditions(t *testing.T, open Opener) {
	s := setup(t, open)
	seedPeople(t, s)
	q := func(c ablage.Cond) ablage.Query { return ablage.Query{Where: c} }
	cases := []struct {
		name string
		c    ablage.Cond
		want []string
	}{
		{"all", ablage.All, []string{"1", "2", "3", "4", "5"}},
		{"eq", ablage.Eq("age", 36), []string{"1", "5"}},
		{"eq is case-sensitive", ablage.Eq("name", "grace"), []string{"4"}},
		{"eq nil", ablage.Eq("age", nil), []string{"4"}},
		{"ne includes NULL", ablage.Ne("age", 36), []string{"2", "3", "4"}},
		{"ne nil", ablage.Ne("email", nil), []string{"1", "2", "5"}},
		{"lt", ablage.Lt("age", 41), []string{"1", "5"}},
		{"lte", ablage.Lte("age", 41), []string{"1", "2", "5"}},
		{"gt", ablage.Gt("age", 41), []string{"3"}},
		{"gte", ablage.Gte("age", 41), []string{"2", "3"}},
		{"float", ablage.Lt("score", 0), []string{"3"}},
		{"bool", ablage.Eq("active", true), []string{"1", "3", "5"}},
		{"in", ablage.In("name", "Ada", "Grace", "nobody"), []string{"1", "3"}},
		{"in empty", ablage.In[string]("name"), nil},
		{"prefix", ablage.Prefix("name", "A"), []string{"1", "2"}},
		{"prefix case-sensitive", ablage.Prefix("name", "g"), []string{"4"}},
		{"prefix empty", ablage.Prefix("email", ""), []string{"1", "2", "5"}},
		{"prefix unicode", ablage.Prefix("name", "Ü"), []string{"5"}},
		{"prefix no like wildcards", ablage.Prefix("name", "A%"), nil},
		{"null", ablage.Null("email"), []string{"3", "4"}},
		{"not null", ablage.NotNull("email"), []string{"1", "2", "5"}},
		{"and", ablage.And(ablage.Eq("active", true), ablage.Gt("age", 40)), []string{"3"}},
		{"or", ablage.Or(ablage.Eq("id", "1"), ablage.Eq("id", "4")), []string{"1", "4"}},
		{"or empty", ablage.Or(), nil},
		{"not includes NULL", ablage.Not(ablage.Gt("age", 40)), []string{"1", "4", "5"}},
		{"not not", ablage.Not(ablage.Not(ablage.Eq("age", 36))), []string{"1", "5"}},
		{"not in", ablage.Not(ablage.In("age", 36, 41)), []string{"3", "4"}},
		{"not and", ablage.Not(ablage.And(ablage.Eq("active", true), ablage.Eq("age", 36))), []string{"2", "3", "4"}},
		{"nested", ablage.Or(ablage.And(ablage.Prefix("name", "A"), ablage.Not(ablage.Eq("active", true))), ablage.Null("age")), []string{"2", "4"}},
		{"json null", ablage.Null("tags"), []string{"1", "2", "3", "4", "5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectIDs(t, s, "people", q(tc.c), tc.want...)
			n, err := s.Count(ctx, "people", tc.c)
			must(t, err)
			if n != int64(len(tc.want)) {
				t.Errorf("count %d, want %d", n, len(tc.want))
			}
		})
	}
	bad := []ablage.Cond{
		ablage.Eq("nope", 1),
		ablage.Eq("age", "x"),
		ablage.Lt("active", true),
		ablage.Prefix("age", "1"),
		ablage.Eq("tags", `{}`),
		{Op: ablage.OpNot},
	}
	for _, c := range bad {
		if _, err := s.Find(ctx, "people", q(c)); err == nil {
			t.Errorf("invalid condition %+v accepted", c)
		}
	}
}

func testOrder(t *testing.T, open Opener) {
	s := setup(t, open)
	seedPeople(t, s)
	cases := []struct {
		name  string
		order []ablage.Order
		want  []string
	}{
		{"default by id", nil, []string{"1", "2", "3", "4", "5"}},
		{"bytes order", []ablage.Order{ablage.Asc("name")}, []string{"1", "2", "3", "4", "5"}},
		{"desc", []ablage.Order{ablage.Desc("name")}, []string{"5", "4", "3", "2", "1"}},
		{"nulls first asc, ties by id", []ablage.Order{ablage.Asc("age")}, []string{"4", "1", "5", "2", "3"}},
		{"nulls last desc", []ablage.Order{ablage.Desc("age")}, []string{"3", "2", "1", "5", "4"}},
		{"two fields", []ablage.Order{ablage.Asc("age"), ablage.Desc("name")}, []string{"4", "5", "1", "2", "3"}},
		{"bool", []ablage.Order{ablage.Desc("active"), ablage.Desc("id")}, []string{"5", "3", "1", "4", "2"}},
		{"float", []ablage.Order{ablage.Asc("score")}, []string{"2", "4", "5", "3", "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectIDs(t, s, "people", ablage.Query{OrderBy: tc.order}, tc.want...)
		})
	}
	if _, err := s.Find(ctx, "people", ablage.Query{OrderBy: []ablage.Order{ablage.Asc("tags")}}); err == nil {
		t.Error("ordering by a json field accepted")
	}
}

func testLimitOffset(t *testing.T, open Opener) {
	s := setup(t, open)
	seedPeople(t, s)
	expectIDs(t, s, "people", ablage.Query{Limit: 2}, "1", "2")
	expectIDs(t, s, "people", ablage.Query{Limit: 2, Offset: 2}, "3", "4")
	expectIDs(t, s, "people", ablage.Query{Offset: 3}, "4", "5")
	expectIDs(t, s, "people", ablage.Query{Offset: 10})
	if _, err := s.Find(ctx, "people", ablage.Query{Limit: -1}); err == nil {
		t.Error("negative limit accepted")
	}
}

func testUpdate(t *testing.T, open Opener) {
	s := setup(t, open)
	seedPeople(t, s)
	n, err := s.Update(ctx, "people", ablage.Row{"active": false, "visits": ablage.Incr(2)}, ablage.Eq("age", 36))
	must(t, err)
	if n != 2 {
		t.Errorf("updated %d, want 2", n)
	}
	n, err = s.Update(ctx, "people", ablage.Row{"visits": ablage.Incr(-1)}, ablage.Eq("id", "1"))
	must(t, err)
	if r := get(t, s, "people", "1"); r["active"] != false || r["visits"] != int64(1) || n != 1 {
		t.Errorf("after update: %v (n=%d)", r, n)
	}
	n, err = s.Update(ctx, "people", ablage.Row{"email": nil}, ablage.Eq("id", "nope"))
	must(t, err)
	if n != 0 {
		t.Errorf("updated %d rows matching nothing", n)
	}
	must(t, nil)
	if _, err := s.Update(ctx, "people", ablage.Row{"email": nil}, ablage.Eq("id", "1")); err != nil {
		t.Error(err)
	}
	if get(t, s, "people", "1")["email"] != nil {
		t.Error("set to NULL failed")
	}
	for _, set := range []ablage.Row{{}, {"id": "x"}, {"age": ablage.Incr(1)}, {"name": nil}, {"nope": 1}} {
		if _, err := s.Update(ctx, "people", set, ablage.All); err == nil {
			t.Errorf("invalid update %v accepted", set)
		}
	}
}

func testDelete(t *testing.T, open Opener) {
	s := setup(t, open)
	seedPeople(t, s)
	n, err := s.Delete(ctx, "people", ablage.Eq("active", true))
	must(t, err)
	if n != 3 {
		t.Errorf("deleted %d, want 3", n)
	}
	expectIDs(t, s, "people", ablage.Query{}, "2", "4")
	n, err = s.Delete(ctx, "people", ablage.Eq("id", "nope"))
	must(t, err)
	if n != 0 {
		t.Errorf("deleted %d rows matching nothing", n)
	}
}

func testDuplicate(t *testing.T, open Opener) {
	s := setup(t, open)
	seedPeople(t, s)
	err := s.Insert(ctx, "people", ablage.Row{"id": "1", "name": "again"})
	if !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("duplicate id: %v", err)
	}
	err = s.Insert(ctx, "people", ablage.Row{"id": "9", "name": "x", "email": "ada@x"})
	if !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("duplicate unique field: %v", err)
	}
	_, err = s.Update(ctx, "people", ablage.Row{"email": "ada@x"}, ablage.Eq("id", "2"))
	if !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("update to a duplicate: %v", err)
	}
	// NULL does not count.
	insert(t, s, "people", ablage.Row{"id": "8", "name": "no mail"})
	// Unique across several fields.
	pair := ablage.Table{Name: "members", Fields: []ablage.Field{
		{Name: "tenant", Type: ablage.String}, {Name: "email", Type: ablage.String, Nullable: true},
	}, Indexes: []ablage.Index{{Fields: []string{"tenant", "email"}, Unique: true}}}
	must(t, s.Declare(pair))
	must(t, s.Migrate(ctx))
	insert(t, s, "members",
		ablage.Row{"id": "a", "tenant": "t1", "email": "x"},
		ablage.Row{"id": "b", "tenant": "t2", "email": "x"},
		ablage.Row{"id": "c", "tenant": "t1"},
		ablage.Row{"id": "d", "tenant": "t1"},
	)
	if err := s.Insert(ctx, "members", ablage.Row{"id": "e", "tenant": "t1", "email": "x"}); !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("duplicate pair: %v", err)
	}
	if n, _ := s.Count(ctx, "people", ablage.All); n != 6 {
		t.Errorf("failed inserts left rows: %d", n)
	}
}

func testSoftDeleteUnique(t *testing.T, open Opener) {
	users := ablage.Table{Name: "users", Fields: []ablage.Field{
		{Name: "email", Type: ablage.String, Unique: true},
		{Name: "deleted_at", Type: ablage.Time, Nullable: true},
	}, SoftDelete: "deleted_at"}
	s := setup(t, open, users)
	insert(t, s, "users", ablage.Row{"id": "a", "email": "x"})
	if err := s.Insert(ctx, "users", ablage.Row{"id": "b", "email": "x"}); !errors.Is(err, ablage.ErrDuplicate) {
		t.Fatalf("duplicate among live rows: %v", err)
	}
	_, err := s.Update(ctx, "users", ablage.Row{"deleted_at": time.Now()}, ablage.Eq("id", "a"))
	must(t, err)
	insert(t, s, "users", ablage.Row{"id": "b", "email": "x"}) // a deleted row does not block
	_, err = s.Update(ctx, "users", ablage.Row{"deleted_at": nil}, ablage.Eq("id", "a"))
	if !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("restoring into a taken value: %v", err)
	}
	if err := s.Insert(ctx, "users", ablage.Row{"id": "a", "email": "y"}); !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("the id stays taken by a deleted row: %v", err)
	}
	if err := s.Declare(ablage.Table{Name: "bad", Fields: []ablage.Field{{Name: "d", Type: ablage.Time}}, SoftDelete: "d"}); err == nil {
		t.Error("soft delete on a field that is not nullable accepted")
	}
}

func testReferences(t *testing.T, open Opener) {
	projects := ablage.Table{Name: "projects", Fields: []ablage.Field{{Name: "name", Type: ablage.String}}}
	tasks := ablage.Table{Name: "tasks", Fields: []ablage.Field{
		{Name: "project", Type: ablage.String, Ref: "projects", OnDelete: ablage.Cascade},
		{Name: "owner", Type: ablage.String, Nullable: true, Ref: "people", OnDelete: ablage.SetNull},
		{Name: "reviewer", Type: ablage.String, Nullable: true, Ref: "people"},
		{Name: "parent", Type: ablage.String, Nullable: true, Ref: "tasks", OnDelete: ablage.Cascade},
	}}
	// Declared before the tables they reference: Migrate sorts them.
	s := setup(t, open, tasks, people, projects)
	seedPeople(t, s)
	insert(t, s, "projects", ablage.Row{"id": "p1", "name": "one"}, ablage.Row{"id": "p2", "name": "two"})
	insert(t, s, "tasks",
		ablage.Row{"id": "t1", "project": "p1", "owner": "1"},
		ablage.Row{"id": "t2", "project": "p1", "owner": "1", "parent": "t1"},
		ablage.Row{"id": "t3", "project": "p2", "reviewer": "2"},
	)
	if err := s.Insert(ctx, "tasks", ablage.Row{"id": "t9", "project": "nope"}); !errors.Is(err, ablage.ErrReference) {
		t.Errorf("missing parent: %v", err)
	}
	if _, err := s.Update(ctx, "tasks", ablage.Row{"owner": "nope"}, ablage.Eq("id", "t1")); !errors.Is(err, ablage.ErrReference) {
		t.Errorf("update to a missing parent: %v", err)
	}
	// Restrict: the reviewer cannot be deleted while referenced.
	if _, err := s.Delete(ctx, "people", ablage.Eq("id", "2")); !errors.Is(err, ablage.ErrReference) {
		t.Errorf("delete of a referenced row: %v", err)
	}
	expectIDs(t, s, "people", ablage.Query{Where: ablage.Eq("id", "2")}, "2")
	// SetNull.
	_, err := s.Delete(ctx, "people", ablage.Eq("id", "1"))
	must(t, err)
	if get(t, s, "tasks", "t1")["owner"] != nil {
		t.Error("owner not set to NULL")
	}
	// Cascade, also through the self-reference.
	_, err = s.Delete(ctx, "projects", ablage.Eq("id", "p1"))
	must(t, err)
	expectIDs(t, s, "tasks", ablage.Query{}, "t3")
	// A failed cascade changes nothing.
	insert(t, s, "tasks", ablage.Row{"id": "t4", "project": "p2"})
	_, err = s.Update(ctx, "tasks", ablage.Row{"reviewer": "2"}, ablage.Eq("id", "t4"))
	must(t, err)
	if _, err := s.Delete(ctx, "people", ablage.All); !errors.Is(err, ablage.ErrReference) {
		t.Errorf("delete all people: %v", err)
	}
	if n, _ := s.Count(ctx, "people", ablage.All); n != 4 {
		t.Errorf("a failed delete removed rows: %d left", n)
	}
	if err := s.Declare(ablage.Table{Name: "bad", Fields: []ablage.Field{{Name: "r", Type: ablage.String, Ref: "people", OnDelete: ablage.SetNull}}}); err == nil {
		t.Error("SetNull on a field that is not nullable accepted")
	}
}

func testUpsert(t *testing.T, open Opener) {
	s := setup(t, open)
	ok, err := s.Upsert(ctx, "people", ablage.Row{"id": "a", "name": "Ada", "visits": 1},
		ablage.Row{"name": "Ada", "visits": ablage.Incr(1)}, ablage.All)
	must(t, err)
	if !ok || get(t, s, "people", "a")["visits"] != int64(1) {
		t.Errorf("insert by upsert: ok=%v %v", ok, get(t, s, "people", "a"))
	}
	ok, err = s.Upsert(ctx, "people", ablage.Row{"id": "a", "name": "Ada L.", "visits": 1},
		ablage.Row{"name": "Ada L.", "visits": ablage.Incr(1)}, ablage.All)
	must(t, err)
	if r := get(t, s, "people", "a"); !ok || r["name"] != "Ada L." || r["visits"] != int64(2) {
		t.Errorf("update by upsert: ok=%v %v", ok, r)
	}
	ok, err = s.Upsert(ctx, "people", ablage.Row{"id": "a", "name": "other"},
		ablage.Row{"name": "other"}, ablage.Eq("active", true))
	must(t, err)
	if r := get(t, s, "people", "a"); ok || r["name"] != "Ada L." {
		t.Errorf("upsert not matching where: ok=%v %v", ok, r)
	}
	ok, err = s.Upsert(ctx, "people", ablage.Row{"id": "a"}, ablage.Row{}, ablage.Eq("name", "Ada L."))
	must(t, err)
	if !ok {
		t.Error("empty update of a matching row reports false")
	}
	insert(t, s, "people", ablage.Row{"id": "b", "name": "B", "email": "b@x"})
	_, err = s.Upsert(ctx, "people", ablage.Row{"id": "a", "name": "x"}, ablage.Row{"email": "b@x"}, ablage.All)
	if !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("upsert into a taken unique value: %v", err)
	}
}

func testTx(t *testing.T, open Opener) {
	s := setup(t, open)
	must(t, s.Tx(ctx, func(tx ablage.Store) error {
		insert(t, tx, "people", ablage.Row{"id": "a", "name": "Ada"})
		expectIDs(t, tx, "people", ablage.Query{}, "a") // sees its own write
		return tx.Tx(ctx, func(inner ablage.Store) error {
			insert(t, inner, "people", ablage.Row{"id": "b", "name": "B"})
			return nil
		})
	}))
	expectIDs(t, s, "people", ablage.Query{}, "a", "b")
	boom := errors.New("boom")
	err := s.Tx(ctx, func(tx ablage.Store) error {
		insert(t, tx, "people", ablage.Row{"id": "c", "name": "C"})
		if _, err := tx.Update(ctx, "people", ablage.Row{"name": "changed"}, ablage.All); err != nil {
			return err
		}
		if _, err := tx.Delete(ctx, "people", ablage.Eq("id", "a")); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Tx returned %v", err)
	}
	expectIDs(t, s, "people", ablage.Query{}, "a", "b")
	if get(t, s, "people", "a")["name"] != "Ada" {
		t.Error("rolled back update is visible")
	}
	if err := s.Tx(ctx, func(tx ablage.Store) error { return tx.Migrate(ctx) }); err == nil {
		t.Error("Migrate inside Tx accepted")
	}
}

func testErrors(t *testing.T, open Opener) {
	s := setup(t, open)
	if _, err := s.Find(ctx, "nope", ablage.Query{}); err == nil {
		t.Error("unknown table accepted")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Insert(cctx, "people", ablage.Row{"id": "x", "name": "x"}); err == nil {
		t.Error("insert with a cancelled context succeeded")
	}
}

// Indexes of different tables or fields never share a name, so none is
// silently skipped.
func testIndexNames(t *testing.T, open Opener) {
	s := setup(t, open,
		ablage.Table{Name: "user", Fields: []ablage.Field{{Name: "group_name", Type: ablage.String, Unique: true}}},
		ablage.Table{Name: "user_group", Fields: []ablage.Field{{Name: "name", Type: ablage.String, Unique: true}}},
		ablage.Table{Name: "pairs", Fields: []ablage.Field{
			{Name: "a", Type: ablage.String}, {Name: "b", Type: ablage.String}, {Name: "a_b", Type: ablage.String, Unique: true},
		}, Indexes: []ablage.Index{{Fields: []string{"a", "b"}, Unique: true}}},
	)
	insert(t, s, "user_group", ablage.Row{"id": "1", "name": "x"})
	if err := s.Insert(ctx, "user_group", ablage.Row{"id": "2", "name": "x"}); !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("user_group.name: %v", err)
	}
	insert(t, s, "user", ablage.Row{"id": "1", "group_name": "x"})
	if err := s.Insert(ctx, "user", ablage.Row{"id": "2", "group_name": "x"}); !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("user.group_name: %v", err)
	}
	insert(t, s, "pairs", ablage.Row{"id": "1", "a": "x", "b": "y", "a_b": "z"})
	if err := s.Insert(ctx, "pairs", ablage.Row{"id": "2", "a": "x", "b": "y", "a_b": "other"}); !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("pairs (a, b): %v", err)
	}
	if err := s.Insert(ctx, "pairs", ablage.Row{"id": "3", "a": "1", "b": "2", "a_b": "z"}); !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("pairs a_b: %v", err)
	}
}

// A failed statement inside a transaction does not end it.
func testTxContinuesAfterError(t *testing.T, open Opener) {
	projects := ablage.Table{Name: "projects"}
	tasks := ablage.Table{Name: "tasks", Fields: []ablage.Field{{Name: "project", Type: ablage.String, Ref: "projects"}}}
	s := setup(t, open, people, projects, tasks)
	must(t, s.Tx(ctx, func(tx ablage.Store) error {
		insert(t, tx, "people", ablage.Row{"id": "a", "name": "A"})
		if err := tx.Insert(ctx, "people", ablage.Row{"id": "a", "name": "again"}); !errors.Is(err, ablage.ErrDuplicate) {
			t.Errorf("duplicate in tx: %v", err)
		}
		if err := tx.Insert(ctx, "tasks", ablage.Row{"id": "t", "project": "nope"}); !errors.Is(err, ablage.ErrReference) {
			t.Errorf("reference in tx: %v", err)
		}
		insert(t, tx, "people", ablage.Row{"id": "b", "name": "B"})
		return nil
	}))
	expectIDs(t, s, "people", ablage.Query{}, "a", "b")
}

// A panic inside Tx rolls back and leaves the store usable.
func testTxPanic(t *testing.T, open Opener) {
	s := setup(t, open)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		_ = s.Tx(ctx, func(tx ablage.Store) error {
			insert(t, tx, "people", ablage.Row{"id": "a", "name": "A"})
			panic("boom")
		})
	}()
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	n, err := s.Count(tctx, "people", ablage.All)
	must(t, err)
	if n != 0 {
		t.Errorf("writes before the panic kept: %d", n)
	}
	must(t, s.Insert(tctx, "people", ablage.Row{"id": "b", "name": "B"}))
}

// Unique values are checked row by row, as SQL does: shifting values that
// collide on the way fails, and changes nothing.
func testUniqueRowByRow(t *testing.T, open Opener) {
	slots := ablage.Table{Name: "slots", Fields: []ablage.Field{{Name: "pos", Type: ablage.Int, Unique: true}}}
	s := setup(t, open, slots)
	insert(t, s, "slots", ablage.Row{"id": "1", "pos": 1}, ablage.Row{"id": "2", "pos": 2})
	if _, err := s.Update(ctx, "slots", ablage.Row{"pos": ablage.Incr(1)}, ablage.All); !errors.Is(err, ablage.ErrDuplicate) {
		t.Errorf("colliding shift: %v", err)
	}
	if get(t, s, "slots", "1")["pos"] != int64(1) || get(t, s, "slots", "2")["pos"] != int64(2) {
		t.Error("a failed update changed rows")
	}
}

// Values not every store can keep the same way are refused.
func testInvalidValues(t *testing.T, open Opener) {
	s := setup(t, open)
	bad := []ablage.Row{
		{"name": "a\xffb"},
		{"name": "a\x00b"},
		{"score": math.NaN()},
		{"born": time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"born": time.Date(0, 12, 31, 0, 0, 0, 0, time.UTC)},
		{"tags": json.RawMessage(`["a\u0000"]`)},
		{"tags": json.RawMessage(`{"a":`)},
	}
	for i, row := range bad {
		row["id"] = fmt.Sprint(i)
		if _, ok := row["name"]; !ok {
			row["name"] = "x"
		}
		if err := s.Insert(ctx, "people", row); err == nil {
			t.Errorf("accepted %q", row)
		}
	}
	if _, err := s.Find(ctx, "people", ablage.Query{Where: ablage.Prefix("name", "a\xff")}); err == nil {
		t.Error("invalid UTF-8 in a condition accepted")
	}
	// Infinity and escaped backslashes are fine.
	insert(t, s, "people", ablage.Row{"id": "ok", "name": "x", "score": math.Inf(1), "tags": json.RawMessage(`["\\u0000"]`)})
	if get(t, s, "people", "ok")["score"] != math.Inf(1) {
		t.Error("infinity lost")
	}
}

// Delete reports every row it matched, also when a cascade within the same
// table removed some of them first.
func testCascadeCount(t *testing.T, open Opener) {
	nodes := ablage.Table{Name: "nodes", Fields: []ablage.Field{
		{Name: "parent", Type: ablage.String, Nullable: true, Ref: "nodes", OnDelete: ablage.Cascade},
	}}
	s := setup(t, open, nodes)
	insert(t, s, "nodes", ablage.Row{"id": "a"}, ablage.Row{"id": "b", "parent": "a"}, ablage.Row{"id": "c", "parent": "b"},
		ablage.Row{"id": "d"})
	n, err := s.Delete(ctx, "nodes", ablage.Or(ablage.Eq("id", "a"), ablage.Eq("id", "b"), ablage.Eq("id", "c")))
	must(t, err)
	if n != 3 {
		t.Errorf("deleted %d, want 3", n)
	}
	expectIDs(t, s, "nodes", ablage.Query{}, "d")
}

// In takes more values than a database takes parameters in one statement,
// in every operation and nested in other conditions.
func testLongIn(t *testing.T, open Opener) {
	s := setup(t, open)
	must(t, s.Tx(ctx, func(tx ablage.Store) error {
		for i := range 500 {
			insert(t, tx, "people", ablage.Row{"id": fmt.Sprintf("p%06d", i), "name": fmt.Sprintf("n%05d", 1000-i), "age": i})
		}
		return nil
	}))
	// 70,000 ids (more than SQLite and Postgres take parameters), every second one of the rows among them, and duplicates.
	var list []string
	for i := range 70000 {
		list = append(list, fmt.Sprintf("p%06d", 2*i))
	}
	list = append(list, list[:1000]...)
	in := ablage.In("id", list...)
	count := func(c ablage.Cond, want int64) {
		t.Helper()
		n, err := s.Count(ctx, "people", c)
		must(t, err)
		if n != want {
			t.Errorf("count %d, want %d", n, want)
		}
	}
	count(in, 250)
	count(ablage.Not(in), 250)
	count(ablage.Or(in, ablage.Eq("id", "p000001")), 251)
	count(ablage.And(in, ablage.Lt("age", 100)), 50)
	expectIDs(t, s, "people", ablage.Query{Where: in, OrderBy: []ablage.Order{ablage.Asc("name")}, Limit: 3, Offset: 1},
		"p000496", "p000494", "p000492")
	n, err := s.Update(ctx, "people", ablage.Row{"visits": ablage.Incr(1)}, in)
	must(t, err)
	if n != 250 {
		t.Errorf("updated %d, want 250", n)
	}
	count(ablage.Eq("visits", 1), 250)
	ok, err := s.Upsert(ctx, "people", ablage.Row{"id": "p000002", "name": "x"}, ablage.Row{"name": "upserted"}, in)
	must(t, err)
	if !ok || get(t, s, "people", "p000002")["name"] != "upserted" {
		t.Errorf("upsert with a long list in where: ok=%v", ok)
	}
	n, err = s.Delete(ctx, "people", ablage.And(in, ablage.Lt("age", 100)))
	must(t, err)
	if n != 50 {
		t.Errorf("deleted %d, want 50", n)
	}
	count(ablage.All, 450)
}

// Long In lists hold values of every type that can be compared, including
// those that are hard to pass on as text.
func testLongInTypes(t *testing.T, open Opener) {
	s := setup(t, open)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	names := []string{`quote " and \ backslash`, "NULL", "", "{a,b}", "Ünal 🙂", "'; --"}
	floats := []float64{math.Inf(1), math.Inf(-1), 0.1, -1.5e300, 5e-324, 1 << 62}
	for i := range 6 {
		insert(t, s, "people", ablage.Row{
			"id": fmt.Sprint(i), "name": names[i], "age": int64(-1<<63) + int64(i), "score": floats[i],
			"born": base.Add(time.Duration(i) * time.Microsecond), "avatar": []byte{0, byte(i), 255},
		})
	}
	insert(t, s, "people", ablage.Row{"id": "null", "name": "other"}) // NULL everywhere it can be
	pad := func(n int, f func(i int) any) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = f(i)
		}
		return out
	}
	cases := []struct {
		name  string
		field string
		vals  []any
	}{
		{"string", "name", append(pad(500, func(i int) any { return fmt.Sprintf("x%d", i) }), names[0], names[1], names[2], names[3], names[4], names[5])},
		{"int", "age", append(pad(500, func(i int) any { return i }), int64(-1<<63), int64(-1<<63)+1, int64(-1<<63)+2, int64(-1<<63)+3, int64(-1<<63)+4, int64(-1<<63)+5)},
		{"float", "score", append(pad(500, func(i int) any { return float64(i) + 0.5 }), floats[0], floats[1], floats[2], floats[3], floats[4], floats[5])},
		{"time", "born", append(pad(500, func(i int) any { return base.Add(time.Hour * time.Duration(i+1)) }),
			base, base.Add(1*time.Microsecond), base.Add(2*time.Microsecond), base.Add(3*time.Microsecond), base.Add(4*time.Microsecond), base.Add(5*time.Microsecond))},
		{"bytes", "avatar", append(pad(500, func(i int) any { return []byte{1, byte(i), byte(i >> 8)} }),
			[]byte{0, 0, 255}, []byte{0, 1, 255}, []byte{0, 2, 255}, []byte{0, 3, 255}, []byte{0, 4, 255}, []byte{0, 5, 255})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// All six rows, then without the first one, and the rest.
			expectIDs(t, s, "people", ablage.Query{Where: ablage.In(tc.field, tc.vals...)}, "0", "1", "2", "3", "4", "5")
			expectIDs(t, s, "people", ablage.Query{Where: ablage.In(tc.field, tc.vals[:len(tc.vals)-1]...)}, "0", "1", "2", "3", "4")
			expectIDs(t, s, "people", ablage.Query{Where: ablage.Not(ablage.In(tc.field, tc.vals[:len(tc.vals)-1]...))}, "5", "null")
		})
	}
}

// Aggregations follow Aggregation.Apply: NULLs ignored, sums of nothing 0,
// minimum and maximum of nothing NULL, groups sorted by their keys.
func testAggregate(t *testing.T, open Opener) {
	s := setup(t, open)
	ag, ok := s.(ablage.Aggregator)
	if !ok {
		t.Skip("the store is not an ablage.Aggregator")
	}
	seedPeople(t, s)
	born := time.Date(1906, 12, 9, 0, 0, 0, 0, time.UTC)
	_, err := s.Update(ctx, "people", ablage.Row{"born": born, "visits": 7}, ablage.Eq("id", "3"))
	must(t, err)
	_, err = s.Update(ctx, "people", ablage.Row{"born": born.AddDate(10, 0, 0), "visits": -2}, ablage.Eq("id", "4"))
	must(t, err)
	all := []ablage.Agg{
		ablage.CountAll("n"), ablage.Count("age", "aged"), ablage.Sum("age", "ages"), ablage.Sum("visits", "visits"),
		ablage.Min("age", "youngest"), ablage.Max("age", "oldest"), ablage.Min("name", "first"), ablage.Max("name", "last"),
		ablage.Min("score", "low"), ablage.Max("score", "high"), ablage.Min("born", "born_first"), ablage.Max("born", "born_last"),
	}
	cases := []struct {
		name string
		a    ablage.Aggregation
		want []ablage.Row
	}{
		{"all rows", ablage.Aggregation{Values: all}, []ablage.Row{{
			"n": int64(5), "aged": int64(4), "ages": int64(198), "visits": int64(5), "youngest": int64(36), "oldest": int64(85),
			"first": "Ada", "last": "Ünal", "low": -2.0, "high": 1.5, "born_first": born, "born_last": born.AddDate(10, 0, 0),
		}}},
		{"no rows", ablage.Aggregation{Where: ablage.Eq("id", "nope"), Values: all}, []ablage.Row{{
			"n": int64(0), "aged": int64(0), "ages": int64(0), "visits": int64(0), "youngest": nil, "oldest": nil,
			"first": nil, "last": nil, "low": nil, "high": nil, "born_first": nil, "born_last": nil,
		}}},
		{"only NULLs", ablage.Aggregation{Where: ablage.Eq("id", "4"), Values: []ablage.Agg{ablage.Count("age", "c"), ablage.Sum("age", "s"), ablage.Max("age", "m")}},
			[]ablage.Row{{"c": int64(0), "s": int64(0), "m": nil}}},
		{"group by bool", ablage.Aggregation{GroupBy: []string{"active"}, Values: []ablage.Agg{ablage.CountAll("n"), ablage.Sum("age", "ages")}},
			[]ablage.Row{{"active": false, "n": int64(2), "ages": int64(41)}, {"active": true, "n": int64(3), "ages": int64(157)}}},
		{"group by with NULL first", ablage.Aggregation{GroupBy: []string{"age"}, Values: []ablage.Agg{ablage.CountAll("n"), ablage.Max("name", "name")}},
			[]ablage.Row{
				{"age": nil, "n": int64(1), "name": "grace"}, {"age": int64(36), "n": int64(2), "name": "Ünal"},
				{"age": int64(41), "n": int64(1), "name": "Alan"}, {"age": int64(85), "n": int64(1), "name": "Grace"},
			}},
		{"group by two fields, with where", ablage.Aggregation{Where: ablage.Ne("id", "2"), GroupBy: []string{"active", "email"}, Values: []ablage.Agg{ablage.CountAll("n")}},
			[]ablage.Row{
				{"active": false, "email": nil, "n": int64(1)}, {"active": true, "email": nil, "n": int64(1)},
				{"active": true, "email": "ada@x", "n": int64(1)}, {"active": true, "email": "u@x", "n": int64(1)},
			}},
		{"group by strings in byte order", ablage.Aggregation{GroupBy: []string{"name"}},
			[]ablage.Row{{"name": "Ada"}, {"name": "Alan"}, {"name": "Grace"}, {"name": "grace"}, {"name": "Ünal"}}},
		{"groups of no rows", ablage.Aggregation{Where: ablage.Eq("id", "nope"), GroupBy: []string{"age"}, Values: all}, []ablage.Row{}},
		{"long list in where", ablage.Aggregation{Where: ablage.In("id", append(pad(1000), "1", "5")...), GroupBy: []string{"age"}, Values: []ablage.Agg{ablage.CountAll("n")}},
			[]ablage.Row{{"age": int64(36), "n": int64(2)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ag.Aggregate(ctx, "people", tc.a)
			must(t, err)
			if got == nil || fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
			// The package function does the same on any store.
			again, err := ablage.Aggregate(ctx, s, "people", tc.a)
			must(t, err)
			if fmt.Sprint(again) != fmt.Sprint(got) {
				t.Errorf("ablage.Aggregate: %v", again)
			}
		})
	}
	bad := []ablage.Aggregation{
		{},
		{Values: []ablage.Agg{ablage.Sum("name", "x")}},
		{Values: []ablage.Agg{ablage.Sum("score", "x")}},
		{Values: []ablage.Agg{ablage.Min("active", "x")}},
		{Values: []ablage.Agg{ablage.Max("avatar", "x")}},
		{Values: []ablage.Agg{ablage.Max("tags", "x")}},
		{Values: []ablage.Agg{ablage.Count("nope", "x")}},
		{Values: []ablage.Agg{ablage.CountAll("Bad-Name")}},
		{Values: []ablage.Agg{ablage.CountAll("")}},
		{Values: []ablage.Agg{ablage.CountAll("x"), ablage.Count("age", "x")}},
		{GroupBy: []string{"age"}, Values: []ablage.Agg{ablage.CountAll("age")}},
		{GroupBy: []string{"tags"}},
		{GroupBy: []string{"age", "age"}},
		{GroupBy: []string{"nope"}},
		{Values: []ablage.Agg{{Op: 0, As: "x"}}},
		{Where: ablage.Eq("nope", 1), Values: []ablage.Agg{ablage.CountAll("n")}},
	}
	for _, a := range bad {
		if _, err := ag.Aggregate(ctx, "people", a); err == nil {
			t.Errorf("invalid aggregation %+v accepted", a)
		}
	}
	// A sum fits if the total fits, whatever the order of adding up; one
	// that does not fit into an int64 fails.
	insert(t, s, "people", ablage.Row{"id": "big1", "name": "big", "visits": int64(math.MaxInt64)})
	insert(t, s, "people", ablage.Row{"id": "big2", "name": "big", "visits": int64(math.MaxInt64) - 5})
	insert(t, s, "people", ablage.Row{"id": "big3", "name": "big", "visits": int64(math.MinInt64)})
	got, err := ag.Aggregate(ctx, "people", ablage.Aggregation{Values: []ablage.Agg{ablage.Sum("visits", "v")}})
	must(t, err)
	if want := int64(math.MaxInt64) - 1; got[0]["v"] != want {
		t.Errorf("sum %v, want %d", got[0]["v"], want)
	}
	must(t, s.Insert(ctx, "people", ablage.Row{"id": "big4", "name": "big", "visits": 2}))
	if _, err := ag.Aggregate(ctx, "people", ablage.Aggregation{Values: []ablage.Agg{ablage.Sum("visits", "v")}}); err == nil {
		t.Error("overflowing sum accepted")
	}
}

func pad(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("pad%d", i)
	}
	return out
}
