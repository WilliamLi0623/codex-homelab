package codexrouting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileRoutingStateStore stores only mode, observation time, and generation.
// It is intended for one coordinator process; the service manager must ensure
// that only one writer owns a given path.
type FileRoutingStateStore struct {
	Path string
	mu   sync.Mutex
}

type routingStateFile struct {
	Mode       Mode      `json:"mode"`
	ObservedAt time.Time `json:"observed_at"`
	Generation int64     `json:"generation"`
}

func (s *FileRoutingStateStore) Load(ctx context.Context) (RoutingState, bool, error) {
	if err := ctx.Err(); err != nil {
		return RoutingState{}, false, err
	}
	if s == nil || s.Path == "" {
		return RoutingState{}, false, errors.New("routing-state file path is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return RoutingState{}, false, nil
	}
	if err != nil {
		return RoutingState{}, false, fmt.Errorf("open routing-state file: %w", err)
	}
	defer file.Close()
	var data routingStateFile
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return RoutingState{}, false, fmt.Errorf("decode routing-state file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return RoutingState{}, false, errors.New("decode routing-state file: trailing data or file exceeds 4096 bytes")
	}
	state := RoutingState{Mode: data.Mode, ObservedAt: data.ObservedAt.UTC(), Generation: data.Generation}
	if err := validateRoutingState(state); err != nil {
		return RoutingState{}, false, fmt.Errorf("validate routing-state file: %w", err)
	}
	return state, true, nil
}

func (s *FileRoutingStateStore) Save(ctx context.Context, state RoutingState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.Path == "" {
		return errors.New("routing-state file path is required")
	}
	if err := validateRoutingState(state); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	if ok && (state.Generation < current.Generation || state.ObservedAt.Before(current.ObservedAt) || (state.Generation == current.Generation && state != current)) {
		return errors.New("routing-state generation is stale or conflicts with persisted state")
	}
	if ok && state == current {
		return nil
	}
	path := filepath.Clean(s.Path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create routing-state directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".routing-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create routing-state temporary file: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure routing-state temporary file: %w", err)
	}
	data, err := json.Marshal(routingStateFile{Mode: state.Mode, ObservedAt: state.ObservedAt.UTC(), Generation: state.Generation})
	if err != nil {
		_ = temp.Close()
		return fmt.Errorf("encode routing state: %w", err)
	}
	data = append(data, '\n')
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write routing-state temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync routing-state temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close routing-state temporary file: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("publish routing-state file: %w", err)
	}
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}

func (s *FileRoutingStateStore) loadUnlocked() (RoutingState, bool, error) {
	file, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return RoutingState{}, false, nil
	}
	if err != nil {
		return RoutingState{}, false, fmt.Errorf("open routing-state file: %w", err)
	}
	defer file.Close()
	var data routingStateFile
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return RoutingState{}, false, fmt.Errorf("decode routing-state file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return RoutingState{}, false, errors.New("decode routing-state file: trailing data or file exceeds 4096 bytes")
	}
	state := RoutingState{Mode: data.Mode, ObservedAt: data.ObservedAt.UTC(), Generation: data.Generation}
	if err := validateRoutingState(state); err != nil {
		return RoutingState{}, false, fmt.Errorf("validate routing-state file: %w", err)
	}
	return state, true, nil
}
