package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/backupactivity"
	"github.com/jackc/pgx/v5"
)

// WriteBackupActivity is the startup/recovery sink. Each call opens a dedicated
// short-lived connection, so it also works before Ent is wired and after database
// replacement. The caller must run schema migrations before replaying events.
// Actors remain historical snapshots without numeric links to restored users.
// Ownership comes from the current instance's first settings row, as in Get.
func WriteBackupActivity(ctx context.Context, dsn string, event backupactivity.Event) error {
	metadata, err := backupActivityMetadata(event)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect backup activity: %w", err)
	}
	defer conn.Close(context.Background())
	var companyID int64
	if err := conn.QueryRow(ctx, `SELECT company_id FROM company_settings ORDER BY id LIMIT 1`).Scan(&companyID); err != nil {
		return fmt.Errorf("backup activity company: %w", err)
	}
	if err := validateCompanyID(companyID); err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `INSERT INTO activity_logs
		(company_id, actor_id, event_key, action, object_type, object_id, metadata, created_at)
		VALUES ($1, NULL, $2, $3, 'instance', 1, $4::jsonb, $5)
		ON CONFLICT (event_key) DO NOTHING`, companyID, event.Key, event.Action, metadata, event.OccurredAt)
	if err != nil {
		return fmt.Errorf("record backup activity: %w", err)
	}
	return nil
}

func backupActivityMetadata(event backupactivity.Event) (string, error) {
	if strings.TrimSpace(event.Key) == "" || event.OccurredAt.IsZero() || strings.TrimSpace(event.Actor.Name) == "" {
		return "", fmt.Errorf("backup activity requires event key, occurrence time and actor name snapshot")
	}
	switch event.Action {
	case "backup_created", "backup_failed", "backup_validated", "backup_validation_failed", "backup_download_started", "restore_started", "restore_completed", "restore_failed":
	default:
		return "", fmt.Errorf("invalid backup activity action: %q", event.Action)
	}
	// Explicit allowlist: never serialize an Operation, manifest or journal. These
	// facts contain no capability, password, file path or raw diagnostic output.
	metadata := map[string]string{
		"actor_name":  event.Actor.Name,
		"entity_name": "Instance backup and restore",
	}
	for key, value := range map[string]string{
		"actor_company_name": event.Actor.CompanyName,
		"source_name":        event.SourceName,
		"build_kind":         event.BuildKind,
		"version":            event.Version,
		"failure_category":   event.FailureCategory,
		"failure_phase":      event.FailurePhase,
		"failure_message":    event.FailureMessage,
		"diagnostic_id":      event.DiagnosticID,
	} {
		if value != "" {
			metadata[key] = value
		}
	}
	encoded, err := json.Marshal(metadata)
	return string(encoded), err
}
