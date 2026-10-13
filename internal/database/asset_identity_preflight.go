package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

// AssetIdentityReview records present facts, not a reconstructed timeline. A
// resolution supplies only missing/ambiguous identity facts; associations stay.
type AssetIdentityReview struct {
	Snapshot    string                    `json:"snapshot"`
	Evidence    json.RawMessage           `json:"evidence"`
	Reviewer    string                    `json:"reviewer"`
	Reason      string                    `json:"reason"`
	Resolutions []AssetIdentityResolution `json:"resolutions"`
}
type AssetIdentityResolution struct {
	ID             int64  `json:"id"`
	Number         string `json:"number"`
	OwnershipClass string `json:"ownership_class"`
}

func assetIdentityInventory(ctx context.Context, tx pgx.Tx) (AssetIdentityReview, error) {
	var out AssetIdentityReview
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('assets') IS NOT NULL`).Scan(&exists); err != nil {
		return out, err
	}
	out.Evidence = json.RawMessage(`[]`)
	if exists {
		err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object(
   'id',a.id,'number',to_jsonb(a)->'number','ownership_class',to_jsonb(a)->'ownership_class',
   'customer_id',a.customer_id,'location_id',a.location_id,'name',a.name,
   'location_customer_id',(SELECT object_id FROM locations WHERE id=a.location_id AND object_type='customer'),
   'archived_at',to_jsonb(a)->'deleted_at',
   'job_references',coalesce((SELECT jsonb_agg(jsonb_build_object('id',j.id,'customer_id',j.customer_id) ORDER BY j.id) FROM jobs j WHERE j.asset_id=a.id),'[]'),
   'association_start',NULL) ORDER BY a.id),'[]') FROM assets a`).Scan(&out.Evidence)
		if err != nil {
			return out, err
		}
	}
	sum := sha256.Sum256(out.Evidence)
	out.Snapshot = hex.EncodeToString(sum[:])
	out.Resolutions = []AssetIdentityResolution{}
	return out, nil
}

func (db *DB) AssetIdentityPreflight(ctx context.Context) (AssetIdentityReview, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return AssetIdentityReview{}, err
	}
	defer tx.Rollback(ctx)
	return assetIdentityInventory(ctx, tx)
}

func loadAssetIdentityReview(inventory AssetIdentityReview) (AssetIdentityReview, error) {
	var facts []struct {
		ID               int64  `json:"id"`
		Number           string `json:"number"`
		Class            string `json:"ownership_class"`
		Customer         *int64 `json:"customer_id"`
		Location         *int64 `json:"location_id"`
		LocationCustomer *int64 `json:"location_customer_id"`
	}
	if err := json.Unmarshal(inventory.Evidence, &facts); err != nil {
		return inventory, err
	}
	if len(facts) == 0 {
		return inventory, nil
	}
	data, err := os.ReadFile(os.Getenv("FREEFSM_ASSET_REVIEW_FILE"))
	if err != nil {
		return inventory, fmt.Errorf("Asset identity review required: export AssetIdentityPreflight, review every identity and supply FREEFSM_ASSET_REVIEW_FILE (snapshot %s)", inventory.Snapshot)
	}
	var review AssetIdentityReview
	if err = json.Unmarshal(data, &review); err != nil {
		return inventory, fmt.Errorf("invalid Asset identity review")
	}
	if review.Snapshot != inventory.Snapshot || len(review.Resolutions) != len(facts) || strings.TrimSpace(review.Reviewer) == "" || strings.TrimSpace(review.Reason) == "" {
		return inventory, fmt.Errorf("Asset identity review is stale, incomplete or unattributed")
	}
	known := map[string]int{}
	for _, f := range facts {
		if f.Number != "" {
			known[f.Number]++
		}
	}
	ids := map[int64]AssetIdentityResolution{}
	numbers := map[string]bool{}
	for _, r := range review.Resolutions {
		if r.ID <= 0 || ids[r.ID].ID != 0 || numbers[r.Number] || strings.TrimSpace(r.Number) == "" || strings.ContainsAny(r.Number, "\r\n\t") || (r.OwnershipClass != "customer_owned" && r.OwnershipClass != "company_owned") {
			return inventory, fmt.Errorf("invalid or duplicate Asset identity resolution")
		}
		ids[r.ID] = r
		numbers[r.Number] = true
	}
	for _, f := range facts {
		r, ok := ids[f.ID]
		if !ok || (known[f.Number] == 1 && f.Number != "" && r.Number != f.Number) || ((f.Class == "customer_owned" || f.Class == "company_owned") && r.OwnershipClass != f.Class) {
			return inventory, fmt.Errorf("Asset review must preserve known identity %d", f.ID)
		}
		if (r.OwnershipClass == "customer_owned" && f.Customer == nil) || (f.Customer == nil && f.Location != nil) {
			return inventory, fmt.Errorf("Asset %d has unresolved association evidence", f.ID)
		}
		if f.Location != nil && (f.LocationCustomer == nil || f.Customer == nil || *f.LocationCustomer != *f.Customer) {
			return inventory, fmt.Errorf("Asset %d Location has inconsistent Customer association evidence", f.ID)
		}
	}
	review.Evidence = inventory.Evidence
	return review, nil
}
