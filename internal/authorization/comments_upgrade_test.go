package authorization_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"golang.org/x/crypto/bcrypt"
)

func TestCommentsUpgradePreservesRecordedFacts(t *testing.T) {
	pool := authorizationPool(t)
	ctx := context.Background()
	db := &database.DB{Pool: pool}
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() > "057_protected_host_audit.up.sql" {
			continue
		}
		b, err := fs.ReadFile(database.MigrationFS(), e.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[e.Name()] = &fstest.MapFile{Data: b}
	}
	if err := db.Migrate(ctx, old); err != nil {
		t.Fatal(err)
	}
	var company, admin, parent, id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`).Scan(&company); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("CommentsUpgrade1!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'upgrade-comments@test','Recorded author',$2,'admin') RETURNING id`, company, string(hash)).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Recorded parent') RETURNING id`, company).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("historical text\n", 1000)
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := pool.QueryRow(ctx, `INSERT INTO comments(company_id,object_type,object_id,author_id,content,created_at,updated_at) VALUES($1,'customer',$2,$3,$4,$5,$5) RETURNING id`, company, parent, admin, text, at).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope)::text FROM authorization_grants g`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	svc := authorization.New(pool)
	ref := objectref.New(objectref.TypeCustomer, parent)
	actor := authorization.Actor{UserID: admin}
	list, err := svc.Comments(ctx, actor, ref)
	if err != nil || len(list.Entries) != 1 {
		t.Fatalf("upgrade list=%+v err=%v", list, err)
	}
	c := list.Entries[0]
	if c.ID != id || c.AuthorID != admin || c.Content != text || !c.CreatedAt.Equal(at) || c.ReplyTo != nil || c.RemovedAt != nil || c.RemovedBy != nil {
		t.Fatalf("recorded facts changed: %+v", c)
	}
	reply, err := svc.AddComment(ctx, actor, ref, "new reply", id)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveComment(ctx, actor, ref, id); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	list, err = authorization.New(pool).Comments(ctx, actor, ref)
	if err != nil || len(list.Entries) != 2 || list.Entries[0].Content != "" || list.Entries[1].ID != reply || list.Entries[1].ReplyTo == nil || *list.Entries[1].ReplyTo != id {
		t.Fatalf("restart changed thread: %+v %v", list, err)
	}
	retained, err := svc.RetainedComment(ctx, actor, ref, id)
	if err != nil || retained != text {
		t.Fatal("retained history lost", err)
	}
	var after string
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope)::text FROM authorization_grants g`).Scan(&after); err != nil || before != after {
		t.Fatalf("migration changed grants: %v", err)
	}
}
