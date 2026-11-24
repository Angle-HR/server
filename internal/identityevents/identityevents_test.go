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

func TestStaffDeactivatedEvent(t *testing.T) {
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	a, err := StaffDeactivatedEvent(StaffDeactivated{StaffID: "s1", DeactivatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := StaffDeactivatedEvent(StaffDeactivated{StaffID: "s1", DeactivatedAt: at})
	c, _ := StaffDeactivatedEvent(StaffDeactivated{StaffID: "s1", DeactivatedAt: at.Add(time.Hour)})
	if a.Type != "identity.staff.deactivated.v1" || a.ID != b.ID || a.ID == c.ID || a.PartitionKey != "s1" || a.Region != "global" {
		t.Fatalf("%+v", a)
	}
	if err = a.Validate(); err != nil {
		t.Fatal(err)
	}
}
