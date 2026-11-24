// Command hiringprobe is a manual check of the hiring service's gRPC API.
//
//	hiringprobe ADDR open [LIMIT]                              ListOpenJobs (no call context)
//	hiringprobe ADDR public PUBLIC_ID                          GetPublicJob (no call context)
//	hiringprobe ADDR list REGION COMPANY_ID USER_ID [PERM...]  ListJobs as that caller
//	hiringprobe ADDR publish REGION COMPANY_ID USER_ID JOB_ID [PERM...]  PublishJob as that caller
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	hiringv1 "github.com/Angle-HR/server/gen/openhr/hiring/v1"
	"github.com/Angle-HR/server/pkg/rpc"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: hiringprobe ADDR open|public|list|publish ...")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(addr, cmd string, args []string) error {
	conn, err := rpc.Dial(addr, "hiring", rpc.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	c := hiringv1.NewHiringServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	caller := func(a []string) ([]string, context.Context, error) {
		if len(a) < 3 {
			return nil, nil, fmt.Errorf("need REGION COMPANY_ID USER_ID")
		}
		return a[3:], rpc.WithCallContext(ctx, &commonv1.CallContext{Region: a[0], CompanyId: a[1], UserId: a[2]}), nil
	}
	withPerms := func(ctx context.Context, perms []string) context.Context {
		cc, _ := rpc.CallContextFrom(ctx)
		cc.Permissions = perms
		return rpc.WithCallContext(ctx, cc)
	}
	var out proto.Message
	switch cmd {
	case "open":
		limit := 10
		if len(args) > 0 {
			limit, _ = strconv.Atoi(args[0])
		}
		out, err = c.ListOpenJobs(ctx, &hiringv1.ListOpenJobsRequest{Limit: int32(limit)})
	case "public":
		if len(args) < 1 {
			return fmt.Errorf("need PUBLIC_ID")
		}
		out, err = c.GetPublicJob(ctx, &hiringv1.GetPublicJobRequest{PublicId: args[0]})
	case "list":
		rest, cctx, cerr := caller(args)
		if cerr != nil {
			return cerr
		}
		out, err = c.ListJobs(withPerms(cctx, rest), &hiringv1.ListJobsRequest{})
	case "publish":
		rest, cctx, cerr := caller(args)
		if cerr != nil || len(rest) < 1 {
			return fmt.Errorf("need REGION COMPANY_ID USER_ID JOB_ID")
		}
		out, err = c.PublishJob(withPerms(cctx, rest[1:]), &hiringv1.PublishJobRequest{JobId: rest[0]})
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		return err
	}
	fmt.Println(protojson.MarshalOptions{Multiline: true}.Format(out))
	return nil
}
