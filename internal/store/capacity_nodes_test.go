package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func openCapacityTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func capacityRequest(task, attempt, generation string, priority int) CapacityClaimRequest {
	return CapacityClaimRequest{TaskID: task, AttemptID: attempt, Generation: generation, Priority: priority}
}

func TestClaimCapacitySkipsReservedVMIDAndChoosesLowestFree(t *testing.T) {
	s := openCapacityTestStore(t)
	claim, created, err := s.ClaimCapacity(context.Background(), capacityRequest("task-1", "attempt-1", "gen-1", 10))
	if err != nil || !created {
		t.Fatalf("ClaimCapacity() = (%+v, %t, %v)", claim, created, err)
	}
	if claim.VMID != 3000 {
		t.Fatalf("VMID = %d, want lowest free 3000", claim.VMID)
	}
	claim, _, err = s.ClaimCapacity(context.Background(), capacityRequest("task-2", "attempt-1", "gen-2", 10))
	if err != nil {
		t.Fatal(err)
	}
	if claim.VMID != 3001 {
		t.Fatalf("second VMID = %d, want 3001", claim.VMID)
	}
	if claim.VMID == 3005 {
		t.Fatal("reserved template VMID 3005 was allocated")
	}
}

func TestClaimCapacityExcludesObservedExternalVMID(t *testing.T) {
	s := openCapacityTestStore(t)
	claim, created, err := s.ClaimCapacityExcluding(context.Background(), capacityRequest("task-1", "attempt-1", "gen-1", 10), map[int]struct{}{3000: {}})
	if err != nil || !created {
		t.Fatalf("ClaimCapacityExcluding() = (%+v, %t, %v)", claim, created, err)
	}
	if claim.VMID != 3001 {
		t.Fatalf("VMID = %d, want externally occupied 3000 to be skipped", claim.VMID)
	}
}

func TestClaimCapacityIsIdempotentAcrossRestartAndPreservesGeneration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capacity.sqlite")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := s.ClaimCapacity(context.Background(), capacityRequest("task-1", "attempt-1", "caller-generation", 10))
	if err != nil || !created {
		t.Fatalf("first claim = (%+v, %t, %v)", first, created, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	second, created, err := s.ClaimCapacity(context.Background(), capacityRequest("task-1", "attempt-1", "caller-generation", 10))
	if err != nil || created {
		t.Fatalf("repeat claim = (%+v, %t, %v)", second, created, err)
	}
	if second.VMID != first.VMID || second.Generation != first.Generation {
		t.Fatalf("repeat claim = %+v, first = %+v", second, first)
	}
}

func TestClaimCapacityRejectsMismatchedExistingAttempt(t *testing.T) {
	s := openCapacityTestStore(t)
	_, _, err := s.ClaimCapacity(context.Background(), capacityRequest("task-1", "attempt-1", "gen-1", 10))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.ClaimCapacity(context.Background(), capacityRequest("task-1", "attempt-1", "gen-2", 10))
	if !errors.Is(err, ErrCapacityClaimConflict) {
		t.Fatalf("mismatch error = %v, want ErrCapacityClaimConflict", err)
	}
}

func TestClaimCapacityDifferentAttemptsNeverShareVMID(t *testing.T) {
	s := openCapacityTestStore(t)
	const n = 20
	claims := make(chan CapacityClaim, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claim, _, err := s.ClaimCapacity(context.Background(), capacityRequest("task", "attempt-"+itoa(i), "gen-"+itoa(i), 10))
			if err != nil {
				errs <- err
				return
			}
			claims <- claim
		}(i)
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for claim := range claims {
		if seen[claim.VMID] {
			t.Fatalf("duplicate VMID %d", claim.VMID)
		}
		seen[claim.VMID] = true
	}
	if len(seen) != n {
		t.Fatalf("unique claims = %d, want %d", len(seen), n)
	}
}

func TestClaimCapacitySameAttemptConcurrentIsIdempotent(t *testing.T) {
	s := openCapacityTestStore(t)
	const n = 12
	claims := make(chan CapacityClaim, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, _, err := s.ClaimCapacity(context.Background(), capacityRequest("task", "attempt", "gen", 10))
			if err != nil {
				errs <- err
				return
			}
			claims <- claim
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first CapacityClaim
	for claim := range claims {
		if first.VMID == 0 {
			first = claim
			continue
		}
		if claim.VMID != first.VMID || claim.Generation != first.Generation {
			t.Fatalf("claims differ: first=%+v current=%+v", first, claim)
		}
	}
}

func TestClaimCapacityFailsClosedForInvalidInput(t *testing.T) {
	s := openCapacityTestStore(t)
	for _, request := range []CapacityClaimRequest{
		{TaskID: "task", AttemptID: "attempt", Generation: "gen", Priority: 0},
		{TaskID: "task", AttemptID: "attempt", Generation: "gen", Priority: 1, VMID: 2999},
		{TaskID: "task", AttemptID: "attempt", Generation: "gen", Priority: 1, VMID: 3005},
	} {
		if _, _, err := s.ClaimCapacity(context.Background(), request); !errors.Is(err, ErrCapacityClaimInvalid) {
			t.Fatalf("request %+v error = %v, want ErrCapacityClaimInvalid", request, err)
		}
	}
}

func TestCustomReservedVMIDsAlwaysIncludeTemplate(t *testing.T) {
	s, err := OpenWithCapacityConfig(filepath.Join(t.TempDir(), "capacity.sqlite"), CapacityConfig{ReservedVMIDs: []int{3000}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	claim, _, err := s.ClaimCapacity(context.Background(), capacityRequest("task", "attempt", "gen", 1))
	if err != nil {
		t.Fatal(err)
	}
	if claim.VMID != 3001 {
		t.Fatalf("VMID = %d, want 3001 with 3000 and template 3005 reserved", claim.VMID)
	}
	for i := 0; i < 3; i++ {
		claim, _, err = s.ClaimCapacity(context.Background(), capacityRequest("task-"+itoa(i), "attempt", "gen-"+itoa(i), 1))
		if err != nil {
			t.Fatal(err)
		}
		if claim.VMID == 3005 {
			t.Fatal("custom reserved set allowed template VMID 3005")
		}
	}
}

func TestStoredCapacityClaimValidationIsFailClosed(t *testing.T) {
	s := openCapacityTestStore(t)
	invalid := []struct {
		name                                            string
		id, task, attempt, generation, state, createdAt string
		vmid, priority                                  int
	}{
		{"id", "", "task", "attempt-id", "gen", CapacityClaimed, "now", 3000, 1},
		{"state", "claim-2", "task-2", "attempt-2", "gen", "READY", "now", 3001, 1},
		{"created-at", "claim-3", "task-3", "attempt-3", "gen", CapacityClaimed, "", 3002, 1},
		{"task", "claim-4", "", "attempt-4", "gen", CapacityClaimed, "now", 3003, 1},
		{"attempt", "claim-5", "task-5", "", "gen", CapacityClaimed, "now", 3004, 1},
		{"generation", "claim-6", "task-6", "attempt-6", "", CapacityClaimed, "now", 3006, 1},
		{"priority", "claim-7", "task-7", "attempt-7", "gen", CapacityClaimed, "now", 3007, 0},
		{"vmid", "claim-8", "task-8", "attempt-8", "gen", CapacityClaimed, "now", 2999, 1},
	}
	for _, tc := range invalid {
		_, err := s.db.Exec(`INSERT INTO capacity_nodes(id, vmid, generation, task_id, attempt_id, priority, state, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, tc.id, tc.vmid, tc.generation, tc.task, tc.attempt, tc.priority, tc.state, tc.createdAt)
		if err != nil {
			t.Fatalf("insert %s: %v", tc.name, err)
		}
		if _, err := s.GetCapacityClaim(context.Background(), tc.task, tc.attempt); !errors.Is(err, ErrCapacityClaimInvalid) {
			t.Fatalf("GetCapacityClaim(%s) error = %v, want ErrCapacityClaimInvalid", tc.name, err)
		}
	}
}

func TestFreshAndOldSchemaMigrationsAreCompatible(t *testing.T) {
	freshPath := filepath.Join(t.TempDir(), "fresh.sqlite")
	fresh, err := Open(freshPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(t.TempDir(), "old.sqlite")
	old, err := sql.Open("sqlite", oldPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY); INSERT INTO schema_migrations(version) VALUES (1); CREATE TABLE capacity_nodes (id TEXT PRIMARY KEY, vmid INTEGER NOT NULL UNIQUE, generation TEXT NOT NULL, task_id TEXT, state TEXT NOT NULL, created_at TEXT NOT NULL); INSERT INTO capacity_nodes(id, vmid, generation, task_id, state, created_at) VALUES ('legacy-id', 3010, 'legacy-gen', NULL, 'CREATING', 'legacy-time')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	var got string
	if err := migrated.db.QueryRow("SELECT generation FROM capacity_nodes WHERE id = 'legacy-id'").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "legacy-gen" {
		t.Fatalf("legacy generation = %q, want legacy-gen", got)
	}
	var migrations int
	if err := migrated.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if migrations != len(schemaMigrations) {
		t.Fatalf("migrations = %d, want %d", migrations, len(schemaMigrations))
	}
}

func TestSQLiteCapacityUsesBusyTimeout(t *testing.T) {
	s := openCapacityTestStore(t)
	var timeout int
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout < 1000 {
		t.Fatalf("busy_timeout = %d, want at least 1000ms", timeout)
	}
}

func TestClaimCapacityDifferentIndependentStoresRetryUniqueVMIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capacity.sqlite")
	left, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Open(path)
	if err != nil {
		_ = left.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	type result struct {
		claim CapacityClaim
		err   error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i, request := range []CapacityClaimRequest{
		capacityRequest("task-left", "attempt", "gen-left", 1),
		capacityRequest("task-right", "attempt", "gen-right", 1),
	} {
		wg.Add(1)
		go func(database *Store, request CapacityClaimRequest) {
			defer wg.Done()
			claim, _, err := database.ClaimCapacity(context.Background(), request)
			results <- result{claim: claim, err: err}
		}([]*Store{left, right}[i], request)
	}
	wg.Wait()
	close(results)
	var claims []CapacityClaim
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		claims = append(claims, result.claim)
	}
	if len(claims) != 2 || claims[0].VMID == claims[1].VMID {
		t.Fatalf("independent-store claims = %+v, want distinct VMIDs", claims)
	}
}

func TestClaimCapacitySameAttemptIndependentStoresReturnsWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capacity.sqlite")
	left, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Open(path)
	if err != nil {
		_ = left.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	results := make(chan CapacityClaim, 2)
	errs := make(chan error, 2)
	request := capacityRequest("same-task", "same-attempt", "same-generation", 1)
	var wg sync.WaitGroup
	for _, database := range []*Store{left, right} {
		wg.Add(1)
		go func(database *Store) {
			defer wg.Done()
			claim, _, err := database.ClaimCapacity(context.Background(), request)
			if err != nil {
				errs <- err
				return
			}
			results <- claim
		}(database)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first CapacityClaim
	for claim := range results {
		if first.VMID == 0 {
			first = claim
			continue
		}
		if claim.VMID != first.VMID || claim.ID != first.ID {
			t.Fatalf("winner claims differ: first=%+v current=%+v", first, claim)
		}
	}
}

func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(digits[n%10]) + result
		n /= 10
	}
	return result
}
