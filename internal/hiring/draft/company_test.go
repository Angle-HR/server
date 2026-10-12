package draft

import (
	"context"
	"testing"

	"github.com/Angle-HR/server/internal/rbac"
)

func TestAcceptDPA(t *testing.T) {
	s, st, _, _ := publishEnv(t)
	ctx := context.Background()
	founder, legal, hr1 := caller(userA, rbac.RoleFounder), caller(userB, rbac.RoleLegal), caller(userC, rbac.RoleHR1)

	if _, err := s.AcceptDPA(ctx, hr1, CurrentDPAVersion); !isForbidden(err) {
		t.Errorf("hr1 accept: %v", err)
	}
	if _, err := s.AcceptDPA(ctx, founder, "2020-old"); !isInvalid(err) {
		t.Errorf("old version: %v", err)
	}
	got, err := s.AcceptDPA(ctx, founder, CurrentDPAVersion)
	if err != nil || !got.DPAAccepted || got.DPAVersion != CurrentDPAVersion {
		t.Fatalf("accept: %v %+v", err, got)
	}
	if _, err = s.AcceptDPA(ctx, legal, CurrentDPAVersion); err != nil {
		t.Errorf("legal accept: %v", err)
	}
	if len(st.DPAAccepted) == 0 {
		t.Error("nothing stored")
	}
}

func TestPrivacyContact(t *testing.T) {
	s, st, _, _ := publishEnv(t)
	ctx := context.Background()
	hr1 := caller(userA, rbac.RoleHR1)
	for _, bad := range []string{"", "not an email", "Name <a@b.co>"} {
		if _, err := s.SetPrivacyContact(ctx, hr1, PrivacyContactInput{Email: bad}); !isInvalid(err) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	got, err := s.SetPrivacyContact(ctx, hr1, PrivacyContactInput{Email: " privacy@acme.test ", DPO: "Dana, dpo@acme.test"})
	if err != nil || got.PrivacyContact != "privacy@acme.test" || st.DPO == "" {
		t.Fatalf("set: %v %+v", err, got)
	}
	if _, err = s.SetPrivacyContact(ctx, caller(userB, rbac.RoleHR2), PrivacyContactInput{Email: "a@b.co"}); !isForbidden(err) {
		t.Errorf("hr2: %v", err)
	}
}

func TestRecordDPIAIsLegalOnly(t *testing.T) {
	s, _, _, _ := publishEnv(t)
	ctx := context.Background()
	if _, err := s.RecordDPIA(ctx, caller(userA, rbac.RoleFounder), DPIAInput{Reference: "doc"}); !isForbidden(err) {
		t.Errorf("founder: %v", err)
	}
	legal := caller(userB, rbac.RoleLegal)
	if _, err := s.RecordDPIA(ctx, legal, DPIAInput{}); !isInvalid(err) {
		t.Errorf("empty: %v", err)
	}
	got, err := s.RecordDPIA(ctx, legal, DPIAInput{Reference: "notion://dpia-1", Summary: "Knockout questions flag candidates"})
	if err != nil || got.DPIAVersion != 1 {
		t.Fatalf("record: %v %+v", err, got)
	}
	got, err = s.RecordDPIA(ctx, legal, DPIAInput{Summary: "v2"})
	if err != nil || got.DPIAVersion != 2 {
		t.Fatalf("second: %v %+v", err, got)
	}
}
