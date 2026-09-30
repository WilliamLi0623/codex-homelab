package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var (
	ErrOutcomeUnknown       = errors.New("session runtime operation outcome is unknown")
	ErrVMIDOccupied         = errors.New("allocated session VMID is already occupied in Proxmox")
	ErrRuntimeNotReady      = errors.New("session runtime did not reach the requested state")
	ErrReconciliationNeeded = errors.New("session runtime needs operator reconciliation")
	ErrInvalidConfig        = errors.New("session runtime configuration is invalid")
)

const SessionSystemStorage = "local"

type RuntimeState string

const (
	RuntimeMissing RuntimeState = "MISSING"
	RuntimeRunning RuntimeState = "RUNNING"
	RuntimeStopped RuntimeState = "STOPPED"
	RuntimeUnknown RuntimeState = "UNKNOWN"
)

type RuntimeRequest struct {
	VMID              int
	SessionID         string
	EpochID           string
	Generation        string
	TemplateVMID      int
	SystemStorage     string
	WorkspaceVolumeID string
	WorkspaceStorage  string
	WorkspaceSizeGiB  int
	Hostname          string
}

// Runtime is the dedicated interactive-Session boundary. It deliberately
// excludes K3s, worker cleanup, and short-lived capacity operations.
type Runtime interface {
	TargetAvailable(context.Context, int) (bool, error)
	Clone(context.Context, RuntimeRequest) (string, error)
	WorkspaceVolume(context.Context, int) (string, error)
	DetachWorkspace(context.Context, int, string) error
	WorkspaceVolumeExists(context.Context, string) (bool, error)
	Start(context.Context, int) error
	Stop(context.Context, int) error
	Observe(context.Context, int) (RuntimeState, error)
	VerifyIdentity(context.Context, store.SessionRuntimeBinding) error
	Delete(context.Context, int) error
}

type Config struct {
	TemplateVMID     int
	SystemStorage    string
	WorkspaceStorage string
	WorkspaceSizeGiB int
}

type Manager struct {
	store   *store.Store
	runtime Runtime
	config  Config
	clock   func() time.Time
}

func NewManager(db *store.Store, runtime Runtime, config Config) (*Manager, error) {
	if db == nil || runtime == nil || config.TemplateVMID < 3900 || config.TemplateVMID > 3902 || config.SystemStorage != SessionSystemStorage || config.WorkspaceStorage != "pool" || config.WorkspaceSizeGiB < 1 {
		return nil, ErrInvalidConfig
	}
	return &Manager{store: db, runtime: runtime, config: config, clock: func() time.Time { return time.Now().UTC() }}, nil
}

type CreateRequest struct {
	ID         string
	SessionID  string
	EpochID    string
	Generation string
}

func (m *Manager) Create(ctx context.Context, request CreateRequest) (store.SessionRuntimeBinding, error) {
	now := m.clock()
	binding, err := m.store.AllocateSessionRuntimeBinding(ctx, store.SessionRuntimeBinding{
		ID: request.ID, SessionID: request.SessionID, EpochID: request.EpochID,
		Generation: request.Generation, State: "ALLOCATING", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return store.SessionRuntimeBinding{}, err
	}
	return m.createAllocated(ctx, binding)
}

func (m *Manager) createAllocated(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionRuntimeBinding, error) {
	available, err := m.runtime.TargetAvailable(ctx, binding.VMID)
	if err != nil {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "ALLOCATING", err))
	}
	if !available {
		return m.currentBindingWithError(ctx, binding, m.markFailed(ctx, binding, "ALLOCATING", ErrVMIDOccupied))
	}
	if err := m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "ALLOCATING", "CREATING", "", m.clock()); err != nil {
		return m.currentBindingWithError(ctx, binding, err)
	}
	runtimeRequest := RuntimeRequest{
		VMID: binding.VMID, SessionID: binding.SessionID, EpochID: binding.EpochID,
		Generation: binding.Generation, TemplateVMID: m.config.TemplateVMID,
		SystemStorage: m.config.SystemStorage, WorkspaceStorage: m.config.WorkspaceStorage,
		WorkspaceSizeGiB: m.config.WorkspaceSizeGiB,
		Hostname:         sessionHostname(binding.SessionID, binding.EpochID, binding.Generation),
	}
	session, err := m.store.GetSession(ctx, binding.SessionID)
	if err != nil {
		return m.currentBindingWithError(ctx, binding, err)
	}
	runtimeRequest.WorkspaceVolumeID = session.WorkspaceVolumeID
	workspaceVolumeID, err := m.runtime.Clone(ctx, runtimeRequest)
	if err != nil {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "CREATING", err))
	}
	if session.WorkspaceVolumeID == "" {
		if err := m.store.SetSessionWorkspaceVolume(ctx, binding.SessionID, workspaceVolumeID); err != nil {
			return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "CREATING", err))
		}
	} else if workspaceVolumeID != session.WorkspaceVolumeID {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "CREATING", errors.New("runtime attached a different Session workspace volume")))
	}
	if err := m.runtime.VerifyIdentity(ctx, binding); err != nil {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "CREATING", err))
	}
	if err := m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "CREATING", "STARTING", "", m.clock()); err != nil {
		return m.currentBindingWithError(ctx, binding, err)
	}
	if err := m.runtime.Start(ctx, binding.VMID); err != nil {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "STARTING", err))
	}
	state, err := m.runtime.Observe(ctx, binding.VMID)
	if err != nil {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "STARTING", err))
	}
	if state != RuntimeRunning {
		return m.currentBindingWithError(ctx, binding, m.markFailed(ctx, binding, "STARTING", ErrRuntimeNotReady))
	}
	if err := m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "STARTING", "READY", "", m.clock()); err != nil {
		return m.currentBindingWithError(ctx, binding, err)
	}
	binding.State = "READY"
	binding.PendingOperation = ""
	return binding, nil
}

// Replace destroys only the exact stopped Session runtime, archives its
// generation, then provisions a replacement for the same logical epoch.
// Workspace data must be mounted independently from the runtime root disk.
func (m *Manager) Replace(ctx context.Context, sessionID, epochID, generation string) (store.SessionRuntimeBinding, error) {
	if strings.TrimSpace(generation) != generation || generation == "" {
		return store.SessionRuntimeBinding{}, fmt.Errorf("replacement generation is required: %w", ErrInvalidConfig)
	}
	binding, err := m.store.GetSessionRuntimeBinding(ctx, sessionID, epochID)
	if err != nil {
		return store.SessionRuntimeBinding{}, err
	}
	if generation == binding.Generation {
		return binding, fmt.Errorf("replacement generation must differ from the current generation: %w", ErrInvalidConfig)
	}
	if binding.State == "READY" {
		if err := m.Stop(ctx, sessionID, epochID); err != nil {
			return m.currentBindingWithError(ctx, binding, err)
		}
		binding, err = m.store.GetSessionRuntimeBinding(ctx, sessionID, epochID)
		if err != nil {
			return store.SessionRuntimeBinding{}, err
		}
	}
	if binding.State != "STOPPED" {
		return binding, fmt.Errorf("replace session runtime in state %s: %w", binding.State, store.ErrSessionRuntimeBindingConflict)
	}
	if err := m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "STOPPED", "DELETING", "", m.clock()); err != nil {
		return m.currentBindingWithError(ctx, binding, err)
	}
	if err := m.runtime.VerifyIdentity(ctx, binding); err != nil {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "DELETING", err))
	}
	session, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "DELETING", err))
	}
	if session.WorkspaceVolumeID == "" {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "DELETING", errors.New("Session workspace volume is not recorded")))
	}
	if err := m.runtime.DetachWorkspace(ctx, binding.VMID, session.WorkspaceVolumeID); err != nil {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "DELETING", err))
	}
	if err := m.runtime.Delete(ctx, binding.VMID); err != nil {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "DELETING", err))
	}
	state, err := m.runtime.Observe(ctx, binding.VMID)
	if err != nil {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "DELETING", err))
	}
	if state != RuntimeMissing {
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "DELETING", ErrRuntimeNotReady))
	}
	workspaceExists, err := m.runtime.WorkspaceVolumeExists(ctx, session.WorkspaceVolumeID)
	if err != nil || !workspaceExists {
		if err == nil {
			err = errors.New("detached Session workspace volume was not found in Proxmox storage")
		}
		return m.currentBindingWithError(ctx, binding, m.markUnknown(ctx, binding, "DELETING", err))
	}
	if err := m.store.CompleteSessionRuntimeReplacement(ctx, binding.ID, generation, m.clock()); err != nil {
		return m.currentBindingWithError(ctx, binding, err)
	}
	binding, err = m.store.GetSessionRuntimeBinding(ctx, sessionID, epochID)
	if err != nil {
		return store.SessionRuntimeBinding{}, err
	}
	return m.createAllocated(ctx, binding)
}

func (m *Manager) currentBindingWithError(ctx context.Context, fallback store.SessionRuntimeBinding, cause error) (store.SessionRuntimeBinding, error) {
	current, err := m.store.GetSessionRuntimeBinding(ctx, fallback.SessionID, fallback.EpochID)
	if err != nil {
		return fallback, fmt.Errorf("%w (refresh Session runtime binding: %v)", cause, err)
	}
	return current, cause
}

func (m *Manager) Stop(ctx context.Context, sessionID, epochID string) error {
	binding, err := m.store.GetSessionRuntimeBinding(ctx, sessionID, epochID)
	if err != nil {
		return err
	}
	if binding.State != "READY" {
		return fmt.Errorf("stop session runtime in state %s: %w", binding.State, store.ErrSessionRuntimeBindingConflict)
	}
	if err := m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "READY", "STOPPING", "", m.clock()); err != nil {
		return err
	}
	if err := m.runtime.VerifyIdentity(ctx, binding); err != nil {
		return m.markUnknown(ctx, binding, "STOPPING", err)
	}
	if err := m.runtime.Stop(ctx, binding.VMID); err != nil {
		return m.markUnknown(ctx, binding, "STOPPING", err)
	}
	state, err := m.runtime.Observe(ctx, binding.VMID)
	if err != nil {
		return m.markUnknown(ctx, binding, "STOPPING", err)
	}
	if state != RuntimeStopped {
		return m.markUnknown(ctx, binding, "STOPPING", ErrRuntimeNotReady)
	}
	return m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "STOPPING", "STOPPED", "", m.clock())
}

func (m *Manager) Resume(ctx context.Context, sessionID, epochID string) error {
	binding, err := m.store.GetSessionRuntimeBinding(ctx, sessionID, epochID)
	if err != nil {
		return err
	}
	if binding.State != "STOPPED" {
		return fmt.Errorf("resume session runtime in state %s: %w", binding.State, store.ErrSessionRuntimeBindingConflict)
	}
	if err := m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "STOPPED", "RESUMING", "", m.clock()); err != nil {
		return err
	}
	if err := m.runtime.VerifyIdentity(ctx, binding); err != nil {
		return m.markUnknown(ctx, binding, "RESUMING", err)
	}
	if err := m.runtime.Start(ctx, binding.VMID); err != nil {
		return m.markUnknown(ctx, binding, "RESUMING", err)
	}
	state, err := m.runtime.Observe(ctx, binding.VMID)
	if err != nil {
		return m.markUnknown(ctx, binding, "RESUMING", err)
	}
	if state != RuntimeRunning {
		return m.markUnknown(ctx, binding, "RESUMING", ErrRuntimeNotReady)
	}
	return m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "RESUMING", "READY", "", m.clock())
}

func (m *Manager) Delete(ctx context.Context, sessionID, epochID string) error {
	binding, err := m.store.GetSessionRuntimeBinding(ctx, sessionID, epochID)
	if err != nil {
		return err
	}
	if binding.State != "STOPPED" && binding.State != "FAILED" {
		return fmt.Errorf("delete session runtime in state %s: %w", binding.State, store.ErrSessionRuntimeBindingConflict)
	}
	session, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.WorkspaceVolumeID == "" {
		return errors.New("refusing to delete Session runtime without a recorded persistent workspace volume")
	}
	if err := m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, binding.State, "DELETING", "", m.clock()); err != nil {
		return err
	}
	if err := m.runtime.VerifyIdentity(ctx, binding); err != nil {
		return m.markUnknown(ctx, binding, "DELETING", err)
	}
	if err := m.runtime.DetachWorkspace(ctx, binding.VMID, session.WorkspaceVolumeID); err != nil {
		return m.markUnknown(ctx, binding, "DELETING", err)
	}
	if err := m.runtime.Delete(ctx, binding.VMID); err != nil {
		return m.markUnknown(ctx, binding, "DELETING", err)
	}
	state, err := m.runtime.Observe(ctx, binding.VMID)
	if err != nil {
		return m.markUnknown(ctx, binding, "DELETING", err)
	}
	if state != RuntimeMissing {
		return m.markUnknown(ctx, binding, "DELETING", ErrRuntimeNotReady)
	}
	workspaceExists, err := m.runtime.WorkspaceVolumeExists(ctx, session.WorkspaceVolumeID)
	if err != nil || !workspaceExists {
		if err == nil {
			err = errors.New("detached Session workspace volume was not found in Proxmox storage")
		}
		return m.markUnknown(ctx, binding, "DELETING", err)
	}
	return m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "DELETING", "DELETED", "", m.clock())
}

// Reconcile reads Proxmox state only. It never retries clone/start/stop/delete.
func (m *Manager) Reconcile(ctx context.Context, sessionID, epochID string) (store.SessionRuntimeBinding, error) {
	binding, err := m.store.GetSessionRuntimeBinding(ctx, sessionID, epochID)
	if err != nil {
		return store.SessionRuntimeBinding{}, err
	}
	if binding.State != "UNKNOWN" || binding.PendingOperation == "" {
		return binding, fmt.Errorf("session runtime has no reconcilable pending operation: %w", ErrReconciliationNeeded)
	}
	if binding.PendingOperation == "start" || binding.PendingOperation == "stop" {
		if err := m.runtime.VerifyIdentity(ctx, binding); err != nil {
			return binding, fmt.Errorf("verify runtime before reconciliation: %w", err)
		}
	}
	state, err := m.runtime.Observe(ctx, binding.VMID)
	if err != nil {
		return binding, err
	}
	var next string
	switch binding.PendingOperation {
	case "create":
		switch state {
		case RuntimeRunning:
			if err := m.runtime.VerifyIdentity(ctx, binding); err != nil {
				return binding, fmt.Errorf("verify ambiguous creation: %w", err)
			}
			if err := m.reconcileWorkspaceVolume(ctx, binding); err != nil {
				return binding, err
			}
			next = "READY"
		case RuntimeStopped:
			if err := m.runtime.VerifyIdentity(ctx, binding); err != nil {
				return binding, fmt.Errorf("verify ambiguous creation: %w", err)
			}
			if err := m.reconcileWorkspaceVolume(ctx, binding); err != nil {
				return binding, err
			}
			next = "STOPPED"
		default:
			return binding, fmt.Errorf("ambiguous create has no matching Proxmox runtime; refusing clone retry: %w", ErrReconciliationNeeded)
		}
	case "start":
		if state == RuntimeRunning {
			next = "READY"
		} else if state == RuntimeStopped {
			next = "STOPPED"
		} else {
			return binding, fmt.Errorf("ambiguous start runtime is missing: %w", ErrReconciliationNeeded)
		}
	case "stop":
		if state == RuntimeStopped {
			next = "STOPPED"
		} else if state == RuntimeRunning {
			next = "READY"
		} else {
			return binding, fmt.Errorf("ambiguous stop runtime is missing: %w", ErrReconciliationNeeded)
		}
	case "delete":
		if state != RuntimeMissing {
			return binding, fmt.Errorf("ambiguous delete runtime still exists; identity re-verification required: %w", ErrReconciliationNeeded)
		}
		session, err := m.store.GetSession(ctx, sessionID)
		if err != nil {
			return binding, err
		}
		workspaceExists, err := m.runtime.WorkspaceVolumeExists(ctx, session.WorkspaceVolumeID)
		if err != nil || !workspaceExists {
			if err == nil {
				err = errors.New("detached Session workspace volume was not found in Proxmox storage")
			}
			return binding, fmt.Errorf("reconcile ambiguous delete: %w", err)
		}
		next = "DELETED"
	default:
		return binding, fmt.Errorf("unsupported pending operation %q: %w", binding.PendingOperation, ErrReconciliationNeeded)
	}
	if err := m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "UNKNOWN", next, "", m.clock()); err != nil {
		return binding, err
	}
	binding.State = next
	binding.PendingOperation = ""
	return binding, nil
}

func (m *Manager) reconcileWorkspaceVolume(ctx context.Context, binding store.SessionRuntimeBinding) error {
	volumeID, err := m.runtime.WorkspaceVolume(ctx, binding.VMID)
	if err != nil {
		return fmt.Errorf("read ambiguous Session workspace mount: %w", err)
	}
	if !strings.HasPrefix(volumeID, "pool:") {
		return fmt.Errorf("Session workspace mount is not backed by pool storage: %w", ErrReconciliationNeeded)
	}
	session, err := m.store.GetSession(ctx, binding.SessionID)
	if err != nil {
		return err
	}
	if session.WorkspaceVolumeID == "" {
		return m.store.SetSessionWorkspaceVolume(ctx, binding.SessionID, volumeID)
	}
	if session.WorkspaceVolumeID != volumeID {
		return fmt.Errorf("runtime workspace volume differs from Session record: %w", ErrReconciliationNeeded)
	}
	return nil
}

func (m *Manager) markUnknown(ctx context.Context, binding store.SessionRuntimeBinding, from string, cause error) error {
	if err := m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, from, "UNKNOWN", "", m.clock()); err != nil {
		return fmt.Errorf("operation failed (%v); persist UNKNOWN outcome: %w", cause, err)
	}
	return fmt.Errorf("%w: %v", ErrOutcomeUnknown, cause)
}

func (m *Manager) markFailed(ctx context.Context, binding store.SessionRuntimeBinding, from string, cause error) error {
	if err := m.store.UpdateSessionRuntimeBindingState(ctx, binding.ID, from, "FAILED", "", m.clock()); err != nil {
		return fmt.Errorf("operation failed (%v); persist FAILED state: %w", cause, err)
	}
	return cause
}

func sessionHostname(sessionID, epochID, generation string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + epochID + "\x00" + generation))
	return "codex-session-" + hex.EncodeToString(sum[:8])
}
