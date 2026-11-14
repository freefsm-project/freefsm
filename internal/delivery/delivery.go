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
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/credentials"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	MaxAttempts       = 5
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
	ExtraFileIDs    []int64
	PDFVersionID    int64
	ConfirmStalePDF bool
	Key             uuid.UUID
	Document        DocumentRef
	Snapshot        Snapshot
}
type Delivery struct {
	Attachments                                                                  []authorization.DeliveryFile
	From                                                                         string
	ID, CompanyID, DocumentID, ActorID                                           int64
	DocumentType, State, Subject, TextBody, HTMLBody, PDFFilename, MessageID     string
	To, CC, BCC                                                                  []string
	PDF                                                                          []byte
	ExpectedStatusID                                                             *int64
	ExpectedCommercialRevision                                                   *int64
	ExpectedStatusRevision, ExpectedMeaningRevision                              *uuid.UUID
	LifetimeAttemptCount, CycleAttemptCount, RetryCycle, OpenCount               int
	AttemptToken                                                                 uuid.UUID
	ProviderIdentifier, AcceptanceEvidence, LastError                            string
	TrackingEnabled                                                              bool
	FirstOpenAt, LastOpenAt, NextAttemptAt, LeaseExpiresAt, CreatedAt, UpdatedAt *time.Time
	AcceptedAt, DeliveredAt, BouncedAt, FailedAt                                 *time.Time
}
type Summary struct {
	NextAttemptAt, FirstOpenAt         *time.Time
	TrackingEnabled                    bool
	Events                             []HistoryEvent
	SenderRecorded                     bool
	ID                                 int64
	State, LastError                   string
	To                                 []string
	LifetimeAttemptCount, OpenCount    int
	CreatedAt, AcceptedAt, DeliveredAt *time.Time
	BouncedAt, FailedAt, LastOpenAt    *time.Time
}
type HistoryEvent struct {
	At                                               time.Time
	Kind, State, Reason, Provider, Attempt, Evidence string
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
	// Trusted adapters should supply both. Omission requires an unambiguous
	// retained acceptance, or an installed provider reference with explicitly
	// unknown attempt metadata.
	AttemptToken uuid.UUID
	MessageID    string
}

// ProviderEvidenceRecorder is a trusted adapter port. Adapters must authenticate
// provider callbacks and correlate their provider identifier before calling it.
type ProviderEvidenceRecorder interface {
	RecordProviderEvidence(context.Context, ProviderEvent) error
}

type Service struct {
	beforeWork func() error
	control    *instancecontrol.Control
	db         *pgxpool.Pool
	publicURL  string
	now        func() time.Time
}

func New(db *pgxpool.Pool, publicURL string) *Service {
	return &Service{db: db, publicURL: strings.TrimRight(publicURL, "/"), now: time.Now}
}

// SetInstanceControl configures admission before requests or workers start.
func (s *Service) SetInstanceControl(control *instancecontrol.Control) { s.control = control }

// SetBeforeWork installs the process-local connection-generation refresh. Set it
// during initialization; ProcessOne calls it after admission and before claiming.
func (s *Service) SetBeforeWork(fn func() error) { s.beforeWork = fn }

// Queue atomically creates one Delivery per distinct recipient, including copy
// recipients. It returns the first Delivery as the stable request identity;
// History exposes each independently tracked transmission. Existing legacy
// requests reconcile their original Delivery without rewriting its snapshot.
func (s *Service) Queue(ctx context.Context, a Actor, r QueueRequest) (Delivery, error) {
	ctx, release, err := s.control.EnterEmail(ctx)
	if err != nil {
		return Delivery{}, err
	}
	defer release()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Delivery{}, err
	}
	defer tx.Rollback(ctx)
	d, err := s.queueTx(ctx, tx, a, r)
	if err != nil {
		return Delivery{}, err
	}
	return d, tx.Commit(ctx)
}

func (s *Service) queueTx(ctx context.Context, tx pgx.Tx, a Actor, r QueueRequest) (Delivery, error) {
	if a.ID <= 0 || a.CompanyID <= 0 || r.Key == uuid.Nil || (r.Document.Type != "estimate" && r.Document.Type != "invoice") || r.Document.ID <= 0 || len(r.Snapshot.To) == 0 || strings.TrimSpace(r.Snapshot.Subject) == "" || len(r.Snapshot.PDF) == 0 || strings.TrimSpace(r.Snapshot.PDFFilename) == "" {
		return Delivery{}, ErrInvalid
	}
	r.Snapshot.To = canonicalAddresses(r.Snapshot.To)
	r.Snapshot.CC = canonicalAddresses(r.Snapshot.CC)
	r.Snapshot.BCC = canonicalAddresses(r.Snapshot.BCC)
	if len(r.Snapshot.To) == 0 {
		return Delivery{}, ErrInvalid
	}
	for _, group := range [][]string{r.Snapshot.To, r.Snapshot.CC, r.Snapshot.BCC} {
		for _, recipient := range group {
			address, err := mail.ParseAddress(recipient)
			if err != nil || address.Address != recipient || strings.ContainsAny(recipient, "\r\n") {
				return Delivery{}, ErrInvalid
			}
		}
	}
	var err error
	// Workflow configuration uses the authorization mutex before any row locks.
	// Capture a coherent immutable intent under that same fence, including on
	// the legacy queue path which does not call BindDeliveryPDF.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return Delivery{}, err
	}
	request, migrated := authorization.FromContext(ctx)
	var selected authorization.PDFDeliverySelection
	if migrated {
		if a.ID != request.Actor.UserID {
			return Delivery{}, ErrForbidden
		}
		selected, err = request.Service.BindDeliveryPDF(ctx, tx, request.Actor, a.CompanyID, r.Document.Type, r.Document.ID, r.PDFVersionID)
		if err != nil {
			return Delivery{}, err
		}
		r.Snapshot.PDF = selected.Version.Content
		r.Snapshot.PDFFilename = selected.Version.Filename
	}
	var jobID, statusID *int64
	var commercialRevision int64
	var statusRevision, meaningRevision *uuid.UUID
	q := fmt.Sprintf(`SELECT o.job_id,o.status_id,o.status_revision,s.meaning_revision,o.commercial_revision FROM %ss o JOIN customers c ON c.id=o.customer_id AND c.company_id=o.company_id LEFT JOIN statuses s ON s.id=o.status_id WHERE o.id=$1 AND o.company_id=$2 AND o.deleted_at IS NULL AND o.conversion_hidden_at IS NULL AND c.deleted_at IS NULL AND ($3<>'invoice' OR coalesce(s.category_key,'')<>'invoice:void') FOR SHARE OF o,c`, r.Document.Type)
	if err = tx.QueryRow(ctx, q, r.Document.ID, a.CompanyID, r.Document.Type).Scan(&jobID, &statusID, &statusRevision, &meaningRevision, &commercialRevision); errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNotFound
	} else if err != nil {
		return Delivery{}, err
	}
	if !migrated {
		if err = authorize(ctx, tx, a, jobID); err != nil {
			return Delivery{}, err
		}
	}
	var tracking bool
	var from string
	if err = tx.QueryRow(ctx, `SELECT email_tracking_enabled FROM company_settings WHERE company_id=$1`, a.CompanyID).Scan(&tracking); errors.Is(err, pgx.ErrNoRows) {
		tracking = false
	} else if err != nil {
		return Delivery{}, err
	}
	if err = tx.QueryRow(ctx, `SELECT smtp_from FROM company_settings WHERE company_id=$1 FOR SHARE`, a.CompanyID).Scan(&from); err != nil {
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
		if migrated && request.Actor.Channel != "api" {
			var bound bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_admissions WHERE operation_id=$1)`, r.Key).Scan(&bound); err != nil {
				return Delivery{}, err
			}
			if bound {
				if err := authorization.AdmitOperation(ctx, tx, request.Actor, r.Key.String(), r.Document.Type+".delivery.request", map[string]any{"document_id": r.Document.ID, "fingerprint": hex.EncodeToString(fingerprint)}); err != nil {
					return Delivery{}, err
				}
			}
		}
		d, e := get(ctx, tx, a.CompanyID, existingID)
		if e != nil {
			return Delivery{}, e
		}
		return d, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, err
	}
	if migrated && request.Actor.Channel != "api" {
		if err := authorization.AdmitOperation(ctx, tx, request.Actor, r.Key.String(), r.Document.Type+".delivery.request", map[string]any{"document_id": r.Document.ID, "fingerprint": hex.EncodeToString(fingerprint)}); err != nil {
			return Delivery{}, err
		}
	}
	if migrated && selected.Version.Stale && !r.ConfirmStalePDF {
		return Delivery{}, authorization.ErrConflict
	}
	if len(r.Snapshot.PDF) > authorization.MaxDeliveryAttachmentBytes || len(r.ExtraFileIDs) > authorization.MaxDeliveryExtraFiles {
		return Delivery{}, ErrInvalid
	}
	attachments := []authorization.DeliveryFile{}
	if len(r.ExtraFileIDs) > 0 {
		if !migrated {
			return Delivery{}, ErrForbidden
		}
		attachments, err = request.Service.BindDeliveryFiles(ctx, tx, request.Actor, r.Document.Type, r.Document.ID, r.ExtraFileIDs, len(r.Snapshot.PDF))
		if err != nil {
			return Delivery{}, err
		}
	}
	attachmentJSON, err := json.Marshal(attachments)
	if err != nil {
		return Delivery{}, err
	}
	settingsJSON, err := compositionSettings(ctx, tx, a.CompanyID, r.Document.Type)
	if err != nil {
		return Delivery{}, err
	}
	compositionJSON, err := json.Marshal(map[string]any{"settings": json.RawMessage(settingsJSON), "pdf_source": json.RawMessage(selected.Source), "pdf_source_known": selected.Version.SourceKnown})
	if err != nil {
		return Delivery{}, err
	}
	var host, password string
	var port int
	if err = tx.QueryRow(ctx, `SELECT smtp_host,smtp_port,smtp_password FROM company_settings WHERE company_id=$1`, a.CompanyID).Scan(&host, &port, &password); err != nil {
		return Delivery{}, err
	}
	if _, err = mail.ParseAddress(from); err != nil || host == "" || port < 1 || port > 65535 {
		return Delivery{}, fmt.Errorf("mail transport not configured")
	}
	if _, err = credentials.Open(password); err != nil {
		return Delivery{}, err
	}
	recipients := canonicalAddresses(append(append(append([]string{}, r.Snapshot.To...), r.Snapshot.CC...), r.Snapshot.BCC...))
	var first Delivery
	for i, recipient := range recipients {
		key := r.Key
		if i > 0 {
			key = uuid.NewSHA1(r.Key, []byte(recipient))
		}
		messageID := fmt.Sprintf("<delivery-%d-%s@freefsm.local>", a.CompanyID, key)
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
		err = tx.QueryRow(ctx, `INSERT INTO document_deliveries(company_id,document_type,document_id,estimate_id,invoice_id,actor_id,idempotency_key,request_fingerprint,recipients_to,recipients_cc,recipients_bcc,subject,text_body,html_body,pdf_data,pdf_filename,message_id,expected_status_id,tracking_enabled,tracking_token_hash,pdf_version_id,sender_address,expected_status_revision,expected_meaning_revision,attachment_snapshots,composition_snapshot,expected_commercial_revision)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,NULLIF($21,0),$22,$23,$24,$25,$26,$27) RETURNING id`, a.CompanyID, r.Document.Type, r.Document.ID, estimateID, invoiceID, a.ID, key, fingerprint, []string{recipient}, []string{}, []string{}, r.Snapshot.Subject, r.Snapshot.TextBody, html, r.Snapshot.PDF, r.Snapshot.PDFFilename, messageID, statusID, tracking, tokenHash, r.PDFVersionID, from, statusRevision, meaningRevision, attachmentJSON, compositionJSON, commercialRevision).Scan(&id)
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
		if migrated {
			if err := request.Service.AuditPDFDelivery(ctx, tx, request.Actor, r.Document.Type, r.Document.ID, r.PDFVersionID, id, selected.Version.Stale); err != nil {
				return Delivery{}, err
			}
		}
		if i == 0 {
			first = d
		}
	}
	return first, nil
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
	if r.PDFVersionID > 0 {
		status = nil
		tracking = false
	}
	pdf := sha256.Sum256(r.Snapshot.PDF)
	v := struct {
		Document                               DocumentRef `json:"document"`
		To, CC, BCC                            []string
		Subject, Text, HTML, Filename, PDFHash string
		Status                                 *int64
		Tracking                               bool
		PDFVersion                             int64   `json:"pdf_version,omitempty"`
		ExtraFiles                             []int64 `json:"extra_files,omitempty"`
	}{r.Document, r.Snapshot.To, r.Snapshot.CC, r.Snapshot.BCC, r.Snapshot.Subject, r.Snapshot.TextBody, r.Snapshot.HTMLBody, r.Snapshot.PDFFilename, hex.EncodeToString(pdf[:]), status, tracking, r.PDFVersionID, r.ExtraFileIDs}
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
	ctx, release, err := s.control.EnterEmail(ctx)
	if err != nil {
		return Delivery{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return Delivery{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Delivery{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return Delivery{}, err
	}
	if err = allowTransitions(ctx, tx); err != nil {
		return Delivery{}, err
	}
	cleaned, err := tx.Exec(ctx, `UPDATE document_deliveries SET state='cancelled',cancelled_at=clock_timestamp(),last_error='Original document or Customer is archived, hidden, or Void',updated_at=clock_timestamp() WHERE state='queued' AND NOT delivery_document_sendable(company_id,document_type,document_id)`)
	if err != nil {
		return Delivery{}, err
	}
	now := s.now()
	rows, err := tx.Query(ctx, `SELECT id,company_id,greatest(lifetime_attempt_count,cycle_attempt_count) FROM document_deliveries WHERE state='sending' AND lease_expires_at<=$1 FOR UPDATE`, now)
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
		var actual string
		e := tx.QueryRow(ctx, `UPDATE document_deliveries SET state=$1,attempt_token=NULL,lease_expires_at=NULL,next_attempt_at=$2,last_error='worker lease expired after ambiguous transport outcome',failed_at=$3,updated_at=$2 WHERE id=$4 AND state='sending' RETURNING state`, state, now, failed, x.id).Scan(&actual)
		if e != nil {
			return Delivery{}, e
		}
		if actual == "cancelled" {
			if err := event(ctx, tx, x.id, x.company, nil, "transport_failure", "", "", "", "", map[string]any{"reason": "lease_expired", "outcome": "ambiguous"}); err != nil {
				return Delivery{}, err
			}
			continue
		}
		if e = event(ctx, tx, x.id, x.company, nil, "state", "sending", state, "", "", map[string]any{"reason": "lease_expired", "outcome": "ambiguous"}); e != nil {
			return Delivery{}, e
		}
	}
	expired, err := expireAutomaticRetries(ctx, tx, now)
	if err != nil {
		return Delivery{}, err
	}
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM document_deliveries WHERE state='queued' AND sender_address IS NOT NULL AND sender_address<>'' AND next_attempt_at<=$1 ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, now).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) && (len(staleRows) > 0 || cleaned.RowsAffected() > 0 || expired > 0) {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return Delivery{}, commitErr
			}
		}
		return Delivery{}, err
	}
	token := uuid.New()
	lease := now.Add(LeaseDuration)
	tag, err := tx.Exec(ctx, `UPDATE document_deliveries SET state='sending',dispatch_boundary_known=true,handoff_started_at=NULL,lifetime_attempt_count=lifetime_attempt_count+1,cycle_attempt_count=cycle_attempt_count+1,attempt_token=$1,lease_expires_at=$2,updated_at=$3 WHERE id=$4 AND state='queued'`, token, lease, now, id)
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
	if d.State == "cancelled" {
		return d, tx.Commit(ctx)
	}
	if err = event(ctx, tx, id, d.CompanyID, nil, "state", "queued", "sending", "", "", map[string]any{"attempt_token": token, "cycle_attempt": d.CycleAttemptCount, "lifetime_attempt": d.LifetimeAttemptCount}); err != nil {
		return Delivery{}, err
	}
	return d, tx.Commit(ctx)
}

func (s *Service) ProcessOne(ctx context.Context, sender Sender, hook AcceptanceHook) (bool, error) {
	ctx, release, err := s.control.EnterEmail(ctx)
	if errors.Is(err, instancecontrol.ErrMaintenance) || errors.Is(err, instancecontrol.ErrEmailDisabled) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer release()
	if s.beforeWork != nil {
		if err := s.beforeWork(); err != nil {
			return false, err
		}
	}
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
	admitted, err := s.beginDispatch(ctx, d)
	if err != nil {
		return true, err
	}
	if !admitted {
		return true, nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, SendTimeout)
	result, sendErr := sender.Send(sendCtx, d)
	cancel()
	if sendErr != nil {
		return true, s.failAttempt(ctx, d, sendErr)
	}
	// Retry only persistence: SMTP has already accepted this immutable message.
	// The attempt token still fences lease recovery. A bounded independent
	// context also allows recording success when the send's caller is canceled.
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), SendTimeout)
	defer persistCancel()
	for attempt := 0; ; attempt++ {
		err = s.accept(persistCtx, d, result, hook)
		var pgErr *pgconn.PgError
		if err == nil || attempt >= 3 || !errors.As(err, &pgErr) || (pgErr.Code != "40001" && pgErr.Code != "40P01" && pgErr.Code != "55P03") {
			return true, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 25 * time.Millisecond)
		select {
		case <-persistCtx.Done():
			timer.Stop()
			return true, persistCtx.Err()
		case <-timer.C:
		}
	}
}

func (s *Service) accept(ctx context.Context, d Delivery, result SendResult, hook AcceptanceHook) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// Must precede even the delivery row lock, not just the hook's document
	// locks. Configuration takes this fence before status/document row locks.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	if err = allowTransitions(ctx, tx); err != nil {
		return err
	}
	evidence, _ := json.Marshal(result.Evidence)
	now := s.now()
	tag, err := tx.Exec(ctx, `UPDATE document_deliveries SET state='accepted',provider_identifier=$1,acceptance_evidence=$2,accepted_at=$3,attempt_token=NULL,lease_expires_at=NULL,last_error='',updated_at=$3 WHERE id=$4 AND attempt_token=$5 AND state='sending'`, result.ProviderIdentifier, evidence, now, d.ID, d.AttemptToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	if _, err = tx.Exec(ctx, `INSERT INTO document_delivery_events(delivery_id,company_id,event_kind,from_state,to_state,provider_identifier,evidence,attempt_token,message_id) VALUES($1,$2,'state','sending','accepted',$3,$4,$5,$6)`, d.ID, d.CompanyID, result.ProviderIdentifier, evidence, d.AttemptToken, d.MessageID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,'document_delivery_accepted',$3,$4,jsonb_build_object('delivery_id',$5::bigint))`, d.CompanyID, d.ActorID, d.DocumentType, d.DocumentID, d.ID); err != nil {
		return err
	}
	if err = hook.OnAccepted(ctx, tx, d); err != nil {
		return err
	}
	if err = reconcileEarlyProviderEvidence(ctx, tx, d, result.ProviderIdentifier); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) failAttempt(ctx context.Context, d Delivery, cause error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serialize retry admission with archive's cancellation scan, including a
	// transport that finishes after that scan but before archive commits.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	q := fmt.Sprintf(`SELECT o.id FROM %ss o JOIN customers c ON c.id=o.customer_id AND c.company_id=o.company_id WHERE o.id=$1 AND o.company_id=$2 FOR SHARE OF o,c`, d.DocumentType)
	if _, err = tx.Exec(ctx, q, d.DocumentID, d.CompanyID); err != nil {
		return err
	}
	if err = allowTransitions(ctx, tx); err != nil {
		return err
	}
	now := s.now()
	kind := classifySendError(cause)
	state := "queued"
	next := now.Add(retryDelay(d.CycleAttemptCount))
	deadline, err := retryDeadline(ctx, tx, d.ID)
	if err != nil {
		return err
	}
	var failed any = nil
	terminalReason := ""
	if kind == SendPermanent || max(d.LifetimeAttemptCount, d.CycleAttemptCount) >= MaxAttempts || deadline == nil || !next.Before(*deadline) {
		state = "failed"
		failed = now
		switch {
		case kind == SendPermanent:
			terminalReason = "Permanent transport failure; explicit manual retry required"
		case max(d.LifetimeAttemptCount, d.CycleAttemptCount) >= MaxAttempts:
			terminalReason = "Automatic retry allowance exhausted; explicit manual retry required"
		default:
			terminalReason = "Original automatic retry window unavailable or expired; explicit manual retry required"
		}
	}
	lastError := cause.Error()
	if terminalReason != "" {
		lastError += "; " + terminalReason
	}
	var actual string
	err = tx.QueryRow(ctx, `UPDATE document_deliveries SET state=$1,next_attempt_at=$2,attempt_token=NULL,lease_expires_at=NULL,last_error=$3,failed_at=$4,updated_at=$5 WHERE id=$6 AND attempt_token=$7 AND state='sending' RETURNING state`, state, next, lastError, failed, now, d.ID, d.AttemptToken).Scan(&actual)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if actual == "cancelled" {
		if err := event(ctx, tx, d.ID, d.CompanyID, nil, "transport_failure", "", "", "", "", map[string]any{"error": cause.Error(), "classification": kind, "attempt_token": d.AttemptToken}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err = event(ctx, tx, d.ID, d.CompanyID, nil, "state", "sending", state, "", "", map[string]any{"error": lastError, "classification": kind, "attempt_token": d.AttemptToken}); err != nil {
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
	ctx, release, err := s.control.EnterEmail(ctx)
	if err != nil {
		return err
	}
	defer release()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.manualRetryTx(ctx, tx, a, id, reason, nil, keys...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// manualRetryTx applies the frozen Delivery effect in its caller's transaction.
// expected binds API intent to its original document and Job. Browser callers
// retain their historical event-key semantics.
func (s *Service) manualRetryTx(ctx context.Context, tx pgx.Tx, a Actor, id int64, reason string, expected *RetryIntent, keys ...uuid.UUID) error {
	var err error
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	d, err := get(ctx, tx, a.CompanyID, id)
	if err != nil {
		return err
	}
	jobID, err := documentJobID(ctx, tx, d)
	if err != nil {
		return err
	}
	request, migrated := authorization.FromContext(ctx)
	if migrated {
		if a.ID != request.Actor.UserID {
			return ErrForbidden
		}
		if err = request.Service.AuthorizeDeliveryRetry(ctx, tx, request.Actor, a.CompanyID, d.DocumentType, d.DocumentID, len(d.Attachments) > 0); err != nil {
			return err
		}
	} else if err = authorize(ctx, tx, a, jobID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM document_deliveries WHERE id=$1 AND company_id=$2 FOR UPDATE`, id, a.CompanyID); err != nil {
		return err
	}
	d, err = get(ctx, tx, a.CompanyID, id)
	if err != nil {
		return err
	}
	if expected != nil && (d.DocumentType != expected.DocumentType || d.DocumentID != expected.DocumentID || jobID == nil || *jobID != expected.JobID) {
		return authorization.ErrConflict
	}
	attribution := map[string]any{}
	if expected != nil {
		attribution["operation_id"] = expected.OperationID
		if expected.OccurredAt != nil {
			attribution["occurred_at"] = expected.OccurredAt.UTC()
		}
	}
	if d.State == "accepted" {
		if expected != nil && migrated {
			return request.Service.AuditDeliveryRetry(ctx, tx, request.Actor, d.DocumentType, d.DocumentID, d.ID, reason, attribution)
		}
		return nil
	}
	if d.State == "cancelled" {
		if expected != nil {
			return &authorization.JobExecutionConflict{Code: "cancelled"}
		}
		return ErrInvalid
	}
	if d.From == "" {
		return ErrInvalid
	}
	var key uuid.UUID
	if len(keys) > 0 {
		key = keys[0]
		if key == uuid.Nil {
			return ErrInvalid
		}
	}
	if key != uuid.Nil {
		var originalID int64
		var originalReason string
		err = tx.QueryRow(ctx, `SELECT delivery_id,evidence->>'reason' FROM document_delivery_events WHERE company_id=$1 AND actor_id=$2 AND event_kind='state' AND evidence->>'manual_retry_key'=$3`, a.CompanyID, a.ID, key.String()).Scan(&originalID, &originalReason)
		if err == nil {
			if originalID != id || originalReason != reason {
				return ErrIdempotencyConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var previous *uuid.UUID
		if err = tx.QueryRow(ctx, `SELECT last_manual_retry_key FROM document_deliveries WHERE id=$1 AND company_id=$2`, id, a.CompanyID).Scan(&previous); err != nil {
			return err
		}
		if previous != nil && *previous == key {
			return nil
		}
	}
	if d.State != "failed" && d.State != "bounced" {
		return ErrInvalid
	}
	if err = allowTransitions(ctx, tx); err != nil {
		return err
	}
	now := s.now()
	tag, err := tx.Exec(ctx, `UPDATE document_deliveries SET state='queued',retry_cycle=retry_cycle+1,handoff_started_at=NULL,next_attempt_at=$1,failed_at=NULL,bounced_at=NULL,last_error='',updated_at=$1,last_manual_retry_key=NULLIF($4::uuid,'00000000-0000-0000-0000-000000000000') WHERE id=$2 AND company_id=$3 AND state IN ('failed','bounced')`, now, id, a.CompanyID, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	manualEvidence := map[string]any{"reason": reason, "manual": true}
	if key != uuid.Nil {
		manualEvidence["manual_retry_key"] = key.String()
	}
	if err = event(ctx, tx, id, a.CompanyID, &a.ID, "state", d.State, "queued", "", "", manualEvidence); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,'document_delivery_retried',$3,$4,jsonb_build_object('delivery_id',$5::bigint,'reason',$6::text))`, a.CompanyID, a.ID, d.DocumentType, d.DocumentID, id, reason); err != nil {
		return err
	}
	if migrated {
		if err = request.Service.AuditDeliveryRetry(ctx, tx, request.Actor, d.DocumentType, d.DocumentID, d.ID, reason, attribution); err != nil {
			return err
		}
	}
	return nil
}
func documentJobID(ctx context.Context, tx pgx.Tx, d Delivery) (*int64, error) {
	var id *int64
	err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT o.job_id FROM %ss o JOIN customers c ON c.id=o.customer_id AND c.company_id=o.company_id WHERE o.id=$1 AND o.company_id=$2 AND o.deleted_at IS NULL AND o.conversion_hidden_at IS NULL AND c.deleted_at IS NULL FOR SHARE OF o,c`, d.DocumentType), d.DocumentID, d.CompanyID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return id, err
}

func (s *Service) RecordProviderEvidence(ctx context.Context, p ProviderEvent) error {
	if p.CompanyID <= 0 || p.DeliveryID <= 0 || p.ProviderIdentifier == "" || p.EventID == "" || len(p.Evidence) == 0 || (p.State != "delivered" && p.State != "bounced") {
		return ErrInvalid
	}
	ctx, release, err := s.enterEvidence(ctx)
	if err != nil {
		return err
	}
	defer release()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state, messageID, currentProvider string
	if err = tx.QueryRow(ctx, `SELECT state,message_id,provider_identifier FROM document_deliveries WHERE id=$1 AND company_id=$2 FOR UPDATE`, p.DeliveryID, p.CompanyID).Scan(&state, &messageID, &currentProvider); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if p.MessageID != "" && p.MessageID != messageID {
		return ErrForbidden
	}
	var acceptance, latest int64
	var token *uuid.UUID
	rows, err := tx.Query(ctx, `SELECT id,attempt_token FROM document_delivery_events WHERE delivery_id=$1 AND company_id=$2 AND event_kind='state' AND to_state='accepted' AND provider_identifier=$3 AND ($4::uuid='00000000-0000-0000-0000-000000000000' OR attempt_token=$4)`, p.DeliveryID, p.CompanyID, p.ProviderIdentifier, p.AttemptToken)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		if err = rows.Scan(&acceptance, &token); err != nil {
			rows.Close()
			return err
		}
		count++
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	acceptedCorrelation := count == 1
	if count == 0 && p.AttemptToken == uuid.Nil && currentProvider == p.ProviderIdentifier && (state == "accepted" || state == "delivered" || state == "bounced") {
		// Installed acceptance projections may have a provider reference but no
		// attempt metadata. Retain new authentic evidence with unknown attempt
		// correlation, rather than inventing a historical acceptance event.
		count = 1
		acceptedCorrelation = true
	}
	if count == 0 && p.AttemptToken != uuid.Nil && p.MessageID == messageID {
		// An authenticated callback can precede the transport response or arrive
		// after an ambiguous crash. Exact token AND message identity are required
		// to retain it against dispatch; this does not manufacture acceptance.
		err = tx.QueryRow(ctx, `SELECT id FROM document_delivery_events WHERE delivery_id=$1 AND company_id=$2 AND event_kind='dispatch' AND evidence->>'attempt_token'=$3`, p.DeliveryID, p.CompanyID, p.AttemptToken.String()).Scan(&acceptance)
		if err == nil {
			count = 1
			token = &p.AttemptToken
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if count != 1 {
		return ErrForbidden
	}
	if err = tx.QueryRow(ctx, `SELECT coalesce(max(id),0) FROM document_delivery_events WHERE delivery_id=$1 AND event_kind='state' AND to_state='accepted'`, p.DeliveryID).Scan(&latest); err != nil {
		return err
	}
	b, err := json.Marshal(p.Evidence)
	if err != nil {
		return ErrInvalid
	}
	tag, err := tx.Exec(ctx, `INSERT INTO document_delivery_events(delivery_id,company_id,event_kind,provider_identifier,provider_event_id,evidence,attempt_token,message_id,provider_state,transport_event_id) VALUES($1,$2,'provider',$3,$4,$5,$6,$7,$8,NULLIF($9,0)) ON CONFLICT DO NOTHING`, p.DeliveryID, p.CompanyID, p.ProviderIdentifier, p.EventID, b, token, messageID, p.State, acceptance)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var same bool
		if err = tx.QueryRow(ctx, `SELECT delivery_id=$1 AND evidence=$2::jsonb AND (provider_state=$3 OR provider_state IS NULL) AND attempt_token IS NOT DISTINCT FROM $4::uuid FROM document_delivery_events WHERE company_id=$5 AND provider_identifier=$6 AND provider_event_id=$7`, p.DeliveryID, b, p.State, token, p.CompanyID, p.ProviderIdentifier, p.EventID).Scan(&same); err != nil {
			return err
		}
		if !same {
			return ErrIdempotencyConflict
		}
		return tx.Commit(ctx)
	}
	if !acceptedCorrelation || (acceptance != 0 && acceptance != latest) || (state != "accepted" && state != "delivered") || (state == "delivered" && p.State == "delivered") {
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
	ctx, release, err := s.enterEvidence(ctx)
	if err != nil {
		return err
	}
	defer release()
	sum := sha256.Sum256([]byte(token))
	now := s.now()
	tag, err := s.db.Exec(ctx, `UPDATE document_deliveries SET first_open_at=least(coalesce(first_open_at,$1),$1),last_open_at=greatest(coalesce(last_open_at,$1),$1),open_count=open_count+1,updated_at=$1 WHERE tracking_token_hash=$2`, now, sum[:])
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
	rows, err := s.db.Query(ctx, `SELECT id,state,CASE WHEN state='sending' AND dispatch_boundary_known IS NULL THEN 'Legacy transport handoff unknown; await lease recovery and review the original intent' WHEN state IN ('queued','failed','bounced') AND coalesce(sender_address,'')='' THEN 'Legacy sender snapshot unknown; explicit review and a new Delivery are required' ELSE last_error END,recipients_to,lifetime_attempt_count,open_count,created_at,accepted_at,delivered_at,bounced_at,failed_at,last_open_at,coalesce(sender_address,'')<>'',CASE WHEN state='queued' AND coalesce(sender_address,'')<>'' THEN next_attempt_at END,first_open_at,tracking_enabled FROM document_deliveries WHERE company_id=$1 AND document_type=$2 AND document_id=$3 ORDER BY created_at DESC,id DESC`, company, ref.Type, ref.ID)
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
		if err = rows.Scan(&item.ID, &item.State, &item.LastError, &item.To, &item.LifetimeAttemptCount, &item.OpenCount, &item.CreatedAt, &item.AcceptedAt, &item.DeliveredAt, &item.BouncedAt, &item.FailedAt, &item.LastOpenAt, &item.SenderRecorded, &item.NextAttemptAt, &item.FirstOpenAt, &item.TrackingEnabled); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	indices := map[int64]int{}
	for i := range out {
		indices[out[i].ID] = i
	}
	events, err := s.db.Query(ctx, `SELECT e.delivery_id,e.created_at,e.event_kind,coalesce(e.provider_state,e.to_state,''),coalesce(e.evidence->>'error',e.evidence->>'reason',''),e.provider_identifier,coalesce(e.attempt_token::text,e.evidence->>'attempt_token',''),CASE WHEN e.event_kind='provider' THEN e.evidence::text ELSE '' END FROM document_delivery_events e JOIN document_deliveries d ON d.id=e.delivery_id AND d.company_id=e.company_id WHERE d.company_id=$1 AND d.document_type=$2 AND d.document_id=$3 ORDER BY e.id`, company, ref.Type, ref.ID)
	if err != nil {
		return nil, err
	}
	defer events.Close()
	for events.Next() {
		var id int64
		var e HistoryEvent
		if err = events.Scan(&id, &e.At, &e.Kind, &e.State, &e.Reason, &e.Provider, &e.Attempt, &e.Evidence); err != nil {
			return nil, err
		}
		if i, ok := indices[id]; ok {
			out[i].Events = append(out[i].Events, e)
		}
	}
	return out, events.Err()
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
	if err == nil {
		var raw []byte
		err = q.QueryRow(ctx, `SELECT coalesce(sender_address,''),expected_status_revision,expected_meaning_revision,attachment_snapshots,expected_commercial_revision FROM document_deliveries WHERE id=$1`, d.ID).Scan(&d.From, &d.ExpectedStatusRevision, &d.ExpectedMeaningRevision, &raw, &d.ExpectedCommercialRevision)
		if err == nil && len(raw) > 0 {
			err = json.Unmarshal(raw, &d.Attachments)
		}
	}
	return d, err
}
