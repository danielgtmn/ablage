package ablage_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/danielgtmn/ablage"
	"github.com/danielgtmn/ablage/memstore"
)

type Project struct {
	ID        string
	Version   int64      `db:",version"`
	CreatedAt time.Time  `db:",created"`
	UpdatedAt time.Time  `db:",updated"`
	DeletedAt *time.Time `db:",deleted"`
	TenantID  string     `db:",index"`
	Name      string
	Labels    []string
}

type tenantCtx struct{}

func Example() {
	ctx := context.WithValue(context.Background(), tenantCtx{}, "acme")
	store := memstore.New() // or sqlstore.New(db, sqlstore.SQLite / sqlstore.Postgres)
	projects, err := ablage.NewRepository[Project](store, "projects",
		ablage.WithUniqueIndex("tenant_id", "name"),
		ablage.WithScope(func(ctx context.Context) (ablage.Cond, error) {
			t, _ := ctx.Value(tenantCtx{}).(string)
			if t == "" {
				return ablage.All, errors.New("no tenant")
			}
			return ablage.Eq("tenant_id", t), nil
		}),
	)
	if err != nil {
		panic(err)
	}
	if err := store.Migrate(ctx); err != nil {
		panic(err)
	}

	p := Project{TenantID: "acme", Name: "Website", Labels: []string{"web"}}
	if err := projects.Create(ctx, &p); err != nil {
		panic(err)
	}
	dup := Project{TenantID: "acme", Name: "Website"}
	fmt.Println(errors.Is(projects.Create(ctx, &dup), ablage.ErrDuplicate))

	p, _ = projects.Update(ctx, p.ID, func(p *Project) error {
		p.Name = "Web site"
		return nil
	})
	fmt.Println(p.Name, p.Version)

	list, _ := projects.Find(ctx, ablage.Query{Where: ablage.Prefix("name", "Web")})
	fmt.Println(len(list), list[0].Labels)

	other := Project{TenantID: "globex", Name: "Intranet"}
	fmt.Println(errors.Is(projects.Create(ctx, &other), ablage.ErrOutOfScope))

	_ = projects.Delete(ctx, p.ID)
	n, _ := projects.Count(ctx, ablage.All)
	fmt.Println(n)
	// Output:
	// true
	// Web site 2
	// 1 [web]
	// true
	// 0
}
