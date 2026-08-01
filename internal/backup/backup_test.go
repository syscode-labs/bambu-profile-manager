package backup_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/backup"
)

func TestTakeListRestore(t *testing.T) {
	srcDir := t.TempDir()
	backupsRoot := t.TempDir()

	if err := os.WriteFile(filepath.Join(srcDir, "a.json"), []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatalf("write a.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, ".staging"), 0o755); err != nil {
		t.Fatalf("mkdir .staging: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, ".staging", "ignored.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write staging file: %v", err)
	}

	snap, err := backup.Take(srcDir, backupsRoot)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}

	snapshotted, err := os.ReadFile(filepath.Join(snap.Path, "a.json"))
	if err != nil {
		t.Fatalf("read snapshotted a.json: %v", err)
	}
	if string(snapshotted) != `{"v":1}` {
		t.Fatalf("snapshotted content = %q, want original", snapshotted)
	}
	if _, err := os.Stat(filepath.Join(snap.Path, ".staging")); err == nil {
		t.Fatal("Take copied .staging into the snapshot, want it skipped")
	}

	// Mutate the live file, then restore.
	if err := os.WriteFile(filepath.Join(srcDir, "a.json"), []byte(`{"v":2}`), 0o644); err != nil {
		t.Fatalf("mutate a.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "b.json"), []byte(`{"new":true}`), 0o644); err != nil {
		t.Fatalf("write b.json: %v", err)
	}

	if err := backup.Restore(snap.Path, srcDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	restored, err := os.ReadFile(filepath.Join(srcDir, "a.json"))
	if err != nil {
		t.Fatalf("read restored a.json: %v", err)
	}
	if string(restored) != `{"v":1}` {
		t.Fatalf("restored a.json = %q, want original snapshot content", restored)
	}
	// Restore must not delete files added after the snapshot.
	if _, err := os.Stat(filepath.Join(srcDir, "b.json")); err != nil {
		t.Fatalf("b.json was deleted by Restore, want non-destructive restore: %v", err)
	}

	list, err := backup.List(backupsRoot)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != snap.Name {
		t.Fatalf("List = %+v, want exactly the one snapshot taken", list)
	}
}

func TestListEmptyOnMissingRoot(t *testing.T) {
	list, err := backup.List(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("List = %v, want empty for a missing backups root", list)
	}
}

func TestListNewestFirst(t *testing.T) {
	srcDir := t.TempDir()
	backupsRoot := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "a.json"), []byte(`{}`), 0o644)

	first, err := backup.Take(srcDir, backupsRoot)
	if err != nil {
		t.Fatalf("Take 1: %v", err)
	}
	// Snapshot names are timestamp-based to nanosecond precision; force a
	// distinguishable ordering without depending on real elapsed time by
	// renaming the first snapshot to something guaranteed earlier.
	earlier := filepath.Join(backupsRoot, "20200101T000000.000000000Z")
	if err := os.Rename(first.Path, earlier); err != nil {
		t.Fatalf("rename snapshot: %v", err)
	}

	second, err := backup.Take(srcDir, backupsRoot)
	if err != nil {
		t.Fatalf("Take 2: %v", err)
	}

	list, err := backup.List(backupsRoot)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Name != second.Name {
		t.Fatalf("List = %+v, want newest (%s) first", list, second.Name)
	}
}
