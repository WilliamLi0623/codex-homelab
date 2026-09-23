package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	CapacityClaimed  = "CLAIMED"
	CapacityCreating = "CREATING"
	CapacityUnknown  = "UNKNOWN"
)

var (
	ErrCapacityClaimConflict = errors.New("capacity claim conflicts with an existing claim")
	ErrCapacityClaimInvalid  = errors.New("capacity claim is invalid")
	ErrCapacityClaimNotFound = errors.New("capacity claim not found")
)

type CapacityConfig struct {
	ReservedVMIDs []int
}

type CapacityClaimRequest struct {
	TaskID     string
	AttemptID  string
	Generation string
	Priority   int
	VMID       int
}

type CapacityClaim struct {
	ID         string
	TaskID     string
	AttemptID  string
	VMID       int
	Generation string
	Priority   int
	State      string
	CreatedAt  string
}

const maxCapacityClaimRetries = 8

const (
	dynamicVMIDMin  = 3000
	dynamicVMIDMax  = 3899
	templateVMIDMin = 3900
	templateVMIDMax = 3902
)

func normalizeReservedVMIDs(values []int) (map[int]struct{}, error) {
	reserved := make(map[int]struct{}, len(values)+1)
	for vmid := templateVMIDMin; vmid <= templateVMIDMax; vmid++ {
		reserved[vmid] = struct{}{}
	}
	for _, vmid := range values {
		if vmid < dynamicVMIDMin || vmid > templateVMIDMax {
			return nil, fmt.Errorf("%w: reserved VMID %d is outside %d-%d", ErrCapacityClaimInvalid, vmid, dynamicVMIDMin, templateVMIDMax)
		}
		reserved[vmid] = struct{}{}
	}
	return reserved, nil
}

func (s *Store) ClaimCapacity(ctx context.Context, request CapacityClaimRequest) (CapacityClaim, bool, error) {
	return s.ClaimCapacityExcluding(ctx, request, nil)
}

// ClaimCapacityExcluding allocates a ledger VMID while skipping targets that
// were observed as occupied outside the Controller ledger.
func (s *Store) ClaimCapacityExcluding(ctx context.Context, request CapacityClaimRequest, excluded map[int]struct{}) (CapacityClaim, bool, error) {
	if err := s.validateCapacityRequest(request); err != nil {
		return CapacityClaim{}, false, err
	}
	var lastErr error
	for attempt := 0; attempt < maxCapacityClaimRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return CapacityClaim{}, false, err
		}
		claim, created, err := s.claimCapacityOnce(ctx, request, excluded)
		if err == nil {
			return claim, created, nil
		}
		if !retryableCapacityClaimError(err, request) {
			return CapacityClaim{}, false, err
		}
		lastErr = err
	}
	if err := ctx.Err(); err != nil {
		return CapacityClaim{}, false, err
	}
	return CapacityClaim{}, false, fmt.Errorf("capacity claim retry limit reached: %w", lastErr)
}

func (s *Store) claimCapacityOnce(ctx context.Context, request CapacityClaimRequest, excluded map[int]struct{}) (CapacityClaim, bool, error) {
	s.capacityMu.Lock()
	defer s.capacityMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CapacityClaim{}, false, fmt.Errorf("begin capacity claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := scanCapacityClaim(tx.QueryRowContext(ctx, `
		SELECT id, task_id, attempt_id, vmid, generation, priority, state, created_at
		FROM capacity_nodes WHERE task_id = ? AND attempt_id = ?`, request.TaskID, request.AttemptID))
	if err == nil {
		if err := s.validateStoredCapacityClaim(existing); err != nil {
			return CapacityClaim{}, false, err
		}
		if existing.Generation != request.Generation || existing.Priority != request.Priority || (request.VMID != 0 && existing.VMID != request.VMID) {
			return CapacityClaim{}, false, ErrCapacityClaimConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CapacityClaim{}, false, fmt.Errorf("read capacity claim: %w", err)
	}

	vmid, err := s.nextCapacityVMID(ctx, tx, request.VMID, excluded)
	if err != nil {
		return CapacityClaim{}, false, err
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	id := newStoreID("capacity-claim")
	_, err = tx.ExecContext(ctx, `
		INSERT INTO capacity_nodes(id, vmid, generation, task_id, attempt_id, priority, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, vmid, request.Generation, request.TaskID, request.AttemptID, request.Priority, CapacityClaimed, createdAt)
	if err != nil {
		return CapacityClaim{}, false, fmt.Errorf("insert capacity claim: %w", err)
	}
	claim := CapacityClaim{ID: id, TaskID: request.TaskID, AttemptID: request.AttemptID, VMID: vmid, Generation: request.Generation, Priority: request.Priority, State: CapacityClaimed, CreatedAt: createdAt}
	if err := tx.Commit(); err != nil {
		return CapacityClaim{}, false, fmt.Errorf("commit capacity claim: %w", err)
	}
	return claim, true, nil
}

func retryableCapacityClaimError(err error, request CapacityClaimRequest) bool {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "database is locked") || strings.Contains(message, "sqlite_busy") {
		return true
	}
	if request.VMID != 0 {
		return false
	}
	return strings.Contains(message, "unique constraint failed: capacity_nodes.vmid") ||
		strings.Contains(message, "unique constraint failed: capacity_nodes_task_attempt")
}

func (s *Store) validateCapacityRequest(request CapacityClaimRequest) error {
	if request.TaskID == "" || request.AttemptID == "" || request.Generation == "" || request.Priority <= 0 {
		return ErrCapacityClaimInvalid
	}
	if request.VMID != 0 && (request.VMID < dynamicVMIDMin || request.VMID > dynamicVMIDMax) {
		return ErrCapacityClaimInvalid
	}
	if _, reserved := s.reservedVMIDs[request.VMID]; request.VMID != 0 && reserved {
		return ErrCapacityClaimInvalid
	}
	return nil
}

func (s *Store) nextCapacityVMID(ctx context.Context, tx *sql.Tx, requested int, excluded map[int]struct{}) (int, error) {
	if requested != 0 {
		var exists int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM capacity_nodes WHERE vmid = ?", requested).Scan(&exists)
		if err == nil {
			return 0, ErrCapacityClaimConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("check requested VMID: %w", err)
		}
		return requested, nil
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT vmid FROM capacity_nodes WHERE vmid BETWEEN %d AND %d", dynamicVMIDMin, dynamicVMIDMax))
	if err != nil {
		return 0, fmt.Errorf("list capacity VMIDs: %w", err)
	}
	defer rows.Close()
	occupied := map[int]struct{}{}
	for rows.Next() {
		var vmid int
		if err := rows.Scan(&vmid); err != nil {
			return 0, fmt.Errorf("scan capacity VMID: %w", err)
		}
		occupied[vmid] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read capacity VMIDs: %w", err)
	}
	for vmid := dynamicVMIDMin; vmid <= dynamicVMIDMax; vmid++ {
		if _, reserved := s.reservedVMIDs[vmid]; reserved {
			continue
		}
		if _, blocked := excluded[vmid]; blocked {
			continue
		}
		if _, used := occupied[vmid]; !used {
			return vmid, nil
		}
	}
	return 0, ErrCapacityClaimConflict
}

// DeleteCapacityClaimIfState removes a claim that has not reached an external
// mutation. The exact state predicate prevents deleting a claim after another
// goroutine has advanced it to CREATING or UNKNOWN.
func (s *Store) DeleteCapacityClaimIfState(ctx context.Context, taskID, attemptID string, vmid int, state string) error {
	if taskID == "" || attemptID == "" || vmid < dynamicVMIDMin || vmid > dynamicVMIDMax || state != CapacityClaimed && state != CapacityCreating {
		return ErrCapacityClaimInvalid
	}
	s.capacityMu.Lock()
	defer s.capacityMu.Unlock()
	result, err := s.db.ExecContext(ctx, "DELETE FROM capacity_nodes WHERE task_id = ? AND attempt_id = ? AND vmid = ? AND state = ?", taskID, attemptID, vmid, state)
	if err != nil {
		return fmt.Errorf("delete capacity claim: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted capacity claim: %w", err)
	}
	if count == 0 {
		return ErrCapacityClaimConflict
	}
	return nil
}

func (s *Store) GetCapacityClaim(ctx context.Context, taskID, attemptID string) (CapacityClaim, error) {
	claim, err := scanCapacityClaim(s.db.QueryRowContext(ctx, `
		SELECT id, task_id, attempt_id, vmid, generation, priority, state, created_at
		FROM capacity_nodes WHERE task_id = ? AND attempt_id = ?`, taskID, attemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return CapacityClaim{}, ErrCapacityClaimNotFound
	}
	if err == nil {
		err = s.validateStoredCapacityClaim(claim)
	}
	return claim, err
}

func (s *Store) UpdateCapacityClaimState(ctx context.Context, taskID, attemptID, state string) error {
	if state != CapacityClaimed && state != CapacityCreating && state != CapacityUnknown {
		return ErrCapacityClaimInvalid
	}
	s.capacityMu.Lock()
	defer s.capacityMu.Unlock()
	if s.capacityStateUpdateHook != nil {
		if err := s.capacityStateUpdateHook(ctx, taskID, attemptID, state); err != nil {
			return err
		}
	}
	result, err := s.db.ExecContext(ctx, "UPDATE capacity_nodes SET state = ? WHERE task_id = ? AND attempt_id = ?", state, taskID, attemptID)
	if err != nil {
		return fmt.Errorf("update capacity claim state: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count capacity claim update: %w", err)
	}
	if count == 0 {
		return ErrCapacityClaimNotFound
	}
	return nil
}

func (s *Store) validateStoredCapacityClaim(claim CapacityClaim) error {
	if claim.ID == "" || claim.TaskID == "" || claim.AttemptID == "" || claim.Generation == "" || claim.Priority <= 0 || claim.VMID < dynamicVMIDMin || claim.VMID > dynamicVMIDMax || claim.CreatedAt == "" {
		return ErrCapacityClaimInvalid
	}
	if claim.State != CapacityClaimed && claim.State != CapacityCreating && claim.State != CapacityUnknown {
		return ErrCapacityClaimInvalid
	}
	if _, reserved := s.reservedVMIDs[claim.VMID]; reserved {
		return ErrCapacityClaimInvalid
	}
	return nil
}

// SetCapacityClaimStateUpdateHook is a narrow failure-injection seam for the
// adapter contract tests. Production callers should leave it unset.
func (s *Store) SetCapacityClaimStateUpdateHook(hook func(context.Context, string, string, string) error) {
	s.capacityMu.Lock()
	defer s.capacityMu.Unlock()
	s.capacityStateUpdateHook = hook
}

type capacityClaimScanner interface{ Scan(...any) error }

func scanCapacityClaim(scanner capacityClaimScanner) (CapacityClaim, error) {
	var claim CapacityClaim
	err := scanner.Scan(&claim.ID, &claim.TaskID, &claim.AttemptID, &claim.VMID, &claim.Generation, &claim.Priority, &claim.State, &claim.CreatedAt)
	return claim, err
}
