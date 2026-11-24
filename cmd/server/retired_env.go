package main

import (
	"log/slog"
	"os"
)

// retiredEnvVars are variables earlier releases read for the admin account. The
// account now lives in the database and its first password is random (FR-053), so
// they do nothing; a non-empty value deserves a warning, an empty one does not.
var retiredEnvVars = []string{"ADMIN_USERNAME", "ADMIN_PASSWORD"}

// setRetiredEnv returns the retired variables that are set to a non-empty value.
func setRetiredEnv() []string {
	var set []string
	for _, k := range retiredEnvVars {
		if os.Getenv(k) != "" {
			set = append(set, k)
		}
	}
	return set
}

func warnRetiredEnv() {
	if set := setRetiredEnv(); len(set) > 0 {
		slog.Warn("these environment variables are no longer used and are ignored: the admin account lives in the database and its first password is random",
			"variables", set)
	}
}
