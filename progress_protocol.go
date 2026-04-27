package main

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	progressProtocolVersion = "v1"
	progressFrameType       = "p"
	progressFrameFieldCount = 12
	progressFieldSeparator  = "\x1f"

	ProgressStageQueued      = "queued"
	ProgressStageDownloading = "downloading"
	ProgressStageValidating  = "validating"
	ProgressStageBackingUp   = "backing_up"
	ProgressStageInstalling  = "installing"
	ProgressStageRestarting  = "restarting"
	ProgressStageHealthcheck = "healthcheck"
	ProgressStageRollback    = "rollback"
	ProgressStageSucceeded   = "succeeded"
	ProgressStageFailed      = "failed"

	ProgressStatusPending    = "pending"
	ProgressStatusInProgress = "in_progress"
	ProgressStatusSucceeded  = "succeeded"
	ProgressStatusFailed     = "failed"
)

var (
	allowedProgressStages = map[string]struct{}{
		ProgressStageQueued:      {},
		ProgressStageDownloading: {},
		ProgressStageValidating:  {},
		ProgressStageBackingUp:   {},
		ProgressStageInstalling:  {},
		ProgressStageRestarting:  {},
		ProgressStageHealthcheck: {},
		ProgressStageRollback:    {},
		ProgressStageSucceeded:   {},
		ProgressStageFailed:      {},
	}
	allowedProgressStatuses = map[string]struct{}{
		ProgressStatusPending:    {},
		ProgressStatusInProgress: {},
		ProgressStatusSucceeded:  {},
		ProgressStatusFailed:     {},
	}
)

// ProgressFrame is the compact text payload used for frequent progress updates.
type ProgressFrame struct {
	AppID           string
	JobID           string
	Tag             string
	Stage           string
	Status          string
	Percent         int64
	DownloadedBytes int64
	TotalBytes      int64
	SpeedBPS        int64
	Message         string
}

// EncodeProgressFrame serializes f as:
// v1\x1Fp\x1F<appID>\x1F<jobID>\x1F<tag>\x1F<stage>\x1F<status>\x1F<pct>\x1F<downloaded>\x1F<total>\x1F<speedBPS>\x1F<messageB64>.
func EncodeProgressFrame(f ProgressFrame) (string, error) {
	if err := validateProgressFrame(f); err != nil {
		return "", err
	}

	parts := []string{
		progressProtocolVersion,
		progressFrameType,
		f.AppID,
		f.JobID,
		f.Tag,
		f.Stage,
		f.Status,
		strconv.FormatInt(f.Percent, 10),
		strconv.FormatInt(f.DownloadedBytes, 10),
		strconv.FormatInt(f.TotalBytes, 10),
		strconv.FormatInt(f.SpeedBPS, 10),
		base64.RawURLEncoding.EncodeToString([]byte(f.Message)),
	}
	return strings.Join(parts, progressFieldSeparator), nil
}

// DecodeProgressFrame parses and validates a compact progress frame.
func DecodeProgressFrame(frame string) (ProgressFrame, error) {
	parts := strings.Split(frame, progressFieldSeparator)
	if len(parts) != progressFrameFieldCount {
		return ProgressFrame{}, fmt.Errorf("progress frame field count = %d, want %d", len(parts), progressFrameFieldCount)
	}
	if parts[0] != progressProtocolVersion {
		return ProgressFrame{}, fmt.Errorf("unsupported progress protocol version %q", parts[0])
	}
	if parts[1] != progressFrameType {
		return ProgressFrame{}, fmt.Errorf("unsupported progress frame type %q", parts[1])
	}

	pct, err := strconv.ParseInt(parts[7], 10, 64)
	if err != nil {
		return ProgressFrame{}, fmt.Errorf("invalid progress percent: %w", err)
	}
	downloaded, err := strconv.ParseInt(parts[8], 10, 64)
	if err != nil {
		return ProgressFrame{}, fmt.Errorf("invalid downloaded bytes: %w", err)
	}
	total, err := strconv.ParseInt(parts[9], 10, 64)
	if err != nil {
		return ProgressFrame{}, fmt.Errorf("invalid total bytes: %w", err)
	}
	speed, err := strconv.ParseInt(parts[10], 10, 64)
	if err != nil {
		return ProgressFrame{}, fmt.Errorf("invalid speed bytes per second: %w", err)
	}
	messageBytes, err := base64.RawURLEncoding.Strict().DecodeString(parts[11])
	if err != nil {
		return ProgressFrame{}, fmt.Errorf("invalid message base64url: %w", err)
	}

	f := ProgressFrame{
		AppID:           parts[2],
		JobID:           parts[3],
		Tag:             parts[4],
		Stage:           parts[5],
		Status:          parts[6],
		Percent:         pct,
		DownloadedBytes: downloaded,
		TotalBytes:      total,
		SpeedBPS:        speed,
		Message:         string(messageBytes),
	}
	if err := validateProgressFrame(f); err != nil {
		return ProgressFrame{}, err
	}
	return f, nil
}

func validateProgressFrame(f ProgressFrame) error {
	if _, ok := allowedProgressStages[f.Stage]; !ok {
		return fmt.Errorf("unknown progress stage %q", f.Stage)
	}
	if _, ok := allowedProgressStatuses[f.Status]; !ok {
		return fmt.Errorf("unknown progress status %q", f.Status)
	}
	if !validProgressPercent(f.Percent) {
		return fmt.Errorf("progress percent %v out of range", f.Percent)
	}
	return nil
}

func validProgressPercent(pct int64) bool {
	if pct == -1 {
		return true
	}
	return pct >= 0 && pct <= 100
}
