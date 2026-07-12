package main

import "testing"

func TestBuildUpgradePipelineLatest(t *testing.T) {
	got, err := buildUpgradePipeline("https://example.test/install.sh", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "curl -fsSL 'https://example.test/install.sh' | sh -s --"
	if got != want {
		t.Fatalf("pipeline mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildUpgradePipelinePinnedVersion(t *testing.T) {
	got, err := buildUpgradePipeline("https://example.test/install.sh", []string{"v1.2.3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "curl -fsSL 'https://example.test/install.sh' | sh -s -- 'v1.2.3'"
	if got != want {
		t.Fatalf("pipeline mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildUpgradePipelineRejectsExtraArgs(t *testing.T) {
	if _, err := buildUpgradePipeline("https://example.test/install.sh", []string{"v1.2.3", "extra"}); err == nil {
		t.Fatal("expected error for too many arguments, got nil")
	}
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	// A crafted URL with a single quote must not break out of the quoting.
	got := shellQuote("v1'; rm -rf /; echo '")
	want := `'v1'\''; rm -rf /; echo '\'''`
	if got != want {
		t.Fatalf("shellQuote mismatch:\n got: %s\nwant: %s", got, want)
	}
}
