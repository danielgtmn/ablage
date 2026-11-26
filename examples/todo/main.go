// Command todo is a small to-do list on ablage: one repository, a tenant
// scope, soft delete and optimistic updates, on SQLite or Postgres.
//
//	go run ./examples/todo -list home add "Buy milk"
//	go run ./examples/todo -list home ls
//	go run ./examples/todo -list home done <id>
//	go run ./examples/todo -list home rm <id>
//	go run ./examples/todo -list home undo <id>
//
// The data is kept in todo.db; with -postgres DSN in Postgres instead.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/danielgtmn/ablage"
	"github.com/danielgtmn/ablage/sqlstore"
)

// Todo is one entry of a list.
type Todo struct {
	ID        string
	Version   int64      `db:",version"`
	CreatedAt time.Time  `db:",created"`
	UpdatedAt time.Time  `db:",updated"`
	DeletedAt *time.Time `db:",deleted"`
	List      string     `db:",index"`
	Title     string
	Done      bool
	Tags      []string
}

type listKey struct{}

// Every operation is limited to the list in the context.
func listScope(ctx context.Context) (ablage.Cond, error) {
	list, _ := ctx.Value(listKey{}).(string)
	if list == "" {
		return ablage.All, errors.New("no list given")
	}
	return ablage.Eq("list", list), nil
}

func main() {
	file := flag.String("db", "todo.db", "SQLite file")
	pg := flag.String("postgres", "", "Postgres DSN instead of SQLite")
	list := flag.String("list", "", "the list to work on")
	flag.Parse()
	if err := run(*file, *pg, *list, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "todo:", err)
		os.Exit(1)
	}
}

func run(file, pg, list string, args []string) error {
	var (
		db      *sql.DB
		dialect sqlstore.Dialect
		err     error
	)
	if pg != "" {
		db, err = sql.Open("pgx", pg)
		dialect = sqlstore.Postgres
	} else {
		db, err = sql.Open("sqlite", file+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_txlock=immediate")
		dialect = sqlstore.SQLite
	}
	if err != nil {
		return err
	}
	defer db.Close()
	store, err := sqlstore.New(db, dialect, sqlstore.WithPrefix("todo_"))
	if err != nil {
		return err
	}
	todos, err := ablage.NewRepository[Todo](store, "todos", ablage.WithScope(listScope))
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, listKey{}, list)

	if len(args) == 0 {
		return errors.New("command missing: add, ls, done, rm, undo")
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "add":
		if len(rest) == 0 {
			return errors.New("add needs a title")
		}
		t := Todo{List: list, Title: rest[0], Tags: rest[1:]}
		if err := todos.Create(ctx, &t); err != nil {
			return err
		}
		fmt.Println(t.ID)
	case "ls":
		all, err := todos.Find(ctx, ablage.Query{OrderBy: []ablage.Order{ablage.Asc("done"), ablage.Asc("created_at")}})
		if err != nil {
			return err
		}
		for _, t := range all {
			mark := " "
			if t.Done {
				mark = "x"
			}
			fmt.Printf("[%s] %s  %s %v (v%d)\n", mark, t.ID, t.Title, t.Tags, t.Version)
		}
	case "done":
		if len(rest) == 0 {
			return errors.New("done needs an id")
		}
		_, err := todos.Update(ctx, rest[0], func(t *Todo) error {
			if t.Done {
				return errors.New("already done")
			}
			t.Done = true
			return nil
		})
		return err
	case "rm":
		if len(rest) == 0 {
			return errors.New("rm needs an id")
		}
		return todos.Delete(ctx, rest[0])
	case "undo":
		if len(rest) == 0 {
			return errors.New("undo needs an id")
		}
		return todos.Restore(ctx, rest[0])
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}
