package main

import "testing"

func TestSetRetiredEnvReportsOnlyNonEmptyValues(t *testing.T) {
	t.Setenv("ADMIN_USERNAME", "")
	t.Setenv("ADMIN_PASSWORD", "")
	if got := setRetiredEnv(); len(got) != 0 {
		t.Errorf("empty values must not be reported, got %v", got)
	}
	t.Setenv("ADMIN_PASSWORD", "hunter2")
	got := setRetiredEnv()
	if len(got) != 1 || got[0] != "ADMIN_PASSWORD" {
		t.Errorf("setRetiredEnv() = %v, want [ADMIN_PASSWORD]", got)
	}
	for _, name := range got {
		if name == "hunter2" {
			t.Error("a value must never be reported, only the variable name")
		}
	}
}
