package orgsvc

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/regionsaga"
)

type fakeMover struct {
	user   uuid.UUID
	target region.Region
	err    error
}

func (f *fakeMover) Move(_ context.Context, u uuid.UUID, t region.Region) (region.Region, error) {
	f.user, f.target = u, t
	return t, f.err
}

type fakeCountries map[uuid.UUID]region.Region

func (f fakeCountries) CountryRegion(_ context.Context, id uuid.UUID) (region.Region, error) {
	if r, ok := f[id]; ok {
		return r, nil
	}
	return region.RegionUnknown, nil
}

func TestCompleteRegionOnboarding(t *testing.T) {
	ctx := context.Background()
	user, country := uuid.New(), uuid.New()
	req := &orgv1.CompleteRegionOnboardingRequest{UserId: user.String(), CountryId: country.String()}

	if _, err := (&RPC{}).CompleteRegionOnboarding(ctx, req); status.Code(err) != codes.Unimplemented {
		t.Fatalf("saga off: %v", err)
	}

	m := &fakeMover{}
	r := &RPC{Mover: m, Countries: fakeCountries{country: region.RegionEU}}
	resp, err := r.CompleteRegionOnboarding(ctx, req)
	if err != nil || resp.GetRegion() != "eu" || m.user != user || m.target != region.RegionEU {
		t.Fatalf("%v %v %+v", resp, err, m)
	}
	if resp.GetAccessToken() != "" || resp.GetRefreshToken() != "" {
		t.Fatal("no tokens are issued; the caller rotates its session")
	}

	bad := []*orgv1.CompleteRegionOnboardingRequest{
		{UserId: "x", CountryId: country.String()},
		{UserId: user.String(), CountryId: "x"},
		{UserId: user.String(), CountryId: uuid.NewString()}, // unknown country
	}
	for _, b := range bad {
		if _, err := r.CompleteRegionOnboarding(ctx, b); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%+v: %v", b, err)
		}
	}

	for err, want := range map[error]codes.Code{
		regionsaga.ErrConflict:    codes.FailedPrecondition,
		regionsaga.ErrBusy:        codes.Aborted,
		regionsaga.ErrCompensated: codes.Aborted,
		errors.New("db"):          codes.Internal,
	} {
		r.Mover = &fakeMover{err: err}
		if _, got := r.CompleteRegionOnboarding(ctx, req); status.Code(got) != want {
			t.Fatalf("%v: got %v want %v", err, got, want)
		}
	}
}

func TestReactivateCompanyValidation(t *testing.T) {
	r := &RPC{}
	for _, req := range []*orgv1.ReactivateCompanyRequest{
		{CompanyId: "c", Region: "global", AdminId: "a"},
		{CompanyId: "c", Region: "mars", AdminId: "a"},
		{CompanyId: "", Region: "uk", AdminId: "a"},
		{CompanyId: "c", Region: "uk", AdminId: ""},
	} {
		if _, err := r.ReactivateCompany(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%+v: %v", req, err)
		}
	}
}
