package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestValidateCode_ConcurrentWrongGuessesAreCountedAtomically(t *testing.T) {
	t.Parallel()
	store, mr := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateSession(ctx, VerificationSession{SessionID: "s1", Code: "123456"}); err != nil {
		t.Fatal(err)
	}

	const workers = 40
	var wg sync.WaitGroup
	var invalid atomic.Int32
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := store.ValidateCode(ctx, "s1", fmt.Sprintf("%06d", i)); errors.Is(err, ErrInvalidVerificationCode) {
				invalid.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if invalid.Load() != workers {
		t.Fatalf("invalid results = %d want %d", invalid.Load(), workers)
	}
	// A lost update would leave the counter below the number of requests.
	got, err := mr.Get(verifyAttemptsPrefix + "s1")
	if err != nil || got != fmt.Sprint(workers) {
		t.Fatalf("attempt counter = %q (%v) want %d", got, err, workers)
	}
	// The budget is spent, so even the right code is refused.
	if _, err := store.ValidateCode(ctx, "s1", "123456"); !errors.Is(err, ErrInvalidVerificationCode) {
		t.Fatalf("correct code after lockout: %v", err)
	}
}

func TestValidateCode_AttemptCapIsFive(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateSession(ctx, VerificationSession{SessionID: "s1", Code: "123456"}); err != nil {
		t.Fatal(err)
	}
	for range maxVerifyAttempts - 1 {
		if _, err := store.ValidateCode(ctx, "s1", "000000"); !errors.Is(err, ErrInvalidVerificationCode) {
			t.Fatalf("wrong code: %v", err)
		}
	}
	if _, err := store.ValidateCode(ctx, "s1", "123456"); err != nil {
		t.Fatalf("5th attempt with correct code should succeed: %v", err)
	}

	if err := store.CreateSession(ctx, VerificationSession{SessionID: "s2", Code: "123456"}); err != nil {
		t.Fatal(err)
	}
	for range maxVerifyAttempts {
		_, _ = store.ValidateCode(ctx, "s2", "000000")
	}
	if _, err := store.ValidateCode(ctx, "s2", "123456"); !errors.Is(err, ErrInvalidVerificationCode) {
		t.Fatalf("6th attempt must be refused: %v", err)
	}
}

func TestValidateCode_ConcurrentCorrectCodeSucceedsOnce(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateSession(ctx, VerificationSession{SessionID: "s1", Code: "123456"}); err != nil {
		t.Fatal(err)
	}

	const workers = 30
	var wg sync.WaitGroup
	var ok atomic.Int32
	start := make(chan struct{})
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := store.ValidateCode(ctx, "s1", "123456"); err == nil {
				ok.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("successful validations = %d want exactly 1", ok.Load())
	}
}

func TestValidateCode_DecoyNeverValidates(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateSession(ctx, VerificationSession{
		SessionID: "d1", Code: "123456", Purpose: PurposeLoginOTP, Decoy: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateCodeForPurpose(ctx, "d1", "123456", PurposeLoginOTP); !errors.Is(err, ErrInvalidVerificationCode) {
		t.Fatalf("decoy with its own code: %v", err)
	}
}

func TestValidateCode_ResendResetsAttempts(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	ctx := context.Background()
	s := VerificationSession{SessionID: "s1", Code: "123456"}
	if err := store.CreateSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	for range maxVerifyAttempts {
		_, _ = store.ValidateCode(ctx, "s1", "000000")
	}
	s.Code = "654321"
	if err := store.ReplaceSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateCode(ctx, "s1", "654321"); err != nil {
		t.Fatalf("fresh code after resend: %v", err)
	}
}

func TestValidatePassword(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pw string
		ok bool
	}{
		{"aaaaaaaa", false},
		{"12345678", false},
		{"abc123", false},
		{"abcdefg1", true},
		{"Correct-horse-9", true},
		{"pässwörd1", true},
		{"", false},
	}
	for _, c := range cases {
		if err := ValidatePassword(c.pw); (err == nil) != c.ok {
			t.Errorf("ValidatePassword(%q) = %v, want ok=%v", c.pw, err, c.ok)
		}
	}
	long := make([]byte, PasswordMaxLength+1)
	for i := range long {
		long[i] = 'a'
	}
	long[0] = '1'
	if err := ValidatePassword(string(long)); err == nil {
		t.Error("over-long password accepted")
	}
}

func TestResetStore_AllowForgotRequestCooldown(t *testing.T) {
	t.Parallel()
	store, mr := newTestStore(t)
	rs := &ResetStore{client: store.client}
	ctx := context.Background()
	if ok, err := rs.AllowForgotRequest(ctx, "a@example.com"); err != nil || !ok {
		t.Fatalf("first: %v %v", ok, err)
	}
	if ok, _ := rs.AllowForgotRequest(ctx, "a@example.com"); ok {
		t.Fatal("second request inside cooldown must be refused")
	}
	if ok, _ := rs.AllowForgotRequest(ctx, "b@example.com"); !ok {
		t.Fatal("other emails are independent")
	}
	mr.FastForward(forgotCooldown + 1)
	if ok, _ := rs.AllowForgotRequest(ctx, "a@example.com"); !ok {
		t.Fatal("allowed again after cooldown")
	}
}

func TestValidateCode_ReportsRemainingAttemptsAndLock(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateSession(ctx, VerificationSession{SessionID: "s1", Code: "123456"}); err != nil {
		t.Fatal(err)
	}
	for want := maxVerifyAttempts - 1; want >= 0; want-- {
		_, err := store.ValidateCode(ctx, "s1", "000000")
		var ae *VerifyAttemptsError
		if !errors.As(err, &ae) || ae.Locked || ae.Remaining != want {
			t.Fatalf("remaining: got %+v want %d", ae, want)
		}
		if !errors.Is(err, ErrInvalidVerificationCode) {
			t.Fatalf("must still match ErrInvalidVerificationCode: %v", err)
		}
	}
	_, err := store.ValidateCode(ctx, "s1", "123456")
	var ae *VerifyAttemptsError
	if !errors.As(err, &ae) || !ae.Locked || ae.RetryAfter < 1 || ae.RetryAfter > codeTTL {
		t.Fatalf("locked: %+v", ae)
	}
	// Resend (ReplaceSession) resets the budget.
	if err := store.ReplaceSession(ctx, VerificationSession{SessionID: "s1", Code: "654321"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateCode(ctx, "s1", "654321"); err != nil {
		t.Fatalf("fresh code after resend: %v", err)
	}
}
