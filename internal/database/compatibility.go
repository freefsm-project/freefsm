package database

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
)

// CheckCompatibility must run before migrations or bootstrap. Recovery of an
// interrupted restore still precedes this check: never strand its rollback copy.
func (db *DB) CheckCompatibility(ctx context.Context) error {
	var version int
	if err := db.Pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		return fmt.Errorf("cannot determine PostgreSQL version; check database connectivity")
	}
	major := version / 10000
	if major < 16 {
		return fmt.Errorf("PostgreSQL %d is unsupported; use PostgreSQL 16 or later with matching-major pg_dump and pg_restore", major)
	}
	pattern := regexp.MustCompile(`\(PostgreSQL\) (\d+)\.`)
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		output, err := exec.CommandContext(ctx, tool, "--version").Output()
		if err != nil {
			return fmt.Errorf("%s is unavailable; install PostgreSQL %d client tools on the service PATH", tool, major)
		}
		match := pattern.FindSubmatch(output)
		if len(match) != 2 || string(match[1]) != strconv.Itoa(major) {
			return fmt.Errorf("%s is incompatible with PostgreSQL %d; install matching-major client tools on the service PATH", tool, major)
		}
	}
	return nil
}
