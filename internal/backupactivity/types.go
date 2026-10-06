// Package backupactivity defines the facts shared by the backup engine and the
// existing activity service, without coupling either one's implementation.
package backupactivity

import "time"

// Actor is a trusted snapshot captured at the authenticated boundary.
// IDs identify the initiating database, not identities in a restored database.
type Actor struct {
	ID          int64
	CompanyID   int64
	Name        string
	CompanyName string
}

// Event contains only durable, display-safe audit facts. Key is an idempotency
// key, never an operation capability. FailureMessage must be a safe product
// message, not an underlying command, filesystem or database error.
type Event struct {
	Key             string
	Action          string
	OccurredAt      time.Time
	Actor           Actor
	SourceName      string
	BuildKind       string
	Version         string
	FailureCategory string
	FailurePhase    string
	FailureMessage  string
	DiagnosticID    string
}
