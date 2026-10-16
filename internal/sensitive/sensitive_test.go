package sensitive_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/Angle-HR/server/internal/sensitive"
)

func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func TestSealAndOpen(t *testing.T) {
	k := sensitive.NewKeyring()
	if err := k.Add("uk", "v1:"+key(1)); err != nil {
		t.Fatal(err)
	}
	s, err := k.Seal("UK", []byte(`"dyslexia"`), "t|a|q")
	if err != nil {
		t.Fatal(err)
	}
	if s.KeyID != "v1" || len(s.Nonce) != 12 || bytes.Contains(s.Ciphertext, []byte("dyslexia")) {
		t.Fatalf("sealed = %+v", s)
	}
	got, err := k.Open("uk", s, "t|a|q")
	if err != nil || string(got) != `"dyslexia"` {
		t.Fatalf("open = %q, %v", got, err)
	}

	// Bound to its row: another application, another question or a changed byte cannot be opened.
	if _, err = k.Open("uk", s, "t|a2|q"); !errors.Is(err, sensitive.ErrCannotOpen) {
		t.Errorf("another row: %v", err)
	}
	bad := s
	bad.Ciphertext = append([]byte{}, s.Ciphertext...)
	bad.Ciphertext[0] ^= 1
	if _, err = k.Open("uk", bad, "t|a|q"); !errors.Is(err, sensitive.ErrCannotOpen) {
		t.Errorf("tampered: %v", err)
	}
	// Two seals of the same value differ (random nonce).
	s2, _ := k.Seal("uk", []byte(`"dyslexia"`), "t|a|q")
	if bytes.Equal(s.Nonce, s2.Nonce) || bytes.Equal(s.Ciphertext, s2.Ciphertext) {
		t.Error("nonce reused")
	}
}

func TestRegionsAreSeparate(t *testing.T) {
	k := sensitive.NewKeyring()
	_ = k.Add("uk", "v1:"+key(1))
	_ = k.Add("eu", "v1:"+key(2))
	s, _ := k.Seal("uk", []byte("x"), "a")
	if _, err := k.Open("eu", s, "a"); !errors.Is(err, sensitive.ErrCannotOpen) {
		t.Errorf("the EU key must not open UK data: %v", err)
	}
	if _, err := k.Seal("us", []byte("x"), "a"); !errors.Is(err, sensitive.ErrNoKey) {
		t.Errorf("a region without keys must not seal: %v", err)
	}
	if k.Has("us") || !k.Has("UK") {
		t.Error("Has")
	}
	var nilRing *sensitive.Keyring
	if _, err := nilRing.Seal("uk", nil, ""); !errors.Is(err, sensitive.ErrNoKey) || nilRing.Has("uk") {
		t.Error("a nil keyring must refuse, not panic")
	}
}

func TestRotation(t *testing.T) {
	old := sensitive.NewKeyring()
	_ = old.Add("uk", "v1:"+key(1))
	sealedOld, _ := old.Seal("uk", []byte("secret"), "a")

	k := sensitive.NewKeyring()
	if err := k.Add("uk", "v2:"+key(2)+",v1:"+key(1)); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Open("uk", sealedOld, "a"); err != nil || string(got) != "secret" {
		t.Errorf("an old key still opens old rows: %q, %v", got, err)
	}
	if s, _ := k.Seal("uk", []byte("x"), "a"); s.KeyID != "v2" {
		t.Errorf("the first key seals: %s", s.KeyID)
	}
	gone := sensitive.NewKeyring()
	_ = gone.Add("uk", "v2:"+key(2))
	if _, err := gone.Open("uk", sealedOld, "a"); !errors.Is(err, sensitive.ErrNoKey) {
		t.Errorf("a dropped key: %v", err)
	}
}

func TestBadConfig(t *testing.T) {
	for name, spec := range map[string]string{
		"short key":  "v1:" + base64.StdEncoding.EncodeToString([]byte("short")),
		"not base64": "v1:***",
		"no id":      key(1),
		"dup id":     "v1:" + key(1) + ",v1:" + key(2),
		"empty":      " , ",
	} {
		if err := sensitive.NewKeyring().Add("uk", spec); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadFromEnv(t *testing.T) {
	env := map[string]string{"ANGLEHR_UK_SENSITIVE_KEYS": "v1:" + key(1)}
	k, err := sensitive.LoadFromEnv([]string{"uk", "us"}, func(n string) string { return env[n] })
	if err != nil || !k.Has("uk") || k.Has("us") {
		t.Fatalf("k=%v err=%v", k, err)
	}
	env["ANGLEHR_US_SENSITIVE_KEYS"] = "v1:oops"
	if _, err = sensitive.LoadFromEnv([]string{"uk", "us"}, func(n string) string { return env[n] }); err == nil ||
		!strings.Contains(err.Error(), "us") {
		t.Errorf("a broken key must fail start-up and name the region: %v", err)
	}
}
