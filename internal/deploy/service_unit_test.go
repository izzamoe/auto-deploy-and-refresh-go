package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"
)

func TestValidateServiceUnitName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "myapp", false},
		{"valid with hyphen", "my-app", false},
		{"valid with underscore", "my_app", false},
		{"valid with digits", "app123", false},
		{"valid single char", "a", false},
		{"valid max length", strings.Repeat("a", 100), false},
		{"empty", "", true},
		{"too long", strings.Repeat("a", 101), true},
		{"contains dot", "my.app", true},
		{"contains slash", "foo/bar", true},
		{"contains backslash", `foo\bar`, true},
		{"contains space", "my app", true},
		{"leading dash", "-myapp", true},
		{"leading dot", ".myapp", true},
		{"path traversal", "../../etc/passwd", true},
		{"absolute path", "/etc/passwd", true},
		{"embedded null byte", "myapp\x00.service", true},
		{"only dots", "..", true},
		{"trailing slash", "myapp/", true},
		{"unicode", "myapp™", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateServiceUnitName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateServiceUnitName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestRenderServiceUnit(t *testing.T) {
	t.Parallel()

	app := store.App{
		Name:        "myapp",
		BinaryPath:  "/opt/myapp/myapp",
		ServiceName: "myapp.service",
	}

	got := RenderServiceUnit(app)
	want := generatedUnitMarker + "\n" +
		"[Unit]\n" +
		"Description=myapp (managed by auto-deploy)\n" +
		"After=network.target\n\n" +
		"[Service]\n" +
		"ExecStart=/opt/myapp/myapp\n" +
		"WorkingDirectory=/opt/myapp\n" +
		"Restart=on-failure\n" +
		"RestartSec=5\n\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"

	if got != want {
		t.Fatalf("RenderServiceUnit() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderServiceUnitIncludesEnvVars(t *testing.T) {
	t.Parallel()

	app := store.App{
		Name:        "bot",
		BinaryPath:  "/opt/bot/bot",
		ServiceName: "bot.service",
		EnvVars: []store.EnvVar{
			{Name: "BOT_TOKEN", Value: "123:abc"},
			{Name: "LOG_LEVEL", Value: "info"},
		},
	}

	got := RenderServiceUnit(app)
	want := generatedUnitMarker + "\n" +
		"[Unit]\n" +
		"Description=bot (managed by auto-deploy)\n" +
		"After=network.target\n\n" +
		"[Service]\n" +
		"ExecStart=/opt/bot/bot\n" +
		"WorkingDirectory=/opt/bot\n" +
		"Restart=on-failure\n" +
		"RestartSec=5\n" +
		"Environment=BOT_TOKEN=\"123:abc\"\n" +
		"Environment=LOG_LEVEL=\"info\"\n\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"

	if got != want {
		t.Fatalf("RenderServiceUnit() with env =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderServiceUnitSanitisesEnvValue(t *testing.T) {
	t.Parallel()

	app := store.App{
		Name:        "bot",
		BinaryPath:  "/opt/bot/bot",
		ServiceName: "bot.service",
		EnvVars: []store.EnvVar{
			{Name: "EVIL", Value: "x\nExecStartPre=/bin/malicious"},
		},
	}

	got := RenderServiceUnit(app)

	// The newline is stripped, so the injected directive cannot appear at the
	// start of a line.
	if strings.Contains(got, "\nExecStartPre=") {
		t.Fatalf("env value newline injection not sanitised:\n%q", got)
	}
}

func TestRenderServiceUnitSanitisesName(t *testing.T) {
	t.Parallel()

	app := store.App{
		Name:        "myapp\nExecStartPre=/bin/malicious\n# ",
		BinaryPath:  "/opt/myapp/myapp",
		ServiceName: "myapp.service",
	}

	got := RenderServiceUnit(app)

	// Newlines are removed from the Description value, so "ExecStartPre="
	// does not appear at the start of a line as a systemd directive.
	if strings.Contains(got, "\nExecStartPre=") {
		t.Fatal("newlines in app.Name must not inject systemd directives")
	}
}

func TestPreviewServiceUnitInvalidName(t *testing.T) {
	t.Parallel()

	app := store.App{
		Name:        "evil",
		BinaryPath:  "/opt/evil/evil",
		ServiceName: "../../etc/passwd",
	}

	_, err := PreviewServiceUnit(app)
	if err == nil {
		t.Fatal("expected error for invalid service name, got nil")
	}
}

func TestPreviewServiceUnitDoesNotTouchDisk(t *testing.T) {
	// Not t.Parallel(): mutates the shared package-level systemdUnitDir var
	// (same convention as the existing runSystemctl/renameFile stubs in
	// main_test.go, which also run without t.Parallel()).
	dir := t.TempDir()
	origDir := systemdUnitDir
	systemdUnitDir = dir
	t.Cleanup(func() { systemdUnitDir = origDir })

	app := store.App{
		Name:        "myapp",
		BinaryPath:  "/opt/myapp/myapp",
		ServiceName: "myapp.service",
	}

	unit, err := PreviewServiceUnit(app)
	if err != nil {
		t.Fatalf("PreviewServiceUnit: %v", err)
	}
	if !strings.HasPrefix(unit, generatedUnitMarker) {
		t.Fatalf("preview does not start with marker: %q", unit)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files written by preview, found %d", len(entries))
	}
}

func stubServiceUnitSystemctl(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	original := runSystemctl
	runSystemctl = func(name string, args ...string) ([]byte, error) {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		return []byte("ok"), nil
	}
	t.Cleanup(func() { runSystemctl = original })
	return &calls
}

func TestApplyServiceUnitFreshWrite(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level runSystemctl/systemdUnitDir vars.
	dir := t.TempDir()
	origDir := systemdUnitDir
	systemdUnitDir = dir
	t.Cleanup(func() { systemdUnitDir = origDir })

	calls := stubServiceUnitSystemctl(t)

	app := store.App{
		Name:        "myapp",
		BinaryPath:  "/opt/myapp/myapp",
		ServiceName: "myapp.service",
	}

	if err := ApplyServiceUnit(app); err != nil {
		t.Fatalf("ApplyServiceUnit: %v", err)
	}

	written, err := os.ReadFile(filepath.Join(dir, "myapp.service"))
	if err != nil {
		t.Fatalf("read written unit file: %v", err)
	}
	if !strings.HasPrefix(string(written), generatedUnitMarker) {
		t.Fatalf("written unit file missing marker: %q", string(written))
	}

	if len(*calls) != 2 {
		t.Fatalf("expected 2 systemctl calls, got %d: %v", len(*calls), *calls)
	}
	if (*calls)[0][0] != "daemon-reload" {
		t.Fatalf("first call = %v, want daemon-reload", (*calls)[0])
	}
	if (*calls)[1][0] != "enable" || (*calls)[1][1] != "myapp" {
		t.Fatalf("second call = %v, want [enable myapp]", (*calls)[1])
	}
}

func TestApplyServiceUnitRegenerateOverSelfGenerated(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level runSystemctl/systemdUnitDir vars.
	dir := t.TempDir()
	origDir := systemdUnitDir
	systemdUnitDir = dir
	t.Cleanup(func() { systemdUnitDir = origDir })

	stubServiceUnitSystemctl(t)

	app := store.App{
		Name:        "myapp",
		BinaryPath:  "/opt/myapp/myapp",
		ServiceName: "myapp.service",
	}

	if err := ApplyServiceUnit(app); err != nil {
		t.Fatalf("first ApplyServiceUnit: %v", err)
	}

	// Change the app slightly and re-apply: since the existing file was
	// generated by us, this must succeed (regenerate).
	app.BinaryPath = "/opt/myapp2/myapp"
	if err := ApplyServiceUnit(app); err != nil {
		t.Fatalf("second ApplyServiceUnit (regenerate): %v", err)
	}

	written, err := os.ReadFile(filepath.Join(dir, "myapp.service"))
	if err != nil {
		t.Fatalf("read written unit file: %v", err)
	}
	if !strings.Contains(string(written), "/opt/myapp2/myapp") {
		t.Fatalf("regenerated unit file does not reflect updated binary path: %q", string(written))
	}
}

func TestApplyServiceUnitRefusesHandWritten(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level runSystemctl/systemdUnitDir vars.
	dir := t.TempDir()
	origDir := systemdUnitDir
	systemdUnitDir = dir
	t.Cleanup(func() { systemdUnitDir = origDir })

	calls := stubServiceUnitSystemctl(t)

	targetPath := filepath.Join(dir, "myapp.service")
	handWritten := "# hand written by an operator\n[Unit]\nDescription=custom\n"
	if err := os.WriteFile(targetPath, []byte(handWritten), 0644); err != nil {
		t.Fatalf("write hand-written unit file: %v", err)
	}

	app := store.App{
		Name:        "myapp",
		BinaryPath:  "/opt/myapp/myapp",
		ServiceName: "myapp.service",
	}

	err := ApplyServiceUnit(app)
	if err == nil {
		t.Fatal("expected refusal error, got nil")
	}
	if !IsRefuseOverwriteError(err) {
		t.Fatalf("expected refuse-overwrite error, got: %v", err)
	}

	after, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("read unit file after refused apply: %v", readErr)
	}
	if string(after) != handWritten {
		t.Fatalf("hand-written unit file was modified: got %q, want %q", string(after), handWritten)
	}

	if len(*calls) != 0 {
		t.Fatalf("expected no systemctl calls on refusal, got %v", *calls)
	}
}

func TestApplyServiceUnitInvalidNameRejectedBeforeIO(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level runSystemctl/systemdUnitDir vars.
	dir := t.TempDir()
	origDir := systemdUnitDir
	systemdUnitDir = dir
	t.Cleanup(func() { systemdUnitDir = origDir })

	calls := stubServiceUnitSystemctl(t)

	app := store.App{
		Name:        "evil",
		BinaryPath:  "/opt/evil/evil",
		ServiceName: "../../etc/passwd",
	}

	if err := ApplyServiceUnit(app); err == nil {
		t.Fatal("expected error for invalid service name, got nil")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files written for invalid name, found %d", len(entries))
	}
	if len(*calls) != 0 {
		t.Fatalf("expected no systemctl calls for invalid name, got %v", *calls)
	}
}

func TestApplyServiceUnitServiceNameWithoutSuffix(t *testing.T) {
	// Not t.Parallel(): mutates shared package-level runSystemctl/systemdUnitDir vars.
	dir := t.TempDir()
	origDir := systemdUnitDir
	systemdUnitDir = dir
	t.Cleanup(func() { systemdUnitDir = origDir })

	calls := stubServiceUnitSystemctl(t)

	app := store.App{
		Name:        "myapp",
		BinaryPath:  "/opt/myapp/myapp",
		ServiceName: "myapp",
	}

	if err := ApplyServiceUnit(app); err != nil {
		t.Fatalf("ApplyServiceUnit: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "myapp.service")); err != nil {
		t.Fatalf("expected unit file at myapp.service: %v", err)
	}
	if (*calls)[1][1] != "myapp" {
		t.Fatalf("enable call = %v, want myapp (no double suffix)", (*calls)[1])
	}
}
