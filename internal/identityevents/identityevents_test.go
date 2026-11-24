package identityevents

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEnabled(t *testing.T) {
	t.Setenv(EnvVar, "")
	if Enabled() {
		t.Fatal("must default to off")
	}
	t.Setenv(EnvVar, " ON ")
	if !Enabled() {
		t.Fatal("ON should enable")
	}
}

func TestPayloadsHaveNoPersonalData(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	evs := []func() (string, error){
		func() (string, error) {
			e, err := RegisteredEvent(UserRegistered{UserID: "u1", Region: "global", RegisteredAt: at})
			return string(e.Data), err
		},
		func() (string, error) {
			e, err := EmailVerifiedEvent(UserEmailVerified{UserID: "u1", Region: "uk", VerifiedAt: at})
			return string(e.Data), err
		},
		func() (string, error) {
			e, err := DeactivatedEvent(UserDeactivated{UserID: "u1", Region: "uk", ByAdminID: "a1", DeactivatedAt: at})
			return string(e.Data), err
		},
		func() (string, error) {
			e, err := StaffInvitedEvent(StaffInvited{StaffID: "s1", InvitedBy: "a1", InvitedAt: at})
			return string(e.Data), err
		},
	}
	for i, f := range evs {
		data, err := f()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		for k := range m {
			if strings.Contains(k, "email") && !strings.HasSuffix(k, "_verified_at") {
				t.Fatalf("event %d carries %q", i, k)
			}
			if strings.Contains(k, "name") || strings.Contains(k, "password") {
				t.Fatalf("event %d carries %q", i, k)
			}
		}
	}
}

func TestIDsAreStable(t *testing.T) {
	at := time.Now()
	a, _ := RegisteredEvent(UserRegistered{UserID: "u1", Region: "global", RegisteredAt: at})
	b, _ := RegisteredEvent(UserRegistered{UserID: "u1", Region: "global", RegisteredAt: at.Add(time.Hour)})
	if a.ID != b.ID {
		t.Fatal("registered id must derive from the user only")
	}
	if a.Type != TypeUserRegistered || a.Subject != "u1" || a.PartitionKey != "u1" {
		t.Fatalf("unexpected envelope %+v", a)
	}
	s, _ := StaffInvitedEvent(StaffInvited{StaffID: "s1", InvitedAt: at})
	if s.Region != "global" {
		t.Fatalf("staff region = %q", s.Region)
	}
}
