package deploy

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Seams of the binary replacement (FR-043); tests replace them.
var (
	// linkFile makes the backup of the running binary (hard link: same inode, so the
	// path of the binary never disappears).
	linkFile = os.Link
	// syncFile flushes a file's contents (and metadata) to disk.
	syncFile = func(path string) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	}
	// syncDir flushes a directory entry change (rename / link / create) to disk.
	syncDir = func(dir string) error {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
)

// deployingMarkerPath is the transaction marker of an in-flight replacement.
func deployingMarkerPath(binaryPath string) string { return binaryPath + ".deploying" }

// DeployingMarker is the content of <bin>.deploying.
type DeployingMarker struct {
	JobID     string    `json:"jobId"`
	Tag       string    `json:"tag"`
	HadBinary bool      `json:"hadBinary"` // a previous binary existed (so <bin>.bak is its backup)
	StartedAt time.Time `json:"startedAt"`
}

// writeDeployingMarker durably records that a replacement of binaryPath started.
func writeDeployingMarker(binaryPath, jobID, tag string, hadBinary bool) error {
	data, err := json.Marshal(DeployingMarker{JobID: jobID, Tag: tag, HadBinary: hadBinary, StartedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	path := deployingMarkerPath(binaryPath)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	if err := syncFile(path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(binaryPath))
}

// makeBackup keeps a copy of the current binary at bak WITHOUT ever removing the
// binary path: a hard link (same inode) or, where links are impossible, a copy that
// is fsynced and renamed into place. An older bak is replaced.
func makeBackup(binaryPath, bak string) error {
	if err := removeIfExists(bak); err != nil {
		return err
	}
	linkErr := linkFile(binaryPath, bak)
	if linkErr == nil {
		return syncDir(filepath.Dir(bak))
	}
	slog.Warn("backup hard link failed; copying instead", "err", linkErr)
	return copyFileDurably(binaryPath, bak)
}

func copyFileDurably(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	// O_CREATE is subject to the umask; make the copy exactly as executable as the original.
	if err = os.Chmod(tmp, info.Mode().Perm()); err != nil {
		return err
	}
	if err = syncFile(tmp); err != nil {
		return err
	}
	if err = os.Rename(tmp, dst); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dst))
}

// installBinary atomically puts the new binary in place: the new file is fsynced,
// renamed over the old one (the path always names a complete file) and the directory
// is fsynced so the rename survives a crash.
func installBinary(tmpPath, binaryPath string) error {
	if err := syncFile(tmpPath); err != nil {
		return fmt.Errorf("fsync new binary: %w", err)
	}
	if err := renameFile(tmpPath, binaryPath); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(binaryPath)); err != nil {
		// The rename happened; failing the deploy now would roll back a good install.
		slog.Warn("fsync binary directory failed", "err", err)
	}
	return nil
}

// restoreBackup puts the backup back as the binary and closes the transaction (removes
// the marker). When the backup is still a hard link of the binary the rename is a
// no-op that leaves both names, so a leftover backup is removed.
func restoreBackup(backupPath, binaryPath string) error {
	if err := renameFile(backupPath, binaryPath); err != nil {
		return err
	}
	if err := removeIfExists(backupPath); err != nil {
		slog.Warn("remove leftover backup failed", "err", err)
	}
	if err := syncDir(filepath.Dir(binaryPath)); err != nil {
		slog.Warn("fsync binary directory failed", "err", err)
	}
	_ = os.Remove(deployingMarkerPath(binaryPath))
	return nil
}
