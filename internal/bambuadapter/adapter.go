// Package bambuadapter treats Bambu Studio as an external system behind a
// versioned adapter (design.md §15). All filesystem operations take an
// explicit root directory — never a hardcoded path — so tests run against a
// throwaway temp dir instead of a real Bambu Studio installation.
//
// Publish requires Bambu Studio to be closed (decisions.md #4: no evidence
// of safe live coexistence or hot-reload — see
// openspec/changes/init-profile-manager/findings.md). The running-check is
// pluggable via StudioRunningFunc so it can be swapped or stubbed; the
// default implementation shells out to `pgrep`, which is a best-effort,
// macOS/Linux-only signal, not a guarantee.
package bambuadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/normalize"
	"github.com/syscod3/bambu-profile-manager/internal/parser"
)

var (
	ErrStudioRunning = errors.New("bambuadapter: Bambu Studio appears to be running; publish requires it closed (decisions.md #4)")
	ErrInvalidCandidate = errors.New("bambuadapter: invalid candidate profile")
)

// DiscoveredProfile is one profile found under the user profile directory.
type DiscoveredProfile struct {
	Name string
	Path string // absolute path to the .json file
}

// Observation is what Publish's caller later reads back from disk to check
// whether Studio (or its cloud sync) touched the published file.
type Observation struct {
	Path      string
	Bytes     []byte
	ModTime   time.Time
	Fields    map[string]any
}

// StudioRunningFunc reports whether Bambu Studio looks like it's running.
type StudioRunningFunc func() (bool, error)

// PgrepStudioRunning is the default StudioRunningFunc: best-effort, shells
// out to pgrep for a process named "BambuStudio". Any pgrep error (e.g. not
// on macOS/Linux, pgrep missing) is treated as "unknown", not "not running"
// — callers should decide how to handle that rather than silently
// proceeding.
func PgrepStudioRunning() (bool, error) {
	cmd := exec.Command("pgrep", "-x", "BambuStudio")
	err := cmd.Run()
	if err == nil {
		return true, nil // pgrep found a match
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil // pgrep's documented "no process matched"
	}
	return false, fmt.Errorf("bambuadapter: pgrep: %w", err)
}

// LocalAdapter is the filesystem-based Bambu Studio adapter (design.md §15
// "Initial strategy").
type LocalAdapter struct {
	// Dir is the Bambu Studio user profile directory to operate on, e.g.
	// "<user>/<account-id>/filament". Callers point this at a temp dir in
	// tests and the real Bambu Studio directory in production.
	Dir string
	// IsStudioRunning is called before Publish. Defaults to
	// PgrepStudioRunning if nil.
	IsStudioRunning StudioRunningFunc
}

func (a *LocalAdapter) checker() StudioRunningFunc {
	if a.IsStudioRunning != nil {
		return a.IsStudioRunning
	}
	return PgrepStudioRunning
}

// Discover lists every *.json profile directly under Dir.
func (a *LocalAdapter) Discover(ctx context.Context) ([]DiscoveredProfile, error) {
	entries, err := os.ReadDir(a.Dir)
	if err != nil {
		return nil, fmt.Errorf("bambuadapter: discover: read %s: %w", a.Dir, err)
	}
	var out []DiscoveredProfile
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(a.Dir, e.Name())
		p, err := parser.Load(path)
		if err != nil {
			continue // skip unparseable files rather than fail the whole scan
		}
		out = append(out, DiscoveredProfile{Name: p.Name, Path: path})
	}
	return out, nil
}

// Validate checks a candidate profile is well-formed enough to publish.
// This is intentionally shallow — deep Bambu-format validation needs more
// reverse-engineering than confirmed so far (findings.md); it only checks
// what's confirmed: a name, and internal consistency with its own Fields.
func (a *LocalAdapter) Validate(ctx context.Context, candidate *domain.RawProfile) error {
	if candidate.Name == "" {
		return fmt.Errorf("%w: empty name", ErrInvalidCandidate)
	}
	if name, _ := candidate.Fields["name"].(string); name != candidate.Name {
		return fmt.Errorf("%w: Fields[\"name\"]=%q does not match Name=%q", ErrInvalidCandidate, name, candidate.Name)
	}
	return nil
}

// Stage writes candidate to a staging subdirectory (Dir/.staging/), never
// touching the live profile directory, and returns the staged path.
func (a *LocalAdapter) Stage(ctx context.Context, candidate *domain.RawProfile) (string, error) {
	if err := a.Validate(ctx, candidate); err != nil {
		return "", err
	}
	stagingDir := filepath.Join(a.Dir, ".staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", fmt.Errorf("bambuadapter: stage: mkdir: %w", err)
	}
	b, err := json.MarshalIndent(candidate.Fields, "", "  ")
	if err != nil {
		return "", fmt.Errorf("bambuadapter: stage: marshal: %w", err)
	}
	path := filepath.Join(stagingDir, candidate.Name+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", fmt.Errorf("bambuadapter: stage: write: %w", err)
	}
	return path, nil
}

// Publish atomically moves a staged file into the live profile directory,
// backing up any file it would collide with (design.md §15 steps 2-4).
// Refuses if Bambu Studio looks like it's running (decisions.md #4).
func (a *LocalAdapter) Publish(ctx context.Context, stagedPath string) (string, error) {
	running, err := a.checker()()
	if err != nil {
		return "", fmt.Errorf("bambuadapter: publish: check Studio running: %w", err)
	}
	if running {
		return "", ErrStudioRunning
	}

	name := filepath.Base(stagedPath)
	target := filepath.Join(a.Dir, name)

	if _, err := os.Stat(target); err == nil {
		if err := a.backup(target); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("bambuadapter: publish: stat %s: %w", target, err)
	}

	b, err := os.ReadFile(stagedPath)
	if err != nil {
		return "", fmt.Errorf("bambuadapter: publish: read staged file: %w", err)
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return "", fmt.Errorf("bambuadapter: publish: write temp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil { // atomic on the same filesystem
		return "", fmt.Errorf("bambuadapter: publish: rename into place: %w", err)
	}
	return target, nil
}

func (a *LocalAdapter) backup(target string) error {
	backupDir := filepath.Join(a.Dir, ".backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return fmt.Errorf("bambuadapter: backup: mkdir: %w", err)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("bambuadapter: backup: read %s: %w", target, err)
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	dest := filepath.Join(backupDir, stamp+"-"+filepath.Base(target))
	if err := os.WriteFile(dest, b, 0o644); err != nil {
		return fmt.Errorf("bambuadapter: backup: write %s: %w", dest, err)
	}
	return nil
}

// Observe re-reads a published profile from disk, for comparison against
// what was expected (design.md §15 step 8).
func (a *LocalAdapter) Observe(ctx context.Context, publishedPath string) (*Observation, error) {
	info, err := os.Stat(publishedPath)
	if err != nil {
		return nil, fmt.Errorf("bambuadapter: observe: stat: %w", err)
	}
	b, err := os.ReadFile(publishedPath)
	if err != nil {
		return nil, fmt.Errorf("bambuadapter: observe: read: %w", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, fmt.Errorf("bambuadapter: observe: decode: %w", err)
	}
	return &Observation{Path: publishedPath, Bytes: b, ModTime: info.ModTime(), Fields: fields}, nil
}

// VerificationResult is design.md §7's VerificationResult.
type VerificationResult struct {
	ExpectedHash string
	ObservedHash string
	Match        bool
}

// Verify compares an Observation's semantic hash (after normalization, not
// byte-for-byte) against expectedHash. It does not resolve the observed
// profile's inheritance chain itself — callers pass in the profile's own
// Fields already merged with whatever ancestor chain applies, since that
// requires a resolver.Set this package doesn't own.
func Verify(observedFields map[string]any, expectedHash string) (VerificationResult, error) {
	observedHash, err := normalize.Hash(observedFields)
	if err != nil {
		return VerificationResult{}, fmt.Errorf("bambuadapter: verify: hash: %w", err)
	}
	return VerificationResult{ExpectedHash: expectedHash, ObservedHash: observedHash, Match: observedHash == expectedHash}, nil
}
