package progress

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProgressFrameRoundTripNormal(t *testing.T) {
	original := ProgressFrame{
		AppID:           "app1",
		JobID:           "job1",
		Tag:             "v1.0.0",
		Stage:           ProgressStageDownloading,
		Status:          ProgressStatusInProgress,
		Percent:         42,
		DownloadedBytes: 425,
		TotalBytes:      1000,
		SpeedBPS:        512,
		Message:         "downloading release asset",
	}

	encoded, err := EncodeProgressFrame(original)
	if err != nil {
		t.Fatalf("EncodeProgressFrame returned error: %v", err)
	}
	want := "v1" + progressFieldSeparator + "p" + progressFieldSeparator + "app1" + progressFieldSeparator + "job1" + progressFieldSeparator + "v1.0.0" + progressFieldSeparator + "downloading" + progressFieldSeparator + "in_progress" + progressFieldSeparator + "42" + progressFieldSeparator + "425" + progressFieldSeparator + "1000" + progressFieldSeparator + "512" + progressFieldSeparator + "ZG93bmxvYWRpbmcgcmVsZWFzZSBhc3NldA"
	if encoded != want {
		t.Fatalf("encoded frame = %q, want %q", encoded, want)
	}

	decoded, err := DecodeProgressFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeProgressFrame returned error: %v", err)
	}
	if decoded != original {
		t.Fatalf("decoded frame = %#v, want %#v", decoded, original)
	}
}

func TestProgressFrameRoundTripUnknownSizeDownload(t *testing.T) {
	original := ProgressFrame{
		AppID:           "app1",
		JobID:           "job1",
		Tag:             "v1.0.0",
		Stage:           ProgressStageDownloading,
		Status:          ProgressStatusInProgress,
		Percent:         -1,
		DownloadedBytes: 2048,
		TotalBytes:      -1,
		SpeedBPS:        256,
		Message:         "unknown size",
	}

	encoded, err := EncodeProgressFrame(original)
	if err != nil {
		t.Fatalf("EncodeProgressFrame returned error: %v", err)
	}
	decoded, err := DecodeProgressFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeProgressFrame returned error: %v", err)
	}
	if decoded != original {
		t.Fatalf("decoded frame = %#v, want %#v", decoded, original)
	}
}

func TestProgressFrameRoundTripEmptyMessage(t *testing.T) {
	original := ProgressFrame{
		AppID:           "app1",
		JobID:           "job1",
		Tag:             "v1.0.0",
		Stage:           ProgressStageQueued,
		Status:          ProgressStatusPending,
		Percent:         -1,
		DownloadedBytes: 0,
		TotalBytes:      -1,
		SpeedBPS:        0,
		Message:         "",
	}

	encoded, err := EncodeProgressFrame(original)
	if err != nil {
		t.Fatalf("EncodeProgressFrame returned error: %v", err)
	}
	if !strings.HasSuffix(encoded, progressFieldSeparator) {
		t.Fatalf("empty message should encode as empty final field, got %q", encoded)
	}
	decoded, err := DecodeProgressFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeProgressFrame returned error: %v", err)
	}
	if decoded != original {
		t.Fatalf("decoded frame = %#v, want %#v", decoded, original)
	}
}

func TestProgressFrameRoundTripUnicodeAndSeparatorsInMessage(t *testing.T) {
	original := ProgressFrame{
		AppID:           "app1",
		JobID:           "job1",
		Tag:             "v1.0.0",
		Stage:           ProgressStageValidating,
		Status:          ProgressStatusInProgress,
		Percent:         75,
		DownloadedBytes: 300,
		TotalBytes:      400,
		SpeedBPS:        123,
		Message:         "validated café ☕" + progressFieldSeparator + "line two\n<>&",
	}

	encoded, err := EncodeProgressFrame(original)
	if err != nil {
		t.Fatalf("EncodeProgressFrame returned error: %v", err)
	}
	parts := strings.Split(encoded, progressFieldSeparator)
	if len(parts) != progressFrameFieldCount {
		t.Fatalf("encoded field count = %d, want %d", len(parts), progressFrameFieldCount)
	}
	if strings.Contains(parts[11], progressFieldSeparator) || strings.Contains(parts[11], "\n") {
		t.Fatalf("message field was not safely base64url encoded: %q", parts[11])
	}
	decoded, err := DecodeProgressFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeProgressFrame returned error: %v", err)
	}
	if decoded != original {
		t.Fatalf("decoded frame = %#v, want %#v", decoded, original)
	}
}

func TestDecodeProgressFrameRejectsMalformedFrames(t *testing.T) {
	valid := mustEncodeProgressFrame(t, ProgressFrame{
		AppID:           "app1",
		JobID:           "job1",
		Tag:             "v1.0.0",
		Stage:           ProgressStageDownloading,
		Status:          ProgressStatusInProgress,
		Percent:         50,
		DownloadedBytes: 500,
		TotalBytes:      1000,
		SpeedBPS:        100,
		Message:         "ok",
	})

	tests := []struct {
		name  string
		frame string
	}{
		{name: "too few fields", frame: "v1" + progressFieldSeparator + "p"},
		{name: "too many fields", frame: valid + progressFieldSeparator + "extra"},
		{name: "unknown version", frame: strings.Replace(valid, "v1", "v2", 1)},
		{name: "unknown type", frame: strings.Replace(valid, "p", "x", 1)},
		{name: "bad percent", frame: replaceProgressFrameField(valid, 7, "abc")},
		{name: "decimal percent", frame: replaceProgressFrameField(valid, 7, "42.5")},
		{name: "percent below range", frame: replaceProgressFrameField(valid, 7, "-2")},
		{name: "percent above range", frame: replaceProgressFrameField(valid, 7, "101")},
		{name: "bad downloaded", frame: replaceProgressFrameField(valid, 8, "1.5")},
		{name: "bad total", frame: replaceProgressFrameField(valid, 9, "many")},
		{name: "bad speed", frame: replaceProgressFrameField(valid, 10, "fast")},
		{name: "decimal speed", frame: replaceProgressFrameField(valid, 10, "512.25")},
		{name: "bad base64", frame: replaceProgressFrameField(valid, 11, "%%%%")},
		{name: "unknown stage", frame: replaceProgressFrameField(valid, 5, "done")},
		{name: "unknown status", frame: replaceProgressFrameField(valid, 6, "running")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeProgressFrame(tt.frame); err == nil {
				t.Fatal("DecodeProgressFrame returned nil error")
			}
		})
	}
}

func TestDecodeProgressFrameRejectsMalformedCompactFrames(t *testing.T) {
	valid := mustEncodeProgressFrame(t, ProgressFrame{
		AppID:           "app1",
		JobID:           "job1",
		Tag:             "v1.0.0",
		Stage:           ProgressStageDownloading,
		Status:          ProgressStatusInProgress,
		Percent:         50,
		DownloadedBytes: 500,
		TotalBytes:      1000,
		SpeedBPS:        100,
		Message:         "ok",
	})

	tests := []struct {
		name  string
		frame string
	}{
		{name: "empty", frame: ""},
		{name: "missing empty message field", frame: strings.TrimSuffix(mustEncodeProgressFrame(t, ProgressFrame{AppID: "app1", JobID: "job1", Tag: "v1.0.0", Stage: ProgressStageQueued, Status: ProgressStatusPending, Percent: -1, DownloadedBytes: 0, TotalBytes: -1, SpeedBPS: 0, Message: ""}), progressFieldSeparator)},
		{name: "padded base64url message", frame: replaceProgressFrameField(valid, 11, "b2s=")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeProgressFrame(tt.frame); err == nil {
				t.Fatal("DecodeProgressFrame returned nil error")
			}
		})
	}
}

func TestEncodeProgressFrameRejectsInvalidValues(t *testing.T) {
	base := ProgressFrame{
		AppID:           "app1",
		JobID:           "job1",
		Tag:             "v1.0.0",
		Stage:           ProgressStageDownloading,
		Status:          ProgressStatusInProgress,
		Percent:         50,
		DownloadedBytes: 500,
		TotalBytes:      1000,
		SpeedBPS:        100,
		Message:         "ok",
	}

	tests := []struct {
		name string
		mut  func(*ProgressFrame)
	}{
		{name: "unknown stage", mut: func(f *ProgressFrame) { f.Stage = "done" }},
		{name: "unknown status", mut: func(f *ProgressFrame) { f.Status = "running" }},
		{name: "percent out of range", mut: func(f *ProgressFrame) { f.Percent = 101 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := base
			tt.mut(&frame)
			if _, err := EncodeProgressFrame(frame); err == nil {
				t.Fatal("EncodeProgressFrame returned nil error")
			}
		})
	}
}

func TestAllRequiredProgressStagesAndStatusesAreAccepted(t *testing.T) {
	for _, stage := range []string{
		ProgressStageQueued,
		ProgressStageDownloading,
		ProgressStageValidating,
		ProgressStageBackingUp,
		ProgressStageInstalling,
		ProgressStageRestarting,
		ProgressStageHealthcheck,
		ProgressStageRollback,
		ProgressStageSucceeded,
		ProgressStageFailed,
	} {
		t.Run("stage "+stage, func(t *testing.T) {
			_, err := EncodeProgressFrame(ProgressFrame{
				AppID:           "app1",
				JobID:           "job1",
				Tag:             "v1.0.0",
				Stage:           stage,
				Status:          ProgressStatusInProgress,
				Percent:         -1,
				DownloadedBytes: 0,
				TotalBytes:      -1,
				SpeedBPS:        0,
			})
			if err != nil {
				t.Fatalf("stage %q rejected: %v", stage, err)
			}
		})
	}

	for _, status := range []string{
		ProgressStatusPending,
		ProgressStatusInProgress,
		ProgressStatusSucceeded,
		ProgressStatusFailed,
	} {
		t.Run("status "+status, func(t *testing.T) {
			_, err := EncodeProgressFrame(ProgressFrame{
				AppID:           "app1",
				JobID:           "job1",
				Tag:             "v1.0.0",
				Stage:           ProgressStageDownloading,
				Status:          status,
				Percent:         -1,
				DownloadedBytes: 0,
				TotalBytes:      -1,
				SpeedBPS:        0,
			})
			if err != nil {
				t.Fatalf("status %q rejected: %v", status, err)
			}
		})
	}
}

func TestProgressFrameSmallerThanEquivalentJSON(t *testing.T) {
	frame := ProgressFrame{
		AppID:           "app1",
		JobID:           "job1",
		Tag:             "v1.0.0",
		Stage:           ProgressStageDownloading,
		Status:          ProgressStatusInProgress,
		Percent:         42,
		DownloadedBytes: 425,
		TotalBytes:      1000,
		SpeedBPS:        512,
		Message:         "downloading release asset",
	}
	compact, err := EncodeProgressFrame(frame)
	if err != nil {
		t.Fatalf("EncodeProgressFrame returned error: %v", err)
	}
	equivalentJSON, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	if len(compact) >= len(equivalentJSON) {
		t.Fatalf("compact frame length = %d, JSON length = %d; compact should be smaller", len(compact), len(equivalentJSON))
	}
}

func mustEncodeProgressFrame(t *testing.T, frame ProgressFrame) string {
	t.Helper()
	encoded, err := EncodeProgressFrame(frame)
	if err != nil {
		t.Fatalf("EncodeProgressFrame returned error: %v", err)
	}
	return encoded
}

func replaceProgressFrameField(frame string, field int, value string) string {
	parts := strings.Split(frame, progressFieldSeparator)
	parts[field] = value
	return strings.Join(parts, progressFieldSeparator)
}
