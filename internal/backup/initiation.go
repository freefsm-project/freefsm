package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

var ErrInitiated = errors.New("backup action already admitted; query the same capability")
var initiationID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// InitiationReceipt is a status-only capability projection. Unlike Operation it
// intentionally contains no actor/source/filename or historical private values.
type InitiationReceipt struct {
	ActionID    string `json:"action_id"`
	OperationID string `json:"operation_id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Disposition string `json:"disposition"`
	Phase       string `json:"phase,omitempty"`
	Available   bool   `json:"available"`
	Code        string `json:"code,omitempty"`
}
type initiationEvidence struct {
	Receipt     InitiationReceipt
	ActorID     int64
	Destination string
}

// Admissions live outside application backups AND ephemeral job directories.
// Restore may replace the database; recovery may discard interrupted artifacts.
// Neither event is permission to repeat an admitted operation capability.
func (m *Manager) initiationRoot() string { return filepath.Join(m.cfg.StateDir, "backup-admissions") }
func (m *Manager) initiationPath(id string) string {
	return filepath.Join(m.initiationRoot(), id+".json")
}

func (m *Manager) admitInitiation(actor ActivityActor, id, kind string) error {
	if !initiationID.MatchString(id) || !m.validActor(actor) {
		return ErrInvalid
	}
	if err := privateDir(m.initiationRoot()); err != nil {
		return err
	}
	if err := syncDir(m.cfg.StateDir); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(m.initiationRoot(), "admission.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if _, err := os.Lstat(m.initiationPath(id)); err == nil {
		return ErrInitiated
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	e := initiationEvidence{ActorID: actor.ID, Destination: m.destinationIdentity(), Receipt: InitiationReceipt{ActionID: id, OperationID: id, Kind: kind, Disposition: "unknown", Phase: "admitted"}}
	return atomicJSON(m.initiationPath(id), e)
}

func (m *Manager) readInitiation(id string) (initiationEvidence, error) {
	var e initiationEvidence
	if !initiationID.MatchString(id) {
		return e, ErrInvalid
	}
	data, err := os.ReadFile(m.initiationPath(id))
	if err != nil {
		return e, err
	}
	if err = json.Unmarshal(data, &e); err != nil {
		return e, err
	}
	if e.Destination != m.destinationIdentity() || e.Receipt.ActionID != id {
		return e, ErrInvalid
	}
	return e, nil
}

func (m *Manager) InitiationStatus(id string) (InitiationReceipt, error) {
	e, err := m.readInitiation(id)
	if errors.Is(err, os.ErrNotExist) {
		return InitiationReceipt{ActionID: id, Disposition: "not_received"}, nil
	}
	if err != nil {
		return InitiationReceipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	if j := m.jobs[id]; j != nil && j.Kind == e.Receipt.Kind {
		e.Receipt.Phase = j.Phase
		e.Receipt.Available = true
		e.Receipt.Disposition = "accepted"
		if !j.active {
			e.Receipt.Disposition = j.Phase
		}
	}
	return e.Receipt, nil
}

// Caller holds m.mu. Terminal facts are persisted after the real engine/audit
// outcome, never from admission or merely handing work to a goroutine.
func (m *Manager) finishInitiation(j *job) error {
	if !j.initiation {
		return nil
	}
	if err := m.check("initiation-result"); err != nil {
		return err
	}
	e, err := m.readInitiation(j.ID)
	if err != nil {
		return err
	}
	e.Receipt.Disposition = j.Phase
	e.Receipt.Phase = j.Phase
	e.Receipt.Available = false
	e.Receipt.Code = string(j.Failure.Category)
	return atomicJSON(m.initiationPath(j.ID), e)
}

func (m *Manager) rejectInitiation(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs[id] != nil {
		return
	} // a dispatched outcome must remain queryable
	e, err := m.readInitiation(id)
	if err != nil {
		return
	}
	e.Receipt.Disposition = "rejected"
	e.Receipt.Phase = "rejected"
	_ = atomicJSON(m.initiationPath(id), e) // failed evidence stays unknown, never replayable
}

func (m *Manager) StartBackupAction(actor ActivityActor, id, source, password string) (Operation, error) {
	if err := m.admitInitiation(actor, id, "backup"); err != nil {
		return Operation{}, err
	}
	op, err := m.startBackupFor(actor, source, password, id)
	if err != nil {
		m.rejectInitiation(id)
	}
	return op, err
}
func (m *Manager) StageUploadAction(ctx context.Context, actor ActivityActor, id string, r io.Reader, password string) (Operation, error) {
	if err := m.admitInitiation(actor, id, "upload"); err != nil {
		return Operation{}, err
	}
	op, err := m.stageUploadFor(ctx, actor, r, password, id)
	if err != nil {
		m.rejectInitiation(id)
	}
	return op, err
}
