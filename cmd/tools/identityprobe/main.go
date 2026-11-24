// Command identityprobe is a manual check of the identity service.
//
//	identityprobe events NATS_URL                         print the messages of the IDENTITY stream
//	identityprobe grpc ADDR USER_COOKIE ADMIN_COOKIE ADMIN_ID [EMAIL]
//	                                                      call the read RPCs and print the answers
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	identityv1 "github.com/Angle-HR/server/gen/openhr/identity/v1"
	"github.com/Angle-HR/server/pkg/rpc"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: identityprobe events|grpc ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "events":
		err = probeEvents(os.Args[2])
	case "grpc":
		err = probeGRPC(os.Args[2:])
	default:
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func probeEvents(url string) error {
	nc, err := nats.Connect(url)
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := js.Stream(ctx, "IDENTITY")
	if err != nil {
		return err
	}
	info, _ := st.Info(ctx)
	fmt.Printf("stream IDENTITY: %d messages\n", info.State.Msgs)
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq; seq++ {
		m, err := st.GetMsg(ctx, seq)
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", m.Subject, string(m.Data))
	}
	return nil
}

func show(name string, m proto.Message, err error) {
	if err != nil {
		fmt.Printf("%-24s ERROR %v\n", name, err)
		return
	}
	fmt.Printf("%-24s %s\n", name, protojson.MarshalOptions{}.Format(m))
}

func probeGRPC(a []string) error {
	if len(a) < 4 {
		return fmt.Errorf("grpc needs ADDR USER_COOKIE ADMIN_COOKIE ADMIN_ID [EMAIL]")
	}
	conn, err := rpc.Dial(a[0], "identity", rpc.Options{})
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id := identityv1.NewIdentityServiceClient(conn)
	adm := identityv1.NewAdminIdentityServiceClient(conn)

	v, err := id.ValidateAccessToken(ctx, &identityv1.ValidateAccessTokenRequest{Token: a[1]})
	show("ValidateAccessToken", v, err)
	show("ValidateAccessToken bad", func() proto.Message {
		r, e := id.ValidateAccessToken(ctx, &identityv1.ValidateAccessTokenRequest{Token: "nope"})
		_ = e
		return r
	}(), nil)
	av, err := id.ValidateAdminToken(ctx, &identityv1.ValidateAdminTokenRequest{Token: a[2]})
	show("ValidateAdminToken", av, err)
	if v != nil {
		u, err := id.GetUser(ctx, &identityv1.GetUserRequest{UserId: v.GetUserId()})
		show("GetUser", u, err)
		b, err := id.BatchGetUsers(ctx, &identityv1.BatchGetUsersRequest{UserIds: []string{v.GetUserId()}, ExcludeInactive: true})
		show("BatchGetUsers", b, err)
		l, err := id.LookupRegion(ctx, &identityv1.LookupRegionRequest{Key: &identityv1.LookupRegionRequest_UserId{UserId: v.GetUserId()}})
		show("LookupRegion", l, err)
	}
	r, err := adm.ListAdminRoles(ctx, &identityv1.ListAdminRolesRequest{})
	show("ListAdminRoles", r, err)
	h, err := adm.HasAdminPermission(ctx, &identityv1.HasAdminPermissionRequest{AdminUserId: a[3], Permission: "users:read"})
	show("HasAdminPermission", h, err)
	lu, err := adm.ListUsers(ctx, &identityv1.ListUsersRequest{PageSize: 2})
	show("ListUsers", lu, err)
	q, err := adm.ListVerificationQueue(ctx, &identityv1.ListVerificationQueueRequest{PageSize: 5})
	show("ListVerificationQueue", q, err)
	if v != nil && os.Getenv("PROBE_SET_ACTIVE") == "1" {
		off, err := adm.SetUserActive(ctx, &identityv1.SetUserActiveRequest{UserId: v.GetUserId(), Active: false})
		show("SetUserActive(false)", off, err)
		_, verr := id.ValidateAccessToken(ctx, &identityv1.ValidateAccessTokenRequest{Token: a[1]})
		fmt.Printf("%-24s %v\n", "session after disable", verr)
		on, err := adm.SetUserActive(ctx, &identityv1.SetUserActiveRequest{UserId: v.GetUserId(), Active: true})
		show("SetUserActive(true)", on, err)
	}
	_, err = id.PrepareRegionMove(ctx, &identityv1.PrepareRegionMoveRequest{})
	fmt.Printf("%-24s %v\n", "PrepareRegionMove", err)
	return nil
}
