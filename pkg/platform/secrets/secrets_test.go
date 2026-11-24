package secrets_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Angle-HR/server/pkg/platform/secrets"
)

func file(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvValue(t *testing.T) {
	t.Setenv("S_TEST", "from-env")
	if v, ok, err := secrets.Lookup("S_TEST"); v != "from-env" || !ok || err != nil {
		t.Fatalf("got %q %v %v", v, ok, err)
	}
}

func TestFileValueTrimsTrailingNewline(t *testing.T) {
	t.Setenv("S_TEST_FILE", file(t, "from-file\n"))
	if v, ok, err := secrets.Lookup("S_TEST"); v != "from-file" || !ok || err != nil {
		t.Fatalf("got %q %v %v", v, ok, err)
	}
}

func TestUnset(t *testing.T) {
	if v, ok, err := secrets.Lookup("S_TEST_UNSET"); v != "" || ok || err != nil {
		t.Fatalf("got %q %v %v", v, ok, err)
	}
	if _, err := secrets.Require("S_TEST_UNSET"); err == nil {
		t.Fatal("Require of an unset value must fail")
	}
}

func TestBothSetIsAmbiguous(t *testing.T) {
	t.Setenv("S_TEST", "a")
	t.Setenv("S_TEST_FILE", file(t, "b"))
	if _, err := secrets.Get("S_TEST"); !errors.Is(err, secrets.ErrAmbiguous) {
		t.Fatalf("got %v", err)
	}
}

func TestUnreadableFileIsAnError(t *testing.T) {
	t.Setenv("S_TEST_FILE", filepath.Join(t.TempDir(), "missing"))
	if _, err := secrets.Get("S_TEST"); err == nil {
		t.Fatal("expected error")
	}
}

func TestReaderKeepsFirstError(t *testing.T) {
	t.Setenv("S_TEST_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("S_OK", "x")
	var r secrets.Reader
	r.Get("S_TEST")
	if r.Get("S_OK") != "x" || r.Err() == nil {
		t.Fatalf("reader state wrong: %v", r.Err())
	}
}
