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

// AssetAssociationReview attests to retaining observed relationships, not to
// reconstructing an unrecorded acquisition, transfer, or service date.
type AssetAssociationReview struct {
	Snapshot                   string          `json:"snapshot"`
	Evidence                   json.RawMessage `json:"evidence"`
	Reviewer                   string          `json:"reviewer"`
	Reason                     string          `json:"reason"`
	AdministratorReviewed      bool            `json:"administrator_reviewed"`
	RetainObservedUnknownDates bool            `json:"retain_observed_unknown_dates"`
}

func assetAssociationInventory(ctx context.Context, tx pgx.Tx) (AssetAssociationReview, error) {
	out := AssetAssociationReview{Evidence: json.RawMessage(`[]`)}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('assets') IS NOT NULL`).Scan(&exists); err != nil {
		return out, err
	}
	if exists {
		if err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('asset_id',a.id,'customer_id',a.customer_id,'location_id',a.location_id,
   'effective_date',NULL,'review','Unknown historical dates and ambiguous earlier transitions; preserve observed facts',
   'job_links',coalesce((SELECT jsonb_agg(jsonb_build_object('job_id',j.id,'customer_id',j.customer_id,'cross_customer',j.customer_id IS DISTINCT FROM a.customer_id,'effective_date',NULL) ORDER BY j.id) FROM jobs j WHERE j.asset_id=a.id),'[]')) ORDER BY a.id),'[]') FROM assets a`).Scan(&out.Evidence); err != nil {
			return out, err
		}
	}
	sum := sha256.Sum256(out.Evidence)
	out.Snapshot = hex.EncodeToString(sum[:])
	return out, nil
}

func (db *DB) AssetAssociationPreflight(ctx context.Context) (AssetAssociationReview, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return AssetAssociationReview{}, err
	}
	defer tx.Rollback(ctx)
	return assetAssociationInventory(ctx, tx)
}

func loadAssetAssociationReview(inventory AssetAssociationReview) (AssetAssociationReview, error) {
	if string(inventory.Evidence) == "[]" {
		return inventory, nil
	}
	data, err := os.ReadFile(os.Getenv("FREEFSM_ASSET_ASSOCIATION_REVIEW_FILE"))
	if err != nil {
		return inventory, fmt.Errorf("Asset association cutover requires explicit Administrator review: export AssetAssociationPreflight and supply FREEFSM_ASSET_ASSOCIATION_REVIEW_FILE (snapshot %s); observed facts are unchanged", inventory.Snapshot)
	}
	var review AssetAssociationReview
	if err = json.Unmarshal(data, &review); err != nil {
		return inventory, fmt.Errorf("invalid Asset association review")
	}
	if review.Snapshot != inventory.Snapshot || !review.AdministratorReviewed || !review.RetainObservedUnknownDates || strings.TrimSpace(review.Reviewer) == "" || strings.TrimSpace(review.Reason) == "" {
		return inventory, fmt.Errorf("Asset association review is stale, incomplete or lacks Administrator approval")
	}
	review.Evidence = inventory.Evidence
	return review, nil
}
