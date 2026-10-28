package handlers

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestMasterDependenciesInstalledUpgradePreservesFacts(t *testing.T) {
	for _, unknownLeaseStore := range []bool{false, true} {
		name := "known foundation"
		if unknownLeaseStore {
			name = "unknown Lease requires explicit review"
		}
		t.Run(name, func(t *testing.T) {
			prior := fstest.MapFS{}
			entries, err := fs.ReadDir(database.MigrationFS(), ".")
			historyCheck(t, err)
			for _, entry := range entries {
				// Preserve the actual pre-103 installation boundary as later
				// Lease migrations are introduced; never apply descendants first.
				if entry.Name() >= "103_" {
					continue
				}
				raw, err := fs.ReadFile(database.MigrationFS(), entry.Name())
				historyCheck(t, err)
				prior[entry.Name()] = &fstest.MapFile{Data: raw}
			}
			pool, client, _, _, company, admin := permissionHTTPFixtureMigrations(t, prior)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed owner").SaveX(ctx)
			contact := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Recorded contact").SaveX(ctx)
			client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetCustomerContactID(contact.ID).SetJobType("Recorded unfinished work").SaveX(ctx)
			_, err = pool.Exec(ctx, `DELETE FROM job_contacts WHERE contact_id=$1`, contact.ID)
			historyCheck(t, err)
			// Retain an actually possible legacy inconsistency; never infer a
			// completion, default change, archive actor or historical Lease.
			_, err = pool.Exec(ctx, `UPDATE customer_contacts SET deleted_at='2020-01-01',is_primary=true WHERE id=$1`, contact.ID)
			historyCheck(t, err)
			_, err = authorization.New(pool).SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Installed base reader", []authorization.Grant{{Permission: "contact.view", Scope: authorization.All}})
			historyCheck(t, err)
			tables := []string{"customers", "customer_contacts", "locations", "assets", "jobs", "authorization_roles", "authorization_grants", "authorization_audit", "activity_logs"}
			if unknownLeaseStore {
				_, err = pool.Exec(ctx, `CREATE TABLE leases(id bigint PRIMARY KEY, unknown_state text); INSERT INTO leases VALUES(71,'unmapped legacy meaning')`)
				historyCheck(t, err)
				tables = append(tables, "leases")
			}
			snapshot := func() map[string]string {
				out := map[string]string{}
				for _, table := range tables {
					var rows string
					historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(d) ORDER BY to_jsonb(d)::text),'[]')::text FROM `+pgx.Identifier{table}.Sanitize()+` d`).Scan(&rows))
					out[table] = rows
				}
				return out
			}
			before := snapshot()
			err = (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS())
			if unknownLeaseStore {
				if err == nil || !strings.Contains(err.Error(), "explicit dependency mapping review") {
					t.Fatalf("unknown installed Lease mapping: %v", err)
				}
				var created bool
				historyCheck(t, pool.QueryRow(ctx, `SELECT to_regclass('lease_master_dependencies') IS NOT NULL`).Scan(&created))
				if created {
					t.Fatal("failed migration partially installed dependency projection")
				}
			} else {
				historyCheck(t, err)
				var invented int
				historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM lease_master_dependencies)+(SELECT count(*) FROM lease_asset_dependencies)`).Scan(&invented))
				if invented != 0 {
					t.Fatal("upgrade invented Lease dependencies")
				}
			}
			historyEqual(t, "upgrade preserves recorded facts, grants and audit", snapshot(), before)
		})
	}
}
