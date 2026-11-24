// Command orgfake is a stand-in for the org service until org-svc is merged: a tiny OrgService gRPC server over the
// shared databases, answering exactly what hiring asks: GetPublishReadiness, ResolveMembership, ListIndustries and
// ListCompanies (everything else is Unimplemented). It is a development tool, not a service: stop it to see hiring
// fail closed.
//
//	ORGFAKE_ADDR=:9091 DB_URL_GLOBAL=... ANGLEHR_UK_POSTGRES_DSN=... go run ./cmd/tools/orgfake
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/callerresolver"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/db"
	"github.com/Angle-HR/server/pkg/rpc"
)

type fake struct {
	orgv1.UnimplementedOrgServiceServer
	router *dbrouter.DBRouter
	global hiringstore.DB
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfgs, err := dbrouter.LoadConfigsFromEnv()
	if err != nil {
		return err
	}
	router, err := dbrouter.New(ctx, cfgs)
	if err != nil {
		return err
	}
	defer router.Close()
	global, err := db.NewGlobalPool(ctx, os.Getenv("DB_URL_GLOBAL"))
	if err != nil {
		return err
	}
	defer global.Close()
	addr := os.Getenv("ORGFAKE_ADDR")
	if addr == "" {
		addr = ":9091"
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := grpc.NewServer(append(rpc.ServerOptions("org", nil), grpc.ChainUnaryInterceptor(rpc.ServerUnaryInterceptor(rpc.Options{})))...)
	orgv1.RegisterOrgServiceServer(srv, &fake{router: router, global: global})
	go func() { <-ctx.Done(); srv.GracefulStop() }()
	log.Printf("orgfake listening on %s", addr)
	return srv.Serve(lis)
}

// store finds the region that holds a company.
func (f *fake) companySetup(ctx context.Context, company, dpa string) (*hiringtypes.CompanySetup, error) {
	for _, reg := range dbrouter.Regions() {
		pool, err := f.router.DB(reg)
		if err != nil {
			continue
		}
		cs, err := (&hiringstore.Store{DB: pool, Region: string(reg)}).CompanySetup(ctx, company, dpa)
		if err == nil {
			return cs, nil
		}
		if !errors.Is(err, hiringtypes.ErrNotFound) {
			return nil, err
		}
	}
	return nil, hiringtypes.ErrNotFound
}

func (f *fake) GetPublishReadiness(ctx context.Context, in *orgv1.GetPublishReadinessRequest) (*orgv1.GetPublishReadinessResponse, error) {
	cs, err := f.companySetup(ctx, in.GetCompanyId(), in.GetDpaVersion())
	if errors.Is(err, hiringtypes.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "no such company")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	kyb, ok := orgv1.KybStatus_value["KYB_STATUS_"+strings.ToUpper(cs.KYBStatus)]
	if !ok {
		kyb = int32(orgv1.KybStatus_KYB_STATUS_UNSPECIFIED)
	}
	return &orgv1.GetPublishReadinessResponse{
		KybStatus: orgv1.KybStatus(kyb), DpaAccepted: cs.DPAAccepted,
		PrivacyContactEmail: cs.PrivacyContact, DpoContact: cs.DPOContact,
	}, nil
}

func (f *fake) ResolveMembership(ctx context.Context, in *orgv1.ResolveMembershipRequest) (*orgv1.ResolveMembershipResponse, error) {
	cc, _ := rpc.CallContextFrom(ctx)
	reg := region.Region(cc.GetRegion())
	res := &callerresolver.Resolver{Regional: func(r region.Region) (callerresolver.DB, error) {
		p, err := f.router.DB(r)
		if err != nil {
			return nil, err
		}
		return p, nil
	}}
	m, err := res.Lookup(ctx, in.GetUserId(), reg)
	if err != nil {
		return &orgv1.ResolveMembershipResponse{}, nil //nolint:nilerr // "no company" and onboarding both mean has_company=false
	}
	out := &orgv1.ResolveMembershipResponse{
		HasCompany: true, CompanyId: m.OrgID, LegalName: m.CompanyName, RegisteredAddress: m.CompanyAddress,
		IsOwner: m.Owner, Roles: m.Roles,
	}
	for p := range m.Perms {
		out.Permissions = append(out.Permissions, string(p))
	}
	return out, nil
}

func (f *fake) ListIndustries(ctx context.Context, _ *orgv1.ListIndustriesRequest) (*orgv1.ListIndustriesResponse, error) {
	items, err := (&hiringstore.Global{DB: f.global}).Industries(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &orgv1.ListIndustriesResponse{}
	for _, it := range items {
		out.Industries = append(out.Industries, &orgv1.Industry{Id: it.ID, Slug: it.Slug, Name: it.Name})
	}
	return out, nil
}

func (f *fake) ListCompanies(ctx context.Context, _ *orgv1.ListCompaniesRequest) (*orgv1.ListCompaniesResponse, error) {
	cc, _ := rpc.CallContextFrom(ctx)
	pool, err := f.router.DB(region.Region(cc.GetRegion()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprint("region: ", err))
	}
	ids, err := (&hiringstore.Store{DB: pool}).TenantIDs(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &orgv1.ListCompaniesResponse{}
	for _, id := range ids {
		out.Companies = append(out.Companies, &orgv1.Company{Id: id, Region: cc.GetRegion()})
	}
	return out, nil
}
