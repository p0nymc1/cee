package filestore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/p0nymc1/cee/execution"
)

const claimExt = ".claimed"

type Store struct {
	mu  sync.Mutex
	dir string
}

func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("filestore: a directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("filestore: could not create %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Save(state execution.State) error {
	if err := checkPointer(state.Pointer); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("filestore: could not encode state %q: %w", state.Pointer, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	temp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("filestore: could not create a temporary file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("filestore: could not write state %q: %w", state.Pointer, err)
	}

	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("filestore: could not flush state %q: %w", state.Pointer, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("filestore: could not close state %q: %w", state.Pointer, err)
	}
	if err := os.Chmod(tempName, 0o600); err != nil {
		return fmt.Errorf("filestore: could not set permissions on state %q: %w", state.Pointer, err)
	}
	if err := os.Rename(tempName, s.path(state.Pointer)); err != nil {
		return fmt.Errorf("filestore: could not commit state %q: %w", state.Pointer, err)
	}
	return nil
}

func (s *Store) Load(pointer string) (execution.State, error) {
	if err := checkPointer(pointer); err != nil {
		return execution.State{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path(pointer))
	if err != nil {
		if os.IsNotExist(err) {
			return execution.State{}, fmt.Errorf("no suspended workflow for pointer %q", pointer)
		}
		return execution.State{}, fmt.Errorf("filestore: could not read state %q: %w", pointer, err)
	}
	var state execution.State
	if err := json.Unmarshal(data, &state); err != nil {
		return execution.State{}, fmt.Errorf("filestore: state %q is corrupt: %w", pointer, err)
	}
	return state, nil
}

func (s *Store) Delete(pointer string) error {
	if err := checkPointer(pointer); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(s.path(pointer)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no suspended workflow for pointer %q", pointer)
		}
		return fmt.Errorf("filestore: could not delete state %q: %w", pointer, err)
	}
	return nil
}

func (s *Store) Consume(pointer string) (execution.State, error) {
	if err := checkPointer(pointer); err != nil {
		return execution.State{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	suffix, err := claimSuffix()
	if err != nil {
		return execution.State{}, err
	}

	claim := s.path(pointer) + "." + suffix + claimExt

	if err := os.Rename(s.path(pointer), claim); err != nil {
		if os.IsNotExist(err) {
			return execution.State{}, fmt.Errorf("no suspended workflow for pointer %q", pointer)
		}
		return execution.State{}, fmt.Errorf("filestore: could not claim state %q: %w", pointer, err)
	}

	data, err := os.ReadFile(claim)
	if err != nil {
		return execution.State{}, fmt.Errorf("filestore: could not read claimed state %q: %w", pointer, err)
	}
	var state execution.State
	if err := json.Unmarshal(data, &state); err != nil {
		return execution.State{}, fmt.Errorf("filestore: state %q is corrupt: %w", pointer, err)
	}
	return state, nil
}

func (s *Store) Release(pointer string) error {
	if err := checkPointer(pointer); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	claims, err := filepath.Glob(s.path(pointer) + ".*" + claimExt)
	if err != nil {
		return fmt.Errorf("filestore: could not look up claims for %q: %w", pointer, err)
	}
	for _, claim := range claims {
		if err := os.Remove(claim); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("filestore: could not release claim for %q: %w", pointer, err)
		}
	}
	return nil
}

type Orphan struct {
	Pointer   string
	ClaimedAt time.Time
	State     execution.State
}

func (s *Store) Orphaned(minAge time.Duration) ([]Orphan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	claims, err := filepath.Glob(filepath.Join(s.dir, "*"+claimExt))
	if err != nil {
		return nil, fmt.Errorf("filestore: could not list claims: %w", err)
	}

	cutoff := time.Now().Add(-minAge)
	var orphans []Orphan
	for _, claim := range claims {
		info, err := os.Stat(claim)
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		var state execution.State
		if data, err := os.ReadFile(claim); err == nil {
			_ = json.Unmarshal(data, &state)
		}
		orphans = append(orphans, Orphan{
			Pointer:   pointerFromClaim(filepath.Base(claim)),
			ClaimedAt: info.ModTime(),
			State:     state,
		})
	}
	return orphans, nil
}

func pointerFromClaim(name string) string {
	trimmed := strings.TrimSuffix(name, claimExt)
	if i := strings.LastIndex(trimmed, "."); i >= 0 {
		return trimmed[:i]
	}
	return trimmed
}

func claimSuffix() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("filestore: could not generate a claim suffix: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (s *Store) Pending() ([]execution.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("filestore: could not list %s: %w", s.dir, err)
	}

	var states []execution.State
	for _, entry := range entries {
		if entry.IsDir() || checkPointer(entry.Name()) != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		var state execution.State
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		states = append(states, state)
	}
	return states, nil
}

func (s *Store) path(pointer string) string {
	return filepath.Join(s.dir, pointer)
}

func checkPointer(pointer string) error {
	if pointer == "" {
		return fmt.Errorf("filestore: empty resume pointer")
	}
	if len(pointer) > 128 {
		return fmt.Errorf("filestore: resume pointer is too long")
	}
	for _, r := range pointer {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf("filestore: resume pointer %q contains an unsupported character", pointer)
		}
	}
	return nil
}
