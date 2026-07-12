// Package delivery owns immutable document email snapshots and their outbox lifecycle.
//
// Delivery is at-least-once. A process can lose its lease after SMTP accepts but
// before acceptance is committed. A stable tenant-scoped Message-ID and adapter
// idempotency key reduce duplicates, but SMTP cannot provide exactly-once delivery.
package delivery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	MaxAttempts       = 6
	BaseRetryDelay    = time.Minute
	MaxRetryDelay     = time.Hour
	LeaseDuration     = 45 * time.Second
	SendTimeout       = 20 * time.Second
	TrackingTokenSize = 32
)

var (
	ErrForbidden           = errors.New("document delivery forbidden")
	ErrNotFound            = errors.New("document delivery not found")
	ErrInvalid             = errors.New("invalid document delivery")
	ErrIdempotencyConflict = errors.New("idempotency key reused for a different delivery")
	ErrLeaseLost           = errors.New("document delivery lease lost")
)

type SendErrorKind string

const (
	SendTemporary SendErrorKind = "temporary"
	SendPermanent SendErrorKind = "permanent"
	SendAmbiguous SendErrorKind = "ambiguous"
)

type SendError struct {
	Kind SendErrorKind
	Err  error
}

func (e *SendError) Error() string { return string(e.Kind) + " delivery error: " + e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }
func classifySendError(err error) SendErrorKind {
	var e *SendError
	if errors.As(err, &e) {
		return e.Kind
	}
	return SendTemporary
}

type Actor struct {
	ID, CompanyID int64
	Role          string
}
type DocumentRef struct {
	Type string
	ID   int64
}
type Snapshot struct {
	To, CC, BCC                 []string
	Subject, TextBody, HTMLBody string
	PDF                         []byte
	PDFFilename                 string
}
type QueueRequest struct {
	Key      uuid.UUID
	Document DocumentRef
	Snapshot Snapshot
}
type Delivery struct {
	ID, CompanyID, DocumentID, ActorID                                           int64
	DocumentType, State, Subject, TextBody, HTMLBody, PDFFilename, MessageID     string
	To, CC, BCC                                                                  []string
	PDF                                                                          []byte
	ExpectedStatusID                                                             *int64
	LifetimeAttemptCount, CycleAttemptCount, RetryCycle, OpenCount               int
	AttemptToken                                                                 uuid.UUID
	ProviderIdentifier, AcceptanceEvidence, LastError                            string
	TrackingEnabled                                                              bool
	FirstOpenAt, LastOpenAt, NextAttemptAt, LeaseExpiresAt, CreatedAt, UpdatedAt *time.Time
	AcceptedAt, DeliveredAt, BouncedAt, FailedAt                                 *time.Time
}
type Summary struct {
	ID                                 int64
	State, LastError                   string
	To                                 []string
	LifetimeAttemptCount, OpenCount    int
	CreatedAt, AcceptedAt, DeliveredAt *time.Time
	BouncedAt, FailedAt, LastOpenAt    *time.Time
}
type SendResult struct {
	ProviderIdentifier string
	Evidence           map[string]any
}
type Sender interface {
	Send(context.Context, Delivery) (SendResult, error)
}
type AcceptanceHook interface {
	OnAccepted(context.Context, pgx.Tx, Delivery) error
}
type NopAcceptanceHook struct{}

func (NopAcceptanceHook) OnAccepted(context.Context, pgx.Tx, Delivery) error { return nil }

type ProviderEvent struct {
	CompanyID, DeliveryID              int64
	ProviderIdentifier, EventID, State string
	Evidence                           map[string]any
}

// ProviderEvidenceRecorder is a trusted adapter port. Adapters must authenticate
// provider callbacks and correlate their provider identifier before calling it.
type ProviderEvidenceRecorder interface {
	RecordProviderEvidence(context.Context, ProviderEvent) error
}

type Service struct {
	db        *pgxpool.Pool
	publicURL string
	now       func() time.Time
}

func New(db *pgxpool.Pool, publicURL string) *Service {
	return &Service{db: db, publicURL: strings.TrimRight(publicURL, "/"), now: time.Now}
}

func (s *Service) Queue(ctx context.Context, a Actor, r QueueRequest) (Delivery, error) {
	if a.ID <= 0 || a.CompanyID <= 0 || r.Key == uuid.Nil || (r.Document.Type != "estimate" && r.Document.Type != "invoice") || r.Document.ID <= 0 || len(r.Snapshot.To) == 0 || strings.TrimSpace(r.Snapshot.Subject) == "" || len(r.Snapshot.PDF) == 0 || strings.TrimSpace(r.Snapshot.PDFFilename) == "" {
		return Delivery{}, ErrInvalid
	}
	r.Snapshot.To = canonicalAddresses(r.Snapshot.To)
	r.Snapshot.CC = canonicalAddresses(r.Snapshot.CC)
	r.Snapshot.BCC = canonicalAddresses(r.Snapshot.BCC)
	if len(r.Snapshot.To) == 0 {
		return Delivery{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Delivery{}, err
	}
	defer tx.Rollback(ctx)
	var jobID, statusID *int64
	q := fmt.Sprintf(`SELECT job_id,status_id FROM %ss WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL AND conversion_hidden_at IS NULL FOR SHARE`, r.Document.Type)
	if err = tx.QueryRow(ctx, q, r.Document.ID, a.CompanyID).Scan(&jobID, &statusID); errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNotFound
	} else if err != nil {
		return Delivery{}, err
	}
	if err = authorize(ctx, tx, a, jobID); err != nil {
		return Delivery{}, err
	}
	var tracking bool
	if err = tx.QueryRow(ctx, `SELECT email_tracking_enabled FROM company_settings WHERE company_id=$1`, a.CompanyID).Scan(&tracking); errors.Is(err, pgx.ErrNoRows) {
		tracking = false
	} else if err != nil {
		return Delivery{}, err
	}
	fingerprint, err := requestFingerprint(r, statusID, tracking)
	if err != nil {
		return Delivery{}, err
	}
	var existingID int64
	var existingFingerprint []byte
	err = tx.QueryRow(ctx, `SELECT id,request_fingerprint FROM document_deliveries WHERE company_id=$1 AND idempotency_key=$2`, a.CompanyID, r.Key).Scan(&existingID, &existingFingerprint)
	if err == nil {
		if !equalBytes(existingFingerprint, fingerprint) {
			return Delivery{}, ErrIdempotencyConflict
		}
		d, e := get(ctx, tx, a.CompanyID, existingID)
		if e != nil {
			return Delivery{}, e
		}
		return d, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, err
	}
	messageID := fmt.Sprintf("<delivery-%d-%s@freefsm.local>", a.CompanyID, r.Key)
	var tokenHash []byte
	html := r.Snapshot.HTMLBody
	if tracking {
		raw := make([]byte, TrackingTokenSize)
		if _, err = rand.Read(raw); err != nil {
			return Delivery{}, err
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		sum := sha256.Sum256([]byte(token))
		tokenHash = sum[:]
		html = injectPixel(html, s.publicURL+"/delivery/open/"+token)
	}
	var estimateID, invoiceID any
	if r.Document.Type == "estimate" {
		estimateID = r.Document.ID
	} else {
		invoiceID = r.Document.ID
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO document_deliveries(company_id,document_type,document_id,estimate_id,invoice_id,actor_id,idempotency_key,request_fingerprint,recipients_to,recipients_cc,recipients_bcc,subject,text_body,html_body,pdf_data,pdf_filename,message_id,expected_status_id,tracking_enabled,tracking_token_hash)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20) ON CONFLICT(company_id,idempotency_key) DO NOTHING RETURNING id`, a.CompanyID, r.Document.Type, r.Document.ID, estimateID, invoiceID, a.ID, r.Key, fingerprint, r.Snapshot.To, r.Snapshot.CC, r.Snapshot.BCC, r.Snapshot.Subject, r.Snapshot.TextBody, html, r.Snapshot.PDF, r.Snapshot.PDFFilename, messageID, statusID, tracking, tokenHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.QueryRow(ctx, `SELECT id,request_fingerprint FROM document_deliveries WHERE company_id=$1 AND idempotency_key=$2`, a.CompanyID, r.Key).Scan(&id, &existingFingerprint); err != nil {
			return Delivery{}, err
		}
		if !equalBytes(existingFingerprint, fingerprint) {
			return Delivery{}, ErrIdempotencyConflict
		}
		d, e := get(ctx, tx, a.CompanyID, id)
		if e != nil {
			return Delivery{}, e
		}
		return d, tx.Commit(ctx)
	}
	if err != nil {
		return Delivery{}, err
	}
	if err = event(ctx, tx, id, a.CompanyID, &a.ID, "state", "", "queued", "", "", map[string]any{"reason": "queued"}); err != nil {
		return Delivery{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,'document_delivery_queued',$3,$4,jsonb_build_object('delivery_id',$5::bigint))`, a.CompanyID, a.ID, r.Document.Type, r.Document.ID, id); err != nil {
		return Delivery{}, err
	}
	d, err := get(ctx, tx, a.CompanyID, id)
	if err != nil {
		return Delivery{}, err
	}
	return d, tx.Commit(ctx)
}

func canonicalAddresses(v []string) []string {
	out := make([]string, 0, len(v))
	seen := map[string]bool{}
	for _, x := range v {
		x = strings.ToLower(strings.TrimSpace(x))
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}
func requestFingerprint(r QueueRequest, status *int64, tracking bool) ([]byte, error) {
	pdf := sha256.Sum256(r.Snapshot.PDF)
	v := struct {
		Document                               DocumentRef `json:"document"`
		To, CC, BCC                            []string
		Subject, Text, HTML, Filename, PDFHash string
		Status                                 *int64
		Tracking                               bool
	}{r.Document, r.Snapshot.To, r.Snapshot.CC, r.Snapshot.BCC, r.Snapshot.Subject, r.Snapshot.TextBody, r.Snapshot.HTMLBody, r.Snapshot.PDFFilename, hex.EncodeToString(pdf[:]), status, tracking}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var x byte
	for i := range a {
		x |= a[i] ^ b[i]
	}
	return x == 0
}
func authorize(ctx context.Context, tx pgx.Tx, a Actor, jobID *int64) error {
	var role string
	if tx.QueryRow(ctx, `SELECT role FROM users WHERE id=$1 AND company_id=$2`, a.ID, a.CompanyID).Scan(&role) != nil || role != a.Role {
		return ErrForbidden
	}
	if role == "admin" || role == "dispatcher" {
		return nil
	}
	if (role != "tech" && role != "technician") || jobID == nil {
		return ErrForbidden
	}
	var ok bool
	if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_assignments a JOIN jobs j ON j.id=a.job_id WHERE a.job_id=$1 AND a.user_id=$2 AND j.company_id=$3 AND j.deleted_at IS NULL)`, *jobID, a.ID, a.CompanyID).Scan(&ok) != nil || !ok {
		return ErrForbidden
	}
	return nil
}

func (s *Service) Claim(ctx context.Context) (Delivery, error) {
	if err := ctx.Err(); err != nil {
		return Delivery{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Delivery{}, err
	}
	defer tx.Rollback(ctx)
	if err = allowTransitions(ctx, tx); err != nil {
		return Delivery{}, err
	}
	now := s.now()
	rows, err := tx.Query(ctx, `SELECT id,company_id,cycle_attempt_count FROM document_deliveries WHERE state='sending' AND lease_expires_at<=$1 FOR UPDATE`, now)
	if err != nil {
		return Delivery{}, err
	}
	type stale struct {
		id, company int64
		attempt     int
	}
	var staleRows []stale
	for rows.Next() {
		var x stale
		if err = rows.Scan(&x.id, &x.company, &x.attempt); err != nil {
			rows.Close()
			return Delivery{}, err
		}
		staleRows = append(staleRows, x)
	}
	rows.Close()
	for _, x := range staleRows {
		state := "queued"
		var failed any = nil
		if x.attempt >= MaxAttempts {
			state = "failed"
			failed = now
		}
		tag, e := tx.Exec(ctx, `UPDATE document_deliveries SET state=$1,attempt_token=NULL,lease_expires_at=NULL,next_attempt_at=$2,last_error='worker lease expired after ambiguous transport outcome',failed_at=$3,updated_at=$2 WHERE id=$4 AND state='sending'`, state, now, failed, x.id)
		if e != nil {
			return Delivery{}, e
		}
		if tag.RowsAffected() != 1 {
			return Delivery{}, ErrLeaseLost
		}
		if e = event(ctx, tx, x.id, x.company, nil, "state", "sending", state, "", "", map[string]any{"reason": "lease_expired", "outcome": "ambiguous"}); e != nil {
			return Delivery{}, e
		}
	}
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM document_deliveries WHERE state='queued' AND next_attempt_at<=$1 ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, now).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) && len(staleRows) > 0 {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return Delivery{}, commitErr
			}
		}
		return Delivery{}, err
	}
	token := uuid.New()
	lease := now.Add(LeaseDuration)
	tag, err := tx.Exec(ctx, `UPDATE document_deliveries SET state='sending',lifetime_attempt_count=lifetime_attempt_count+1,cycle_attempt_count=cycle_attempt_count+1,attempt_token=$1,lease_expires_at=$2,updated_at=$3 WHERE id=$4 AND state='queued'`, token, lease, now, id)
	if err != nil {
		return Delivery{}, err
	}
	if tag.RowsAffected() != 1 {
		return Delivery{}, ErrLeaseLost
	}
	d, err := get(ctx, tx, 0, id)
	if err != nil {
		return Delivery{}, err
	}
	if err = event(ctx, tx, id, d.CompanyID, nil, "state", "queued", "sending", "", "", map[string]any{"attempt_token": token, "cycle_attempt": d.CycleAttemptCount}); err != nil {
		return Delivery{}, err
	}
	return d, tx.Commit(ctx)
}

func (s *Service) ProcessOne(ctx context.Context, sender Sender, hook AcceptanceHook) (bool, error) {
	d, err := s.Claim(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hook == nil {
		hook = NopAcceptanceHook{}
	}
	sendCtx, cancel := context.WithTimeout(ctx, SendTimeout)
	result, sendErr := sender.Send(sendCtx, d)
	cancel()
	if sendErr != nil {
		return true, s.failAttempt(ctx, d, sendErr)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer tx.Rollback(ctx)
	if err = allowTransitions(ctx, tx); err != nil {
		return true, err
	}
	evidence, _ := json.Marshal(result.Evidence)
	now := s.now()
	tag, err := tx.Exec(ctx, `UPDATE document_deliveries SET state='accepted',provider_identifier=$1,acceptance_evidence=$2,accepted_at=$3,attempt_token=NULL,lease_expires_at=NULL,last_error='',updated_at=$3 WHERE id=$4 AND attempt_token=$5 AND state='sending'`, result.ProviderIdentifier, evidence, now, d.ID, d.AttemptToken)
	if err != nil {
		return true, err
	}
	if tag.RowsAffected() != 1 {
		return true, ErrLeaseLost
	}
	if err = event(ctx, tx, d.ID, d.CompanyID, nil, "state", "sending", "accepted", result.ProviderIdentifier, "", result.Evidence); err != nil {
		return true, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,'document_delivery_accepted',$3,$4,jsonb_build_object('delivery_id',$5::bigint))`, d.CompanyID, d.ActorID, d.DocumentType, d.DocumentID, d.ID); err != nil {
		return true, err
	}
	if err = hook.OnAccepted(ctx, tx, d); err != nil {
		return true, err
	}
	return true, tx.Commit(ctx)
}

func (s *Service) failAttempt(ctx context.Context, d Delivery, cause error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = allowTransitions(ctx, tx); err != nil {
		return err
	}
	now := s.now()
	kind := classifySendError(cause)
	state := "queued"
	next := now.Add(retryDelay(d.CycleAttemptCount))
	var failed any = nil
	if kind == SendPermanent || d.CycleAttemptCount >= MaxAttempts {
		state = "failed"
		failed = now
	}
	tag, err := tx.Exec(ctx, `UPDATE document_deliveries SET state=$1,next_attempt_at=$2,attempt_token=NULL,lease_expires_at=NULL,last_error=$3,failed_at=$4,updated_at=$5 WHERE id=$6 AND attempt_token=$7 AND state='sending'`, state, next, cause.Error(), failed, now, d.ID, d.AttemptToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	if err = event(ctx, tx, d.ID, d.CompanyID, nil, "state", "sending", state, "", "", map[string]any{"error": cause.Error(), "classification": kind}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func retryDelay(attempt int) time.Duration {
	if attempt >= 7 {
		return MaxRetryDelay
	}
	d := BaseRetryDelay * time.Duration(1<<max(0, attempt-1))
	if d > MaxRetryDelay {
		return MaxRetryDelay
	}
	return d
}

func (s *Service) ManualRetry(ctx context.Context, a Actor, id int64, reason string, keys ...uuid.UUID) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	d, err := get(ctx, tx, a.CompanyID, id)
	if err != nil {
		return err
	}
	jobID, active := documentJobID(ctx, tx, d)
	if !active {
		return ErrNotFound
	}
	if err = authorize(ctx, tx, a, jobID); err != nil {
		return err
	}
	var key uuid.UUID
	if len(keys) > 0 {
		key = keys[0]
		if key == uuid.Nil {
			return ErrInvalid
		}
	}
	if key != uuid.Nil {
		var previous *uuid.UUID
		if err = tx.QueryRow(ctx, `SELECT last_manual_retry_key FROM document_deliveries WHERE id=$1 AND company_id=$2`, id, a.CompanyID).Scan(&previous); err != nil {
			return err
		}
		if previous != nil && *previous == key {
			return tx.Commit(ctx)
		}
	}
	if d.State != "failed" && d.State != "bounced" {
		return ErrInvalid
	}
	if err = allowTransitions(ctx, tx); err != nil {
		return err
	}
	now := s.now()
	tag, err := tx.Exec(ctx, `UPDATE document_deliveries SET state='queued',cycle_attempt_count=0,retry_cycle=retry_cycle+1,next_attempt_at=$1,failed_at=NULL,bounced_at=NULL,last_error='',updated_at=$1,last_manual_retry_key=NULLIF($4::uuid,'00000000-0000-0000-0000-000000000000') WHERE id=$2 AND company_id=$3 AND state IN ('failed','bounced')`, now, id, a.CompanyID, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	if err = event(ctx, tx, id, a.CompanyID, &a.ID, "state", d.State, "queued", "", "", map[string]any{"reason": reason, "manual": true}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,'document_delivery_retried',$3,$4,jsonb_build_object('delivery_id',$5::bigint,'reason',$6::text))`, a.CompanyID, a.ID, d.DocumentType, d.DocumentID, id, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func documentJobID(ctx context.Context, tx pgx.Tx, d Delivery) (*int64, bool) {
	var id *int64
	err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT job_id FROM %ss WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL AND conversion_hidden_at IS NULL`, d.DocumentType), d.DocumentID, d.CompanyID).Scan(&id)
	return id, err == nil
}

func (s *Service) RecordProviderEvidence(ctx context.Context, p ProviderEvent) error {
	if p.CompanyID <= 0 || p.DeliveryID <= 0 || p.ProviderIdentifier == "" || p.EventID == "" || (p.State != "delivered" && p.State != "bounced") {
		return ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state, identifier string
	if err = tx.QueryRow(ctx, `SELECT state,provider_identifier FROM document_deliveries WHERE id=$1 AND company_id=$2 FOR UPDATE`, p.DeliveryID, p.CompanyID).Scan(&state, &identifier); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if identifier == "" || identifier != p.ProviderIdentifier {
		return ErrForbidden
	}
	b, _ := json.Marshal(p.Evidence)
	tag, err := tx.Exec(ctx, `INSERT INTO document_delivery_events(delivery_id,company_id,event_kind,provider_identifier,provider_event_id,evidence) VALUES($1,$2,'provider',$3,$4,$5) ON CONFLICT DO NOTHING`, p.DeliveryID, p.CompanyID, p.ProviderIdentifier, p.EventID, b)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if (state != "accepted" && state != "delivered") || (state == "delivered" && p.State == "delivered") {
		return tx.Commit(ctx)
	}
	if err = allowTransitions(ctx, tx); err != nil {
		return err
	}
	now := s.now()
	column := "delivered_at"
	if p.State == "bounced" {
		column = "bounced_at"
	}
	q := fmt.Sprintf(`UPDATE document_deliveries SET state=$1,%s=$2,updated_at=$2 WHERE id=$3 AND company_id=$4`, column)
	if _, err = tx.Exec(ctx, q, p.State, now, p.DeliveryID, p.CompanyID); err != nil {
		return err
	}
	if err = event(ctx, tx, p.DeliveryID, p.CompanyID, nil, "state", state, p.State, p.ProviderIdentifier, "", map[string]any{"provider_event_id": p.EventID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func event(ctx context.Context, tx pgx.Tx, id, company int64, actor *int64, kind, from, to, provider, eventID string, evidence any) error {
	b, _ := json.Marshal(evidence)
	_, err := tx.Exec(ctx, `INSERT INTO document_delivery_events(delivery_id,company_id,actor_id,event_kind,from_state,to_state,provider_identifier,provider_event_id,evidence) VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,NULLIF($8,''),$9)`, id, company, actor, kind, from, to, provider, eventID, b)
	return err
}
func allowTransitions(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT set_config('freefsm.delivery_transition','allowed',true)`)
	return err
}

func injectPixel(html, url string) string {
	if html == "" {
		return ""
	}
	pixel := `<img src="` + url + `" width="1" height="1" alt="" style="display:none">`
	i := strings.LastIndex(strings.ToLower(html), "</body>")
	if i < 0 {
		return html + pixel
	}
	return html[:i] + pixel + html[i:]
}
func (s *Service) RecordOpen(ctx context.Context, token string) error {
	if len(token) != base64.RawURLEncoding.EncodedLen(TrackingTokenSize) {
		return ErrNotFound
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != TrackingTokenSize {
		return ErrNotFound
	}
	sum := sha256.Sum256([]byte(token))
	now := s.now()
	tag, err := s.db.Exec(ctx, `UPDATE document_deliveries SET first_open_at=coalesce(first_open_at,$1),last_open_at=$1,open_count=open_count+1,updated_at=$1 WHERE tracking_token_hash=$2`, now, sum[:])
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
func TrackingResponseHeaders() map[string]string {
	return map[string]string{"Content-Type": "image/gif", "Cache-Control": "no-store, no-cache, must-revalidate, max-age=0", "Pragma": "no-cache", "Expires": "0", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff"}
}
func TrackingPixelGIF() []byte {
	b, _ := hex.DecodeString("47494638396101000100800000ffffff00000021f90401000000002c00000000010001000002024401003b")
	return b
}

func (s *Service) History(ctx context.Context, company int64, ref DocumentRef) ([]Summary, error) {
	rows, err := s.db.Query(ctx, `SELECT id,state,last_error,recipients_to,lifetime_attempt_count,open_count,created_at,accepted_at,delivered_at,bounced_at,failed_at,last_open_at FROM document_deliveries WHERE company_id=$1 AND document_type=$2 AND document_id=$3 ORDER BY created_at DESC,id DESC`, company, ref.Type, ref.ID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var item Summary
		if err = rows.Scan(&item.ID, &item.State, &item.LastError, &item.To, &item.LifetimeAttemptCount, &item.OpenCount, &item.CreatedAt, &item.AcceptedAt, &item.DeliveredAt, &item.BouncedAt, &item.FailedAt, &item.LastOpenAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func get(ctx context.Context, q rowQuerier, company, id int64) (Delivery, error) {
	var d Delivery
	var token *uuid.UUID
	err := q.QueryRow(ctx, `SELECT id,company_id,document_type,document_id,actor_id,recipients_to,recipients_cc,recipients_bcc,subject,text_body,html_body,pdf_data,pdf_filename,message_id,expected_status_id,state,lifetime_attempt_count,cycle_attempt_count,retry_cycle,next_attempt_at,attempt_token,lease_expires_at,provider_identifier,acceptance_evidence::text,last_error,tracking_enabled,first_open_at,last_open_at,open_count,created_at,updated_at,accepted_at,delivered_at,bounced_at,failed_at FROM document_deliveries WHERE id=$1 AND ($2=0 OR company_id=$2)`, id, company).Scan(&d.ID, &d.CompanyID, &d.DocumentType, &d.DocumentID, &d.ActorID, &d.To, &d.CC, &d.BCC, &d.Subject, &d.TextBody, &d.HTMLBody, &d.PDF, &d.PDFFilename, &d.MessageID, &d.ExpectedStatusID, &d.State, &d.LifetimeAttemptCount, &d.CycleAttemptCount, &d.RetryCycle, &d.NextAttemptAt, &token, &d.LeaseExpiresAt, &d.ProviderIdentifier, &d.AcceptanceEvidence, &d.LastError, &d.TrackingEnabled, &d.FirstOpenAt, &d.LastOpenAt, &d.OpenCount, &d.CreatedAt, &d.UpdatedAt, &d.AcceptedAt, &d.DeliveredAt, &d.BouncedAt, &d.FailedAt)
	if token != nil {
		d.AttemptToken = *token
	}
	return d, err
}
