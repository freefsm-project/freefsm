// freefsm-asset-preflight inventories installed Asset identities without migration.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/database"
	"os"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := os.Getenv("FREEFSM_ASSET_PREFLIGHT_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "Set FREEFSM_ASSET_PREFLIGHT_DATABASE_URL explicitly")
		os.Exit(1)
	}
	db, err := database.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Asset preflight could not connect")
		os.Exit(1)
	}
	defer db.Close()
	review, err := db.AssetIdentityPreflight(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Asset inventory unavailable; check schema and read access")
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(review); err != nil {
		os.Exit(1)
	}
}
