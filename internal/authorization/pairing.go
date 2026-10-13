package authorization

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrPairingInvalid = errors.New("invalid pairing request")
var ErrPairingUnavailable = errors.New("pairing expired, consumed or unavailable")

type PairingStart struct {
	DeviceCode      string    `json:"device_code"`
	UserCode        string    `json:"user_code"`
	ExpiresAt       time.Time `json:"expires_at"`
	VerificationURI string    `json:"verification_uri"`
}
type PairingReview struct {
	DeviceName string
	Token      string
	ExpiresAt  time.Time
}

func pairingSecret(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func pairingHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func pairingOperation(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && len(s) == 36 && strings.EqualFold(id.String(), s)
}
func pairingDeviceCode(s string) bool {
	decoded, err := hex.DecodeString(s)
	return err == nil && len(decoded) == 32 && strings.ToLower(s) == s
}
func pairingUniqueError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrPairingUnavailable
	}
	return err
}
func pairingUnexpired(ctx context.Context, tx pgx.Tx, id int64) error {
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM device_pairings WHERE id=$1`, id).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrPairingUnavailable
	}
	return nil
}

// StartPairing has no account identifier. The browser receives only the user
// code; the separate high-entropy device secret never travels in a URL.
func (s *Service) StartPairing(ctx context.Context, operation, device string) (PairingStart, error) {
	out := PairingStart{}
	if !pairingOperation(operation) || device == "" || !utf8.ValidString(device) || utf8.RuneCountInString(device) > 100 || strings.IndexFunc(device, unicode.IsControl) >= 0 {
		return out, ErrPairingInvalid
	}
	device = strings.TrimSpace(device)
	if device == "" {
		return out, ErrPairingInvalid
	}
	out.DeviceCode = pairingSecret(32)
	out.UserCode = strings.ToUpper(pairingSecret(8))
	out.VerificationURI = "/pair"
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO device_pairings(operation_id,device_hash,code_hash,device_name) VALUES($1,$2,$3,$4) RETURNING id,expires_at`, operation, pairingHash(out.DeviceCode), pairingHash(out.UserCode), device).Scan(&id, &out.ExpiresAt)
	if err != nil {
		return out, pairingUniqueError(err)
	}
	if err = protectedaudit.RecordPairingRequest(ctx, tx, id, "user.pairing.start", operation); err != nil {
		return out, err
	}
	if err = pairingUnexpired(ctx, tx, id); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// Review binds the displayed immutable device label to this exact browser
// session. Re-review invalidates a previous interrupted confirmation.
func (s *Service) ReviewPairing(ctx context.Context, a Actor, code string) (PairingReview, error) {
	out := PairingReview{Token: pairingSecret(32)}
	if a.Channel != "web" || a.SessionHash == "" {
		return out, ErrForbidden
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		err := tx.QueryRow(ctx, `UPDATE device_pairings SET review_hash=$2,browser_hash=$3,user_id=$4 WHERE code_hash=$1 AND state='pending' AND expires_at>clock_timestamp() RETURNING device_name,expires_at`, pairingHash(strings.ToUpper(strings.TrimSpace(code))), pairingHash(out.Token), a.SessionHash, p.UserID).Scan(&out.DeviceName, &out.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPairingUnavailable
		}
		return err
	})
	return out, err
}

func (s *Service) ApprovePairing(ctx context.Context, a Actor, review, device string) error {
	if a.Channel != "web" || a.SessionHash == "" {
		return ErrForbidden
	}
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var id int64
		err := tx.QueryRow(ctx, `UPDATE device_pairings SET state='approved',approved_at=clock_timestamp(),credential_revision=(SELECT credential_revision FROM users WHERE id=$2) WHERE review_hash=$1 AND user_id=$2 AND browser_hash=$3 AND device_name=$4 AND state='pending' AND expires_at>clock_timestamp() RETURNING id`, pairingHash(review), a.UserID, a.SessionHash, device).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPairingUnavailable
		}
		if err != nil {
			return err
		}
		if err := audit(ctx, tx, a, p, "user.pairing.approve", Own, "device_pairing", id, map[string]any{"device": device}); err != nil {
			return err
		}
		return pairingUnexpired(ctx, tx, id)
	})
}

// Redeem consumes durably before returning the only copy of the bearer. Lost
// responses require explicit new pairing, never a replay-created session.
func (s *Service) RedeemPairing(ctx context.Context, deviceCode, operation string) (string, error) {
	if !pairingOperation(operation) || !pairingDeviceCode(deviceCode) {
		return "", ErrPairingInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return "", err
	}
	var id int64
	var a Actor
	var device string
	a.Channel = "web"
	err = tx.QueryRow(ctx, `SELECT p.id,p.user_id,p.browser_hash,p.device_name FROM device_pairings p JOIN users u ON u.id=p.user_id WHERE p.device_hash=$1 AND p.state='approved' AND p.expires_at>clock_timestamp() AND p.credential_revision=u.credential_revision FOR UPDATE OF p`, pairingHash(deviceCode)).Scan(&id, &a.UserID, &a.SessionHash, &device)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrPairingUnavailable
	}
	if err != nil {
		return "", err
	}
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return "", err
	}
	token := pairingSecret(32)
	var sessionID int64
	err = tx.QueryRow(ctx, `WITH instant AS (SELECT clock_timestamp() AS t) INSERT INTO sessions(token_hash,user_id,kind,device_name,created_at,last_used_at,expires_at) SELECT $1,$2,'mobile',$3,t,t,t+interval '720 hours' FROM instant RETURNING id`, pairingHash(token), a.UserID, device).Scan(&sessionID)
	if err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `UPDATE device_pairings SET state='redeemed',consumed_at=clock_timestamp(),session_id=$2,redeem_operation_id=$3 WHERE id=$1 AND expires_at>clock_timestamp()`, id, sessionID, operation)
	if err != nil {
		return "", pairingUniqueError(err)
	}
	if tag.RowsAffected() != 1 {
		return "", ErrPairingUnavailable
	}
	apiActor := a
	apiActor.Channel = "api"
	// Issuance is attributed to the new device session. Keep a unchanged: the
	// approving browser remains the authority revalidated before commit below.
	apiActor.SessionHash = pairingHash(token)
	if err = audit(ctx, tx, apiActor, p, "user.pairing.redeem", Own, "session", sessionID, map[string]any{"pairing_id": id, "device": device, "operation_id": operation}); err != nil {
		return "", err
	}
	if _, err = principal(ctx, tx, a, false); err != nil {
		return "", err
	}
	if err = pairingUnexpired(ctx, tx, id); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) CancelPairing(ctx context.Context, deviceCode, operation string) error {
	if !pairingDeviceCode(deviceCode) || !pairingOperation(operation) {
		return ErrPairingInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	var id int64
	var state, previous string
	err = tx.QueryRow(ctx, `SELECT id,state,coalesce(cancel_operation_id::text,'') FROM device_pairings WHERE device_hash=$1 FOR UPDATE`, pairingHash(deviceCode)).Scan(&id, &state, &previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPairingUnavailable
	}
	if err != nil {
		return err
	}
	if state == "cancelled" && strings.EqualFold(previous, operation) {
		return nil
	}
	if state != "pending" && state != "approved" {
		return ErrPairingUnavailable
	}
	if err = pairingUnexpired(ctx, tx, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE device_pairings SET state='cancelled',consumed_at=clock_timestamp(),cancel_operation_id=$2 WHERE id=$1`, id, operation); err != nil {
		return pairingUniqueError(err)
	}
	if err = protectedaudit.RecordPairingRequest(ctx, tx, id, "user.pairing.cancel", operation); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
