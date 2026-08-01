// Package backup takes and restores point-in-time snapshots of a Bambu
// Studio profile directory. This is the safety net for direct-publish
// (no manual export/review step, per decisions.md): every publish takes
// a snapshot first, and a snapshot can always be restored.
package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const timeFormat = "20060102T150405.000000000Z"

// Snapshot is one point-in-time backup.
type Snapshot struct {
	Name string    // directory name under the backups root
	Path string    // full path to the snapshot directory
	At   time.Time
}

// Take copies every regular file directly under srcDir (non-recursive,
// matching what bambuadapter.Discover/Publish operate on — profile .json
// and .info files, not the .staging/.backups subdirectories) into a new
// timestamped directory under backupsRoot, and returns it.
func Take(srcDir, backupsRoot string) (Snapshot, error) {
	now := time.Now().UTC()
	name := now.Format(timeFormat)
	dest := filepath.Join(backupsRoot, name)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return Snapshot{}, fmt.Errorf("backup: mkdir %s: %w", dest, err)
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return Snapshot{}, fmt.Errorf("backup: read %s: %w", srcDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue // skips .staging, .backups, and any nested dirs
		}
		if err := copyFile(filepath.Join(srcDir, e.Name()), filepath.Join(dest, e.Name())); err != nil {
			return Snapshot{}, err
		}
	}
	return Snapshot{Name: name, Path: dest, At: now}, nil
}

// List returns every snapshot under backupsRoot, newest first.
func List(backupsRoot string) ([]Snapshot, error) {
	entries, err := os.ReadDir(backupsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("backup: list %s: %w", backupsRoot, err)
	}
	var out []Snapshot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		at, err := time.Parse(timeFormat, e.Name())
		if err != nil {
			continue // not one of ours, skip rather than fail the whole list
		}
		out = append(out, Snapshot{Name: e.Name(), Path: filepath.Join(backupsRoot, e.Name()), At: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// Restore copies every file from the snapshot back into destDir,
// overwriting files with the same name. It never deletes files present in
// destDir but absent from the snapshot — a non-destructive restore, since
// destDir may have gained profiles since the snapshot was taken that the
// user still wants. For a full point-in-time revert, take a fresh snapshot
// of destDir first (so that state isn't lost either).
func Restore(snapshotPath, destDir string) error {
	entries, err := os.ReadDir(snapshotPath)
	if err != nil {
		return fmt.Errorf("backup: read snapshot %s: %w", snapshotPath, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(snapshotPath, e.Name()), filepath.Join(destDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("backup: open %s: %w", src, err)
	}
	defer in.Close()

	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("backup: create %s: %w", tmp, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("backup: copy %s -> %s: %w", src, tmp, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("backup: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil { // atomic on the same filesystem
		return fmt.Errorf("backup: rename %s -> %s: %w", tmp, dest, err)
	}
	return nil
}
