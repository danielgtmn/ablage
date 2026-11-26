package ablage_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtmn/ablage"
	"github.com/danielgtmn/ablage/internal/testdb"
)

type Status string

type Address struct {
	City string `json:"city"`
	Zip  string `json:"zip"`
}

type Base struct {
	ID        string    `db:"id"`
	Version   int64     `db:"version,version"`
	CreatedAt time.Time `db:",created"`
	UpdatedAt time.Time `db:",updated"`
}

type Customer struct {
	Base
	TenantID  string `db:",index"`
	Email     string `db:",unique"`
	Name      string
	Status    Status
	Credit    uint32
	Rating    *float64
	Tags      []string
	Address   Address
	Meta      map[string]int
	IP        netip.Addr
	Avatar    []byte
	DeletedAt *time.Time `db:",deleted"`
	ignored   string     //nolint:unused // unexported: not stored
	Skipped   string     `db:"-"`
}

type tenantKey struct{}

func withTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenant)
}

func tenantScope(ctx context.Context) (ablage.Cond, error) {
	t, _ := ctx.Value(tenantKey{}).(string)
	if t == "" {
		return ablage.All, errors.New("no tenant in context")
	}
	return ablage.Eq("tenant_id", t), nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

func forEach(t *testing.T, fn func(t *testing.T, s ablage.Store)) {
	for _, b := range testdb.All() {
		t.Run(b.Name, func(t *testing.T) { fn(t, b.Open(t)) })
	}
}

func customers(t *testing.T, s ablage.Store, opts ...ablage.Option) *ablage.Repository[Customer] {
	t.Helper()
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	opts = append([]ablage.Option{ablage.WithScope(tenantScope), ablage.WithClock(c.now)}, opts...)
	r, err := ablage.NewRepository[Customer](s, "customers", opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func expectErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("got error %v, want %v", err, want)
	}
}

var (
	t1 = withTenant(context.Background(), "t1")
	t2 = withTenant(context.Background(), "t2")
)

func TestTable(t *testing.T) {
	r := customers(t, ablagememory())
	tb := r.Table()
	var names []string
	for _, f := range tb.Fields {
		names = append(names, f.Name+":"+f.Type.String())
	}
	want := "version:int created_at:time updated_at:time tenant_id:string email:string name:string status:string credit:int rating:float tags:json address:json meta:json ip:string avatar:bytes deleted_at:time"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("fields\n got %s\nwant %s", got, want)
	}
	if tb.SoftDelete != "deleted_at" {
		t.Errorf("soft delete %q", tb.SoftDelete)
	}
}

func ablagememory() ablage.Store { return testdb.All()[0].Open(nil) }

func TestCreateGet(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		rating := 4.5
		c := Customer{
			TenantID: "t1", Email: "ada@x", Name: "Ada", Status: "active", Credit: 7, Rating: &rating,
			Tags: []string{"vip"}, Address: Address{"Köln", "50667"}, Meta: map[string]int{"a": 1},
			IP: netip.MustParseAddr("192.0.2.1"), Avatar: []byte{1, 2},
		}
		must(t, r.Create(t1, &c))
		if len(c.ID) != 26 || c.Version != 1 || c.CreatedAt.IsZero() || !c.CreatedAt.Equal(c.UpdatedAt) {
			t.Errorf("after create: %+v", c.Base)
		}
		got, err := r.Get(t1, c.ID)
		must(t, err)
		if got.Name != "Ada" || got.Status != "active" || got.Credit != 7 || *got.Rating != 4.5 ||
			!slices.Equal(got.Tags, []string{"vip"}) || got.Address.City != "Köln" || got.Meta["a"] != 1 ||
			got.IP != c.IP || string(got.Avatar) != "\x01\x02" || got.DeletedAt != nil || got.Version != 1 ||
			!got.CreatedAt.Equal(c.CreatedAt) {
			t.Errorf("got %+v", got)
		}
		// Ids sort by creation time.
		d := Customer{TenantID: "t1", Email: "b@x"}
		must(t, r.Create(t1, &d))
		if d.ID <= c.ID {
			t.Errorf("ids not ascending: %s then %s", c.ID, d.ID)
		}
		// Explicit id; duplicate id and email.
		e := Customer{Base: Base{ID: "fixed"}, TenantID: "t1", Email: "e@x"}
		must(t, r.Create(t1, &e))
		expectErr(t, r.Create(t1, &Customer{Base: Base{ID: "fixed"}, TenantID: "t1", Email: "f@x"}), ablage.ErrDuplicate)
		expectErr(t, r.Create(t1, &Customer{TenantID: "t1", Email: "ada@x"}), ablage.ErrDuplicate)
		_, err = r.Get(t1, "nope")
		expectErr(t, err, ablage.ErrNotFound)
		many, err := r.GetMany(t1, c.ID, "fixed", "nope")
		must(t, err)
		if len(many) != 2 {
			t.Errorf("GetMany: %d", len(many))
		}
	})
}

func TestScope(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		a := Customer{TenantID: "t1", Email: "a@x", Name: "A"}
		must(t, r.Create(t1, &a))
		b := Customer{TenantID: "t2", Email: "b@x", Name: "B"}
		must(t, r.Create(t2, &b))

		// Writing outside the scope.
		expectErr(t, r.Create(t1, &Customer{TenantID: "t2", Email: "c@x"}), ablage.ErrOutOfScope)
		moved := a
		moved.TenantID = "t2"
		expectErr(t, r.Replace(t1, &moved), ablage.ErrOutOfScope)
		if err := r.Patch(t1, a.ID, ablage.Row{"tenant_id": "t2"}); err == nil {
			t.Error("patching the scope field accepted")
		}

		// Reading and writing another tenant's entity.
		_, err := r.Get(t1, b.ID)
		expectErr(t, err, ablage.ErrNotFound)
		list, err := r.Find(t1, ablage.Query{})
		must(t, err)
		if len(list) != 1 || list[0].ID != a.ID {
			t.Errorf("t1 sees %v", list)
		}
		n, err := r.Count(t2, ablage.All)
		must(t, err)
		if n != 1 {
			t.Errorf("t2 counts %d", n)
		}
		other := b
		other.TenantID = "t1"
		expectErr(t, r.Replace(t1, &other), ablage.ErrNotFound)
		expectErr(t, r.Patch(t1, b.ID, ablage.Row{"name": "x"}), ablage.ErrNotFound)
		expectErr(t, r.Delete(t1, b.ID), ablage.ErrNotFound)
		expectErr(t, r.Purge(t1, b.ID), ablage.ErrNotFound)
		_, err = r.Update(t1, b.ID, func(c *Customer) error { return nil })
		expectErr(t, err, ablage.ErrNotFound)
		upsert := Customer{Base: Base{ID: b.ID}, TenantID: "t1", Email: "z@x"}
		expectErr(t, r.Upsert(t1, &upsert), ablage.ErrOutOfScope)
		if got, _ := r.Get(t2, b.ID); got.Name != "B" || got.Email != "b@x" {
			t.Errorf("t1 changed t2's entity: %+v", got)
		}

		// A missing tenant fails instead of seeing everything.
		if _, err := r.Find(context.Background(), ablage.Query{}); err == nil || !strings.Contains(err.Error(), "no tenant") {
			t.Errorf("no tenant: %v", err)
		}
		// Unscoped sees all.
		all, err := r.Unscoped().Find(context.Background(), ablage.Query{})
		must(t, err)
		if len(all) != 2 {
			t.Errorf("unscoped sees %d", len(all))
		}
	})
}

func TestReplaceVersion(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		c := Customer{TenantID: "t1", Email: "a@x", Name: "A"}
		must(t, r.Create(t1, &c))
		created := c.CreatedAt
		stale := c
		c.Name = "B"
		c.CreatedAt = time.Time{} // ignored by Replace
		must(t, r.Replace(t1, &c))
		if c.Version != 2 || !c.UpdatedAt.After(created) {
			t.Errorf("after replace: %+v", c.Base)
		}
		stale.Name = "C"
		expectErr(t, r.Replace(t1, &stale), ablage.ErrStale)
		got, _ := r.Get(t1, c.ID)
		if got.Name != "B" || got.Version != 2 || !got.CreatedAt.Equal(created) {
			t.Errorf("stored %+v", got)
		}
		expectErr(t, r.Replace(t1, &Customer{Base: Base{ID: "nope"}, TenantID: "t1"}), ablage.ErrNotFound)
	})
}

func TestUpdateRetries(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		c := Customer{TenantID: "t1", Email: "a@x"}
		must(t, r.Create(t1, &c))
		calls := 0
		got, err := r.Update(t1, c.ID, func(x *Customer) error {
			calls++
			if calls == 1 { // another writer gets in between
				must(t, r.Patch(t1, c.ID, ablage.Row{"name": "other"}))
			}
			x.Credit++
			return nil
		})
		must(t, err)
		if calls != 2 || got.Credit != 1 || got.Name != "other" || got.Version != 3 {
			t.Errorf("calls %d, got %+v", calls, got)
		}
		// Gives up after the configured attempts.
		r2 := r.With(s)
		_, err = r2.Update(t1, c.ID, func(x *Customer) error {
			must(t, r.Patch(t1, c.ID, ablage.Row{"credit": x.Credit + 10}))
			return nil
		})
		expectErr(t, err, ablage.ErrStale)
		boom := errors.New("boom")
		_, err = r.Update(t1, c.ID, func(*Customer) error { return boom })
		expectErr(t, err, boom)
		_, err = r.Update(t1, c.ID, func(x *Customer) error { x.ID = "other"; return nil })
		if err == nil {
			t.Error("changing the id in Update accepted")
		}
	})
}

func TestPatch(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		c := Customer{TenantID: "t1", Email: "a@x"}
		must(t, r.Create(t1, &c))
		rating := 2.5
		must(t, r.Patch(t1, c.ID, ablage.Row{
			"name": "N", "status": Status("gone"), "rating": &rating, "tags": []string{"x", "y"},
			"address": Address{City: "Bonn"}, "ip": netip.MustParseAddr("::1"), "credit": uint32(9),
		}))
		got, _ := r.Get(t1, c.ID)
		if got.Name != "N" || got.Status != "gone" || *got.Rating != 2.5 || !slices.Equal(got.Tags, []string{"x", "y"}) ||
			got.Address.City != "Bonn" || got.IP.String() != "::1" || got.Credit != 9 || got.Version != 2 ||
			!got.UpdatedAt.After(c.UpdatedAt) {
			t.Errorf("after patch %+v", got)
		}
		must(t, r.Patch(t1, c.ID, ablage.Row{"rating": nil}))
		if got, _ := r.Get(t1, c.ID); got.Rating != nil {
			t.Error("rating not cleared")
		}
		for _, set := range []ablage.Row{{}, {"version": 9}, {"deleted_at": time.Now()}, {"id": "x"}, {"nope": 1}, {"credit": "x"}} {
			if err := r.Patch(t1, c.ID, set); err == nil {
				t.Errorf("patch %v accepted", set)
			}
		}
		expectErr(t, r.Patch(t1, "nope", ablage.Row{"name": "x"}), ablage.ErrNotFound)
	})
}

func TestSoftDelete(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		c := Customer{TenantID: "t1", Email: "a@x"}
		must(t, r.Create(t1, &c))
		must(t, r.Delete(t1, c.ID))
		_, err := r.Get(t1, c.ID)
		expectErr(t, err, ablage.ErrNotFound)
		expectErr(t, r.Delete(t1, c.ID), ablage.ErrNotFound)
		del, err := r.WithDeleted().Get(t1, c.ID)
		must(t, err)
		if del.DeletedAt == nil || del.Version != 2 {
			t.Errorf("deleted entity %+v", del)
		}
		// The email is free again.
		d := Customer{TenantID: "t1", Email: "a@x"}
		must(t, r.Create(t1, &d))
		expectErr(t, r.Restore(t1, c.ID), ablage.ErrDuplicate)
		must(t, r.Purge(t1, d.ID))
		must(t, r.Restore(t1, c.ID))
		if got, err := r.Get(t1, c.ID); err != nil || got.DeletedAt != nil {
			t.Errorf("restored: %+v %v", got, err)
		}
		expectErr(t, r.Restore(t1, c.ID), ablage.ErrNotFound)
		// DeleteWhere.
		for _, e := range []string{"x@x", "y@x", "z@x"} {
			must(t, r.Create(t1, &Customer{TenantID: "t1", Email: e, Name: "bulk"}))
		}
		must(t, r.Create(t2, &Customer{TenantID: "t2", Email: "w@x", Name: "bulk"}))
		n, err := r.DeleteWhere(t1, ablage.Eq("name", "bulk"))
		must(t, err)
		if n != 3 {
			t.Errorf("DeleteWhere: %d", n)
		}
		if n, _ := r.Count(t2, ablage.All); n != 1 {
			t.Errorf("DeleteWhere crossed tenants: t2 has %d", n)
		}
		// Purge removes for good, also deleted ones.
		must(t, r.Delete(t1, c.ID))
		must(t, r.Purge(t1, c.ID))
		_, err = r.WithDeleted().Get(t1, c.ID)
		expectErr(t, err, ablage.ErrNotFound)
	})
}

func TestUpsert(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		c := Customer{Base: Base{ID: "k"}, TenantID: "t1", Email: "a@x", Name: "first"}
		must(t, r.Upsert(t1, &c))
		if c.Version != 1 || c.CreatedAt.IsZero() {
			t.Errorf("after insert %+v", c.Base)
		}
		created := c.CreatedAt
		must(t, r.Delete(t1, "k"))
		again := Customer{Base: Base{ID: "k"}, TenantID: "t1", Email: "a@x", Name: "second"}
		must(t, r.Upsert(t1, &again))
		if again.Version != 3 || !again.CreatedAt.Equal(created) || again.Name != "second" || again.DeletedAt != nil {
			t.Errorf("after update %+v", again)
		}
	})
}

func TestTx(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		boom := errors.New("boom")
		err := s.Tx(t1, func(tx ablage.Store) error {
			must(t, r.With(tx).Create(t1, &Customer{TenantID: "t1", Email: "a@x"}))
			if n, _ := r.With(tx).Count(t1, ablage.All); n != 1 {
				t.Errorf("tx sees %d", n)
			}
			return boom
		})
		expectErr(t, err, boom)
		if n, _ := r.Count(t1, ablage.All); n != 0 {
			t.Errorf("rolled back create visible: %d", n)
		}
		must(t, s.Tx(t1, func(tx ablage.Store) error {
			return r.With(tx).Create(t1, &Customer{TenantID: "t1", Email: "a@x"})
		}))
		if n, _ := r.Count(t1, ablage.All); n != 1 {
			t.Errorf("committed create missing: %d", n)
		}
	})
}

// Concurrent Update calls on one entity lose no increment.
func TestConcurrentUpdate(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s, ablage.WithUpdateAttempts(100))
		c := Customer{TenantID: "t1", Email: "a@x"}
		must(t, r.Create(t1, &c))
		var wg sync.WaitGroup
		var failed atomic.Int32
		for range 8 {
			wg.Go(func() {
				for range 5 {
					if _, err := r.Update(t1, c.ID, func(x *Customer) error { x.Credit++; return nil }); err != nil {
						failed.Add(1)
						t.Log(err)
					}
				}
			})
		}
		wg.Wait()
		got, _ := r.Get(t1, c.ID)
		if failed.Load() != 0 || got.Credit != 40 {
			t.Errorf("credit %d after 40 increments, %d failed", got.Credit, failed.Load())
		}
	})
}

func TestMappingErrors(t *testing.T) {
	s := ablagememory()
	type noID struct{ Name string }
	type badVersion struct {
		ID string
		V  string `db:",version"`
	}
	type twoIDs struct {
		ID  string
		Key string `db:",id"`
		X   string `db:"id"`
	}
	type badChan struct {
		ID string
		C  chan int
	}
	type dupCol struct {
		ID string
		A  string `db:"x"`
		B  string `db:"x"`
	}
	type badTag struct {
		ID string `db:",nope"`
	}
	if _, err := ablage.NewRepository[noID](s, "a"); err == nil {
		t.Error("no id accepted")
	}
	if _, err := ablage.NewRepository[badVersion](s, "b"); err == nil {
		t.Error("string version accepted")
	}
	if _, err := ablage.NewRepository[twoIDs](s, "c"); err == nil {
		t.Error("column id on a non-id field accepted")
	}
	if _, err := ablage.NewRepository[badChan](s, "d"); err == nil {
		t.Error("chan field accepted")
	}
	if _, err := ablage.NewRepository[dupCol](s, "e"); err == nil {
		t.Error("two fields on one column accepted")
	}
	if _, err := ablage.NewRepository[badTag](s, "f"); err == nil {
		t.Error("unknown tag option accepted")
	}
	type overflow struct {
		ID string
		N  uint64
	}
	r, err := ablage.NewRepository[overflow](s, "g")
	must(t, err)
	must(t, s.Migrate(context.Background()))
	if err := r.Create(context.Background(), &overflow{N: 1 << 63}); err == nil {
		t.Error("uint64 overflow accepted")
	}
}

func TestNewID(t *testing.T) {
	seen := map[string]bool{}
	prev := ""
	for range 1000 {
		id := ablage.NewID()
		if len(id) != 26 || strings.Trim(id, "0123456789abcdefghjkmnpqrstvwxyz") != "" || seen[id] {
			t.Fatalf("bad id %q", id)
		}
		seen[id] = true
		if id <= prev {
			t.Fatalf("not ascending: %s after %s", id, prev)
		}
		prev = id
	}
}

func TestManagedFieldsGuarded(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		c := Customer{TenantID: "t1", Email: "a@x"}
		must(t, r.Create(t1, &c))
		now := time.Now()
		got, err := r.Update(t1, c.ID, func(x *Customer) error {
			x.DeletedAt = &now
			x.CreatedAt = time.Time{}
			x.Version = 99
			x.Name = "changed"
			return nil
		})
		must(t, err)
		stored, _ := r.Get(t1, c.ID)
		if got.DeletedAt != nil || !got.CreatedAt.Equal(stored.CreatedAt) || got.Version != stored.Version || got.Name != "changed" {
			t.Errorf("returned %+v, stored %+v", got, stored)
		}
		// Unscoped Patch works without a tenant.
		must(t, r.Unscoped().Patch(context.Background(), c.ID, ablage.Row{"name": "job"}))
	})
}

func TestPatchChecksTheGoType(t *testing.T) {
	type small struct {
		ID   string
		N    int8
		U    uint16
		Addr netip.Addr
	}
	s := ablagememory()
	r, err := ablage.NewRepository[small](s, "smalls")
	must(t, err)
	must(t, s.Migrate(context.Background()))
	ctx := context.Background()
	e := small{}
	must(t, r.Create(ctx, &e))
	for _, set := range []ablage.Row{{"n": 1000}, {"u": -1}, {"addr": "not an address"}} {
		if err := r.Patch(ctx, e.ID, set); err == nil {
			t.Errorf("patch %v accepted", set)
		}
	}
	if _, err := r.Get(ctx, e.ID); err != nil {
		t.Errorf("entity unreadable: %v", err)
	}
}

// GetMany takes as many ids as an app has, e.g. every row a page of other
// rows refers to, and still applies scope and soft delete.
func TestGetManyLong(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		var ids []string
		must(t, s.Tx(t1, func(tx ablage.Store) error {
			for i := range 200 {
				ctx, tenant := t1, "t1"
				if i%4 == 0 {
					ctx, tenant = t2, "t2"
				}
				c := Customer{TenantID: tenant, Email: strconv.Itoa(i)}
				must(t, r.With(tx).Create(ctx, &c))
				ids = append(ids, c.ID)
			}
			return nil
		}))
		must(t, r.Delete(t1, ids[1]))
		for i := range 70000 {
			ids = append(ids, "missing"+strconv.Itoa(i))
		}
		got, err := r.GetMany(t1, ids...)
		must(t, err)
		if len(got) != 149 {
			t.Errorf("GetMany: %d, want 149", len(got))
		}
		n, err := r.Count(t1, ablage.In("id", ids...))
		must(t, err)
		if n != 149 {
			t.Errorf("Count: %d, want 149", n)
		}
	})
}

// An Incr in Patch adds to the field in one step, also when writers race.
func TestPatchIncr(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		c := Customer{TenantID: "t1", Email: "a@x", Credit: 5}
		must(t, r.Create(t1, &c))
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				if err := r.Patch(t1, c.ID, ablage.Row{"credit": ablage.Incr(2)}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		must(t, r.Patch(t1, c.ID, ablage.Row{"credit": ablage.Incr(-3), "name": "N"}))
		got, err := r.Get(t1, c.ID)
		must(t, err)
		if got.Credit != 22 || got.Name != "N" || got.Version != 12 || !got.UpdatedAt.After(c.UpdatedAt) {
			t.Errorf("after patches: credit %d, name %q, version %d", got.Credit, got.Name, got.Version)
		}
		for _, set := range []ablage.Row{{"name": ablage.Incr(1)}, {"rating": ablage.Incr(1)}, {"version": ablage.Incr(1)}} {
			if err := r.Patch(t1, c.ID, set); err == nil {
				t.Errorf("patch %v accepted", set)
			}
		}
	})
}

func TestUpdateWhere(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		var ids []string
		for i, tenant := range []string{"t1", "t1", "t1", "t2"} {
			c := Customer{TenantID: tenant, Email: strconv.Itoa(i), Name: "bulk", Credit: 1}
			must(t, r.Create(withTenant(context.Background(), tenant), &c))
			ids = append(ids, c.ID)
		}
		must(t, r.Delete(t1, ids[2]))
		n, err := r.UpdateWhere(t1, ablage.Eq("name", "bulk"), ablage.Row{"status": Status("x"), "credit": ablage.Incr(10)})
		must(t, err)
		if n != 2 {
			t.Errorf("changed %d, want 2 (live, in scope)", n)
		}
		list, err := r.Unscoped().WithDeleted().Find(context.Background(), ablage.Query{})
		must(t, err)
		for i, c := range list {
			changed := i < 2
			if (c.Status == "x") != changed || (c.Credit == 11) != changed || (c.Version == 2) != (i < 3) {
				t.Errorf("entity %d: status %q, credit %d, version %d", i, c.Status, c.Credit, c.Version)
			}
			if changed && !c.UpdatedAt.After(c.CreatedAt) {
				t.Errorf("entity %d: updated not stamped", i)
			}
		}
		// WithDeleted reaches the deleted one, Unscoped every tenant.
		n, err = r.WithDeleted().UpdateWhere(t1, ablage.Eq("id", ids[2]), ablage.Row{"name": "gone"})
		must(t, err)
		if n != 1 {
			t.Errorf("WithDeleted changed %d", n)
		}
		n, err = r.Unscoped().UpdateWhere(context.Background(), ablage.All, ablage.Row{"credit": ablage.Incr(1)})
		must(t, err)
		if n != 3 {
			t.Errorf("Unscoped changed %d, want 3", n)
		}
		for _, set := range []ablage.Row{{}, {"id": "x"}, {"version": 1}, {"updated_at": time.Now()}, {"deleted_at": nil},
			{"tenant_id": "t2"}, {"nope": 1}, {"name": ablage.Incr(1)}} {
			if _, err := r.UpdateWhere(t1, ablage.All, set); err == nil {
				t.Errorf("update %v accepted", set)
			}
		}
		if _, err := r.UpdateWhere(context.Background(), ablage.All, ablage.Row{"name": "x"}); err == nil {
			t.Error("update without a tenant accepted")
		}
	})
}

func TestAggregate(t *testing.T) {
	forEach(t, func(t *testing.T, s ablage.Store) {
		r := customers(t, s)
		for i, c := range []Customer{
			{TenantID: "t1", Status: "a", Credit: 1}, {TenantID: "t1", Status: "a", Credit: 2},
			{TenantID: "t1", Status: "b", Credit: 4}, {TenantID: "t1", Status: "b", Credit: 8},
			{TenantID: "t2", Status: "a", Credit: 16},
		} {
			c.Email = strconv.Itoa(i)
			must(t, r.Create(withTenant(context.Background(), c.TenantID), &c))
			if i == 3 {
				must(t, r.Delete(t1, c.ID))
			}
		}
		a := ablage.Aggregation{GroupBy: []string{"status"}, Values: []ablage.Agg{ablage.CountAll("n"), ablage.Sum("credit", "credit")}}
		check := func(r *ablage.Repository[Customer], ctx context.Context, want string) {
			t.Helper()
			got, err := r.Aggregate(ctx, a)
			must(t, err)
			if fmt.Sprint(got) != want {
				t.Errorf("got %v, want %s", got, want)
			}
		}
		check(r, t1, "[map[credit:3 n:2 status:a] map[credit:4 n:1 status:b]]")
		check(r.WithDeleted(), t1, "[map[credit:3 n:2 status:a] map[credit:12 n:2 status:b]]")
		check(r.Unscoped(), context.Background(), "[map[credit:19 n:3 status:a] map[credit:4 n:1 status:b]]")
		if _, err := r.Aggregate(context.Background(), a); err == nil {
			t.Error("aggregate without a tenant accepted")
		}
	})
}

// plainStore hides every method beyond ablage.Store.
type plainStore struct{ ablage.Store }

func TestAggregateUnsupported(t *testing.T) {
	r, err := ablage.NewRepository[Customer](plainStore{ablagememory()}, "customers")
	must(t, err)
	must(t, r.Store().Migrate(context.Background()))
	_, err = r.Aggregate(context.Background(), ablage.Aggregation{Values: []ablage.Agg{ablage.CountAll("n")}})
	expectErr(t, err, ablage.ErrUnsupported)
}
