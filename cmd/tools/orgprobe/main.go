// Command orgprobe is a small manual check of the org service's gRPC API.
//
//	orgprobe readiness ADDR REGION COMPANY_ID [DPA_VERSION]   GetPublishReadiness
//	orgprobe membership ADDR REGION USER_ID                   ResolveMembership
//	orgprobe contact ADDR REGION COMPANY_ID                   GetCompanyContact
//	orgprobe members ADDR REGION COMPANY_ID                   ListMembers
//	orgprobe decide ADDR REGION COMPANY_ID REVIEWER_ID approve|reject [REASON]   ApplyKybDecision
//
// ADDR is host:port of the service's gRPC port. The region travels in the pkg/rpc call context, as it does between
// services.
package main

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/protobuf/proto"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/pkg/rpc"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Fprintln(os.Stderr, "usage: orgprobe readiness|membership|contact|members|decide ADDR REGION ID ...")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3], os.Args[4], os.Args[5:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(mode, addr, reg, id string, rest []string) error {
	conn, err := rpc.Dial(addr, "org", rpc.Options{})
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx := rpc.WithCallContext(context.Background(), &commonv1.CallContext{Region: reg})
	c := orgv1.NewOrgServiceClient(conn)
	var res proto.Message
	switch mode {
	case "readiness":
		v := ""
		if len(rest) > 0 {
			v = rest[0]
		}
		res, err = c.GetPublishReadiness(ctx, &orgv1.GetPublishReadinessRequest{CompanyId: id, DpaVersion: v})
	case "membership":
		res, err = c.ResolveMembership(ctx, &orgv1.ResolveMembershipRequest{UserId: id})
	case "contact":
		res, err = c.GetCompanyContact(ctx, &orgv1.GetCompanyContactRequest{CompanyId: id})
	case "members":
		res, err = c.ListMembers(ctx, &orgv1.ListMembersRequest{CompanyId: id})
	case "decide":
		if len(rest) < 2 {
			return fmt.Errorf("decide needs REVIEWER_ID approve|reject [REASON]")
		}
		reason := ""
		if len(rest) > 2 {
			reason = rest[2]
		}
		res, err = orgv1.NewAdminOrgServiceClient(conn).ApplyKybDecision(ctx, &orgv1.ApplyKybDecisionRequest{
			CompanyId: id, Region: reg, ReviewerAdminId: rest[0], Approve: rest[1] == "approve", Reason: reason,
		})
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	if err != nil {
		return err
	}
	fmt.Println(protojson.MarshalOptions{Multiline: true, EmitUnpopulated: true}.Format(res))
	return nil
}
