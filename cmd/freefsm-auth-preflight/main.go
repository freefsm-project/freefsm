// freefsm-auth-preflight inventories legacy authorization without migrating or
// starting the application. Its output is evidence for review, never authority.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	os.Exit(run(ctx, os.Getenv("FREEFSM_AUTH_PREFLIGHT_DATABASE_URL"), os.Stdout, os.Stderr))
}

func run(ctx context.Context, dsn string, out, diagnostics io.Writer) int {
	if dsn == "" {
		fmt.Fprintln(diagnostics, "FREEFSM_AUTH_PREFLIGHT_DATABASE_URL must explicitly identify the database to inventory")
		return 1
	}
	db, err := database.Connect(ctx, dsn)
	if err != nil {
		// Driver errors can include connection strings. Do not print them.
		fmt.Fprintln(diagnostics, "authorization preflight could not connect; check the database URL and connectivity")
		return 1
	}
	defer db.Close()
	report, err := db.AuthorizationPreflight(ctx)
	if err != nil {
		fmt.Fprintln(diagnostics, "authorization preflight failed; check supported schema, read access, and database availability; no review inventory was emitted")
		return 1
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(diagnostics, "could not write authorization inventory")
		return 1
	}
	if report.ReviewRequired {
		fmt.Fprintln(diagnostics, "explicit migration review required; this inventory grants no authority and does not approve cutover")
		return 2
	}
	fmt.Fprintln(diagnostics, "inventory complete; this command does not perform or approve authorization cutover")
	return 0
}
