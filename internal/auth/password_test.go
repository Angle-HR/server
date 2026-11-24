package auth

import "testing"

func TestHashAndCheckPassword(t *testing.T) {
	t.Parallel()

	hash, err := HashPassword("secure-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !CheckPassword(hash, "secure-password") {
		t.Fatal("expected password to match hash")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Fatal("expected wrong password to fail")
	}
}
