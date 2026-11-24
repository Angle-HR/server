package auth

import "testing"

func TestTOTPCryptoRoundTrip(t *testing.T) {
	t.Parallel()

	crypto, err := NewTOTPCrypto("test-key")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.Encrypt("SECRET123")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := crypto.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "SECRET123" {
		t.Fatalf("got %q", plain)
	}
}

func TestTOTPCryptoRotation(t *testing.T) {
	t.Parallel()

	old, err := NewTOTPCrypto("old-key")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := old.Encrypt("SECRET123")
	if err != nil {
		t.Fatal(err)
	}

	// New key alone cannot read it.
	alone, err := NewTOTPCryptoKeys("new-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, decErr := alone.Decrypt(enc); decErr == nil {
		t.Fatal("new key alone must not decrypt old payloads")
	}

	// With the old key listed as previous it can, and it always encrypts with the new key.
	rot, err := NewTOTPCryptoKeys("new-key", []string{"", "old-key"})
	if err != nil {
		t.Fatal(err)
	}
	if plain, decErr := rot.Decrypt(enc); decErr != nil || plain != "SECRET123" {
		t.Fatalf("Decrypt old = %q, %v", plain, decErr)
	}
	fresh, err := rot.Encrypt("SECRET456")
	if err != nil {
		t.Fatal(err)
	}
	if _, decErr := old.Decrypt(fresh); decErr == nil {
		t.Fatal("new payloads must not be readable with the old key")
	}
	if plain, decErr := alone.Decrypt(fresh); decErr != nil || plain != "SECRET456" {
		t.Fatalf("Decrypt fresh = %q, %v", plain, decErr)
	}

	// Reencrypt moves old payloads to the new key and leaves current ones alone.
	moved, changed, err := rot.Reencrypt(enc)
	if err != nil || !changed {
		t.Fatalf("Reencrypt old: changed=%v err=%v", changed, err)
	}
	if plain, decErr := alone.Decrypt(moved); decErr != nil || plain != "SECRET123" {
		t.Fatalf("Decrypt moved = %q, %v", plain, decErr)
	}
	same, changed, err := rot.Reencrypt(fresh)
	if err != nil || changed || same != fresh {
		t.Fatalf("Reencrypt current: changed=%v err=%v", changed, err)
	}
	if _, _, err := rot.Reencrypt("not-valid"); err == nil {
		t.Fatal("garbage must fail")
	}
	stranger, _ := NewTOTPCrypto("stranger")
	other, _ := stranger.Encrypt("x")
	if _, _, err := rot.Reencrypt(other); err == nil {
		t.Fatal("unknown key must fail")
	}
}
