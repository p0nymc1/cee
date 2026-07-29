// Package filestore is a durable execution.Store: suspended workflows are
// written to a directory as JSON, one file per resume pointer, so a run
// parked for a human survives a restart. The in-memory default loses parked
// runs on exit, which is fine for tests and unusable for an approval queue
// that waits hours or days.
//
// It lives outside execution for the same reason sandbox does: the engine
// depends on the Store interface, never on a backend, so file I/O stays out
// of the engine's core. JSON rather than gob keeps the module dependency
// free and the parked state readable by an operator with `cat` -- at the
// cost of the type coercion documented on Save.
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

// claimExt marks a file as claimed-but-not-yet-released. checkPointer rejects
// ".", so a claim name can never be mistaken for a live pointer.
const claimExt = ".claimed"

// Store persists suspended workflows under a directory. Safe for concurrent
// use within a process; across processes, writes are atomic (write to a
// temporary file, then rename) so a reader never observes a half-written
// state, and a crash mid-save leaves the previous file intact.
type Store struct {
	mu  sync.Mutex
	dir string
}

// New creates the directory if needed and returns a Store rooted there.
// Parked state carries whatever the workflow had in context -- amounts,
// names, hostnames -- so the directory is created owner-only.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("filestore: a directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("filestore: could not create %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Save writes a suspended run.
//
// The context is serialised as JSON, which does not round-trip every Go
// type: json.Unmarshal decodes all numbers into float64, so an int, int64
// or float32 placed in context before a suspension comes back as a float64
// after Resume. The standard actions are unaffected -- stdlib compares
// numerically through toFloat -- but a Go hook that type-asserts ctx["n"].(int)
// after a resume will panic. Assert on float64, or keep non-JSON types out
// of the context of a workflow that suspends.
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

	// Write-then-rename: rename is atomic on POSIX, so a concurrent reader
	// sees either the old file or the new one, never a partial write.
	temp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("filestore: could not create a temporary file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName) // no-op once the rename succeeds

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("filestore: could not write state %q: %w", state.Pointer, err)
	}
	// Sync before rename: the point of this Store is surviving a crash, and
	// a rename that lands before the contents does not survive one.
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

// Load reads a suspended run back.
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

// Delete consumes a pointer. Deleting one that is already gone is an error,
// because the engine deletes a pointer to guarantee an approval cannot be
// replayed -- a silent success there would hide a double resume.
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

// Consume atomically claims a parked run.
//
// The claim is a rename, not a read-then-delete. POSIX rename unlinks the
// source as one indivisible operation, so when several processes race for
// the same pointer exactly one rename succeeds and every other gets ENOENT
// -- which is the guarantee that a human's decision cannot be applied twice
// by two replicas that both accepted the same resume link. A read-then-delete
// would let both read before either deleted.
//
// The claim is held, not discarded: Release removes it once the resumed run
// has finished. So a claim still present afterwards is the trace of a process
// that took a run and died before completing it, which Orphaned reports.
// Pending skips claims, so a claimed run never looks parked again.
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
	// checkPointer rejects ".", so a claim name can never collide with, or
	// be mistaken for, a live pointer.
	claim := s.path(pointer) + "." + suffix + claimExt

	if err := os.Rename(s.path(pointer), claim); err != nil {
		if os.IsNotExist(err) {
			return execution.State{}, fmt.Errorf("no suspended workflow for pointer %q", pointer)
		}
		return execution.State{}, fmt.Errorf("filestore: could not claim state %q: %w", pointer, err)
	}
	// The claim is deliberately NOT removed here. It is removed by Release,
	// once the resumed run has actually finished. A claim still sitting here
	// afterwards means a process took this run and died before finishing it,
	// which Orphaned surfaces.
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

// Release discards the claim taken by Consume, once the resumed run has
// finished. Releasing a pointer that was never claimed is not an error: the
// engine releases on every path out of a resume, including ones where the
// claim never happened, and failing there would turn tidy-up into noise.
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

// Orphan is a run that was claimed and never released -- the trace a process
// leaves when it dies mid-resume.
type Orphan struct {
	Pointer   string
	ClaimedAt time.Time
	State     execution.State
}

// Orphaned lists claims older than minAge that were never released.
//
// It reports; it does not requeue. Putting a claimed run back automatically
// would re-run a workflow whose side effects may already have landed --
// the money moved, the host was isolated -- and the engine has no idempotency
// to make that safe. So this hands an operator the facts (which run, parked
// for what reason, claimed when) and lets them decide. Use a minAge
// comfortably longer than the longest workflow, or in-flight runs will be
// reported as orphans.
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
			_ = json.Unmarshal(data, &state) // a corrupt claim still counts as an orphan
		}
		orphans = append(orphans, Orphan{
			Pointer:   pointerFromClaim(filepath.Base(claim)),
			ClaimedAt: info.ModTime(),
			State:     state,
		})
	}
	return orphans, nil
}

// pointerFromClaim recovers the original pointer from "<pointer>.<rand>.claimed".
func pointerFromClaim(name string) string {
	trimmed := strings.TrimSuffix(name, claimExt)
	if i := strings.LastIndex(trimmed, "."); i >= 0 {
		return trimmed[:i]
	}
	return trimmed
}

// claimSuffix makes a claim filename unique so two racing claims of
// different pointers, or a retry after a crash, cannot collide.
func claimSuffix() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("filestore: could not generate a claim suffix: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Pending lists the parked runs, for operator tooling: what is waiting, and
// on what. Unreadable or corrupt files are skipped rather than failing the
// whole listing, so one bad file cannot hide every other pending approval.
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
			continue // temporary files and anything not a pointer
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

// checkPointer rejects anything that could escape the store's directory.
// A pointer reaches Load and Delete straight from a caller -- an HTTP
// handler, a CLI argument -- so it is untrusted input being used as a
// filename. Restricting it to an unreserved charset means no separator, no
// "..", no NUL, and no absolute path can get through, without pinning the
// check to the exact token format the engine happens to mint today.
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
