package identityhttp

import (
	"context"
	"errors"
	"testing"

	"github.com/Angle-HR/server/internal/region"
)

type fakeDetails struct{ got string }

func (f *fakeDetails) Detail(_ context.Context, id string, _ region.Region) (CaseDetail, error) {
	f.got = id
	return CaseDetail{CompanyID: id, LegalName: "Acme"}, nil
}

// With ORG_APPLY=grpc the case details come from identity's own read (the contract has no RPC for them).
func TestOrgGRPCDetailFallsBackToDetails(t *testing.T) {
	f := &fakeDetails{}
	d, err := (&OrgGRPC{Details: f}).Detail(context.Background(), "c1", region.Region("uk"))
	if err != nil || d.LegalName != "Acme" || f.got != "c1" {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	if _, err := (&OrgGRPC{}).Detail(context.Background(), "c1", region.Region("uk")); !errors.Is(err, ErrDetailUnavailable) {
		t.Fatalf("without a fallback want ErrDetailUnavailable, got %v", err)
	}
}
