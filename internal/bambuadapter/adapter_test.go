package bambuadapter_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/normalize"
)

// All tests use t.TempDir() — never the real Bambu Studio directory, even
// though this package would point at it in production.

func notRunning() (bool, error) { return false, nil }
func running() (bool, error)    { return true, nil }

func candidate(name string) *domain.RawProfile {
	return &domain.RawProfile{
		Name: name,
		Fields: map[string]any{
			"name":          name,
			"filament_type": "ABS",
		},
	}
}

func TestPublishRefusesWhileStudioRunning(t *testing.T) {
	dir := t.TempDir()
	a := &bambuadapter.LocalAdapter{Dir: dir, IsStudioRunning: running}
	ctx := context.Background()

	staged, err := a.Stage(ctx, candidate("Test Filament"))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	_, err = a.Publish(ctx, staged)
	if !errors.Is(err, bambuadapter.ErrStudioRunning) {
		t.Fatalf("Publish while running = %v, want ErrStudioRunning", err)
	}
}

func TestStageValidatePublishObserveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := &bambuadapter.LocalAdapter{Dir: dir, IsStudioRunning: notRunning}
	ctx := context.Background()

	c := candidate("Test Filament")
	staged, err := a.Stage(ctx, c)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("staged file missing: %v", err)
	}
	// Staging must never touch the live directory directly.
	if filepath.Dir(staged) == dir {
		t.Fatalf("Stage wrote directly into the live dir: %s", staged)
	}

	published, err := a.Publish(ctx, staged)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if filepath.Dir(published) != dir {
		t.Fatalf("Publish wrote to %s, want directly under %s", published, dir)
	}

	obs, err := a.Observe(ctx, published)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Fields["name"] != "Test Filament" {
		t.Fatalf("observed fields = %+v, want name=Test Filament", obs.Fields)
	}

	wantHash, err := normalize.Hash(c.Fields)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	result, err := bambuadapter.Verify(obs.Fields, wantHash)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !result.Match {
		t.Fatalf("Verify.Match = false, expected=%s observed=%s", result.ExpectedHash, result.ObservedHash)
	}
}

func TestPublishBacksUpCollidingFile(t *testing.T) {
	dir := t.TempDir()
	a := &bambuadapter.LocalAdapter{Dir: dir, IsStudioRunning: notRunning}
	ctx := context.Background()

	// Publish an initial version.
	first, err := a.Stage(ctx, candidate("Collide"))
	if err != nil {
		t.Fatalf("Stage 1: %v", err)
	}
	if _, err := a.Publish(ctx, first); err != nil {
		t.Fatalf("Publish 1: %v", err)
	}

	// Publish a changed version with the same name — must back up the old one.
	c2 := candidate("Collide")
	c2.Fields["filament_type"] = "PLA"
	second, err := a.Stage(ctx, c2)
	if err != nil {
		t.Fatalf("Stage 2: %v", err)
	}
	if _, err := a.Publish(ctx, second); err != nil {
		t.Fatalf("Publish 2: %v", err)
	}

	backups, err := filepath.Glob(filepath.Join(dir, ".backups", "*-Collide.json"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("found %d backup(s), want 1: %v", len(backups), backups)
	}

	obs, err := a.Observe(ctx, filepath.Join(dir, "Collide.json"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Fields["filament_type"] != "PLA" {
		t.Fatalf("live file has filament_type=%v, want PLA (should be the newest publish)", obs.Fields["filament_type"])
	}
}

func TestDiscoverSkipsStagingAndBackupDirs(t *testing.T) {
	dir := t.TempDir()
	a := &bambuadapter.LocalAdapter{Dir: dir, IsStudioRunning: notRunning}
	ctx := context.Background()

	staged, err := a.Stage(ctx, candidate("Only This One"))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := a.Publish(ctx, staged); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	found, err := a.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(found) != 1 || found[0].Name != "Only This One" {
		t.Fatalf("Discover = %+v, want exactly [Only This One]", found)
	}
}

func TestValidateRejectsNameMismatch(t *testing.T) {
	dir := t.TempDir()
	a := &bambuadapter.LocalAdapter{Dir: dir, IsStudioRunning: notRunning}
	bad := &domain.RawProfile{Name: "A", Fields: map[string]any{"name": "B"}}
	if err := a.Validate(context.Background(), bad); !errors.Is(err, bambuadapter.ErrInvalidCandidate) {
		t.Fatalf("Validate(mismatched name) = %v, want ErrInvalidCandidate", err)
	}
}
