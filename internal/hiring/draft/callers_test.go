package draft

import (
	"time"

	"github.com/Angle-HR/server/internal/hiring/drafttest"
	"github.com/Angle-HR/server/internal/rbac"
)

// ---- callers ----

const (
	org     = "bbbbbbbb-0000-4000-8000-000000000001"
	userA   = "aaaaaaaa-0000-4000-8000-000000000001"
	userB   = "aaaaaaaa-0000-4000-8000-000000000002"
	userC   = "aaaaaaaa-0000-4000-8000-000000000003"
	skillGo = "22222222-2222-4222-8222-222222222222"
	indTech = "33333333-3333-4333-8333-333333333333"
)

func caller(user string, role rbac.Role) Caller {
	return Caller{UserID: user, OrgID: org, CompanyName: "Acme Ltd", Perms: rbac.NewSet(rbac.DefaultMatrix[role]...)}
}

func newService() (*Service, *drafttest.Store) {
	st := drafttest.NewStore()
	ref := &drafttest.Ref{Skills: map[string]string{skillGo: "Go"}, Industry: indTech}
	fixed := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	return &Service{Store: st, Ref: ref, Now: func() time.Time { return fixed }}, st
}
