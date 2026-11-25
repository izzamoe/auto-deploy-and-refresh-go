package deploy

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
)

// RecoveryAction describes what startup recovery did for one binary path.
type RecoveryAction struct {
	BinaryPath string
	Action     string // restored_backup | cleared_marker | removed_stale_tmp
	JobID      string
}

func readDeployingMarker(binaryPath string) (*DeployingMarker, bool) {
	data, err := os.ReadFile(deployingMarkerPath(binaryPath))
	if err != nil {
		return nil, false
	}
	var m DeployingMarker
	if err := json.Unmarshal(data, &m); err != nil {
		// A torn or foreign marker still means "a deploy was in flight".
		return &DeployingMarker{}, true
	}
	return &m, true
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// RecoverInterrupted runs at startup (B19, FR-044, NFR-007) and makes every binary
// path consistent again after a process was killed in the middle of a deploy:
//
//   - the binary is missing but <bin>.bak exists -> the backup is restored;
//   - a <bin>.deploying marker is present together with a backup -> the install was
//     never confirmed healthy, so the previous binary is restored;
//   - a marker without a backup (first deploy) -> the marker is cleared and an
//     installed binary is left alone;
//   - a leftover <bin>.tmp is removed.
//
// A healthy install (no marker, binary present) is never touched. It does not restart
// services; a restart is the operator's (or the next deploy's) call.
func RecoverInterrupted(binaryPaths []string) []RecoveryAction {
	var actions []RecoveryAction
	for _, bin := range binaryPaths {
		bak := bin + ".bak"
		marker, hasMarker := readDeployingMarker(bin)
		jobID := ""
		if hasMarker {
			jobID = marker.JobID
		}
		binExists := fileExists(bin)
		bakExists := fileExists(bak)

		var action string
		switch {
		case !binExists && bakExists:
			action = "restored_backup"
		case hasMarker && bakExists && (marker.HadBinary || !binExists):
			action = "restored_backup"
		case hasMarker:
			action = "cleared_marker"
		}

		switch action {
		case "restored_backup":
			if err := restoreBackup(bak, bin); err != nil {
				slog.Error("recovery: restoring the backup failed; leaving the marker", "binary", bin, "err", err)
				continue
			}
		case "cleared_marker":
			if err := os.Remove(deployingMarkerPath(bin)); err != nil && !errors.Is(err, os.ErrNotExist) {
				slog.Warn("recovery: removing the marker failed", "binary", bin, "err", err)
			}
		}
		if err := os.Remove(bin + ".tmp"); err == nil && action == "" {
			action = "removed_stale_tmp"
		} else if err == nil {
			// removed together with another action
		}
		if action != "" {
			slog.Warn("recovery of an interrupted deploy", "binary", bin, "action", action, "job", jobID)
			actions = append(actions, RecoveryAction{BinaryPath: bin, Action: action, JobID: jobID})
		}
	}
	return actions
}
