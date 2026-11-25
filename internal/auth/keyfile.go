package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// keyLen is the session key size: 256 bits of entropy (FR-003).
const keyLen = 32

// LoadOrCreateKey returns the persistent session signing key stored at path
// (Q-1, D6). A missing file is a first start and creates the key. A file of the
// wrong size is corrupt: it is replaced (regenerated=true) and every existing
// session becomes invalid. The file is always owner-only (0600); a wider mode on
// an otherwise valid key is tightened with a warning.
func LoadOrCreateKey(path string) (key []byte, regenerated bool, err error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(data) == keyLen {
			if fi, statErr := os.Stat(path); statErr == nil && fi.Mode().Perm() != 0o600 {
				slog.Warn("session key file mode was too open; tightening to 0600", "path", path, "mode", fi.Mode().Perm().String())
				if chErr := os.Chmod(path, 0o600); chErr != nil {
					return nil, false, fmt.Errorf("session key %s: chmod 0600: %w", path, chErr)
				}
			}
			return data, false, nil
		}
		slog.Warn("session key file has the wrong size; regenerating it (all sessions are invalidated)", "path", path, "size", len(data))
		regenerated = true
	case errors.Is(err, fs.ErrNotExist):
		// first start
	default:
		return nil, false, fmt.Errorf("session key %s: read: %w", path, err)
	}

	key = make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, false, fmt.Errorf("session key: generate: %w", err)
	}
	if err := writeKeyFile(path, key); err != nil {
		return nil, false, err
	}
	return key, regenerated, nil
}

// writeKeyFile stores key atomically (temp file + rename) with mode 0600.
func writeKeyFile(path string, key []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("session key %s: create directory: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, ".session-key-*")
	if err != nil {
		return fmt.Errorf("session key %s: create temp file: %w", path, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("session key %s: chmod: %w", path, err)
	}
	if _, err := tmp.Write(key); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("session key %s: write: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("session key %s: sync: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("session key %s: close: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("session key %s: rename: %w", path, err)
	}
	return nil
}
