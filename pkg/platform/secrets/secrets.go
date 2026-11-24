// Package secrets resolves configuration values that may be secret. A value NAME comes from the environment
// variable NAME or, when that is unset, from the file named by NAME_FILE. This is the portable replacement for
// SSM Parameter Store and Key Vault reads inside the application: the platform (ECS secrets, Kubernetes or
// Docker secret mounts, Key Vault CSI driver) puts the value in an env var or a file, and the code never needs a
// cloud SDK.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrAmbiguous is returned when both NAME and NAME_FILE are set.
var ErrAmbiguous = errors.New("secrets: both the variable and its _FILE variant are set")

// Lookup returns the value of name and whether it was set. A set but empty variable counts as set. A NAME_FILE
// file's trailing newline is removed, as mounted secret files usually end with one. Setting both NAME and
// NAME_FILE is an error, so a stale environment value can never silently win over a rotated file.
func Lookup(name string) (string, bool, error) {
	val, hasVal := os.LookupEnv(name)
	path := strings.TrimSpace(os.Getenv(name + "_FILE"))
	hasFile := path != ""
	switch {
	case hasVal && hasFile:
		return "", false, fmt.Errorf("%w: %s", ErrAmbiguous, name)
	case hasFile:
		b, err := os.ReadFile(path) // #nosec G304 -- the path is operator-provided configuration.
		if err != nil {
			return "", false, fmt.Errorf("secrets: read %s_FILE: %w", name, err)
		}
		return strings.TrimRight(string(b), "\r\n"), true, nil
	case hasVal:
		return val, true, nil
	default:
		return "", false, nil
	}
}

// Get returns the value of name, or "" when it is unset.
func Get(name string) (string, error) {
	v, _, err := Lookup(name)
	return v, err
}

// Require returns the value of name, or an error when it is unset or empty.
func Require(name string) (string, error) {
	v, ok, err := Lookup(name)
	if err != nil {
		return "", err
	}
	if !ok || v == "" {
		return "", fmt.Errorf("secrets: %s (or %s_FILE) is required", name, name)
	}
	return v, nil
}

// Reader reads several values and keeps the first error, so a loader can read a list of settings and check once.
type Reader struct{ err error }

// Get returns the value of name, "" when unset, and remembers an error.
func (r *Reader) Get(name string) string {
	v, err := Get(name)
	if err != nil && r.err == nil {
		r.err = err
	}
	return v
}

// Err is the first error met by Get.
func (r *Reader) Err() error { return r.err }
