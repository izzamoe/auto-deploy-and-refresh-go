package deploy

import (
	"fmt"
	"regexp"
)

var runAsUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ValidateRunAsUser accepts an empty value (run as root, the pre-hardening behavior,
// Q-15) or a conservative POSIX user name; it is interpolated into the unit file.
func ValidateRunAsUser(name string) error {
	if name == "" {
		return nil
	}
	if !runAsUserPattern.MatchString(name) {
		return fmt.Errorf("must be a user name of 1-32 characters: lowercase letters, digits, '_' or '-', not starting with a digit or '-'")
	}
	return nil
}
