// freefsm-master-preflight observes Customer masters without running migrations.
package main

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/database"
	"os"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := os.Getenv("FREEFSM_MASTER_PREFLIGHT_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "Set FREEFSM_MASTER_PREFLIGHT_DATABASE_URL explicitly")
		os.Exit(1)
	}
	db, err := database.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not connect to the master inventory database")
		os.Exit(1)
	}
	defer db.Close()
	report, err := db.CustomerMasterPreflight(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Master preflight failed; check schema, read access and availability. No inventory emitted.")
		os.Exit(1)
	}
	if _, err = fmt.Fprintln(os.Stdout, report); err != nil {
		os.Exit(1)
	}
}
