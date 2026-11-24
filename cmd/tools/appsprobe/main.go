// Command appsprobe is a small manual check of the applications service.
//
//	appsprobe grpc ADDR REGION COMPANY_ID USER_ID JOB_ID [PERMISSION...]   ListApplications over gRPC with CallContext metadata
//	appsprobe events NATS_URL                                              print the messages of the APPLICATIONS stream
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	applicationsv1 "github.com/Angle-HR/server/gen/openhr/applications/v1"
	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	"github.com/Angle-HR/server/pkg/rpc"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: appsprobe grpc|events ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "grpc":
		err = probeGRPC(os.Args[2:])
	case "events":
		err = probeEvents(os.Args[2])
	default:
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func probeGRPC(a []string) error {
	if len(a) < 5 {
		return fmt.Errorf("grpc needs ADDR REGION COMPANY_ID USER_ID JOB_ID")
	}
	conn, err := rpc.Dial(a[0], "applications", rpc.Options{})
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx := rpc.WithCallContext(context.Background(), &commonv1.CallContext{
		Region: a[1], CompanyId: a[2], UserId: a[3], Permissions: a[5:],
	})
	res, err := applicationsv1.NewApplicationsServiceClient(conn).ListApplications(ctx,
		&applicationsv1.ListApplicationsRequest{JobId: a[4], PageSize: 10})
	if err != nil {
		return err
	}
	fmt.Println(protojson.Format(res))
	return nil
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
	st, err := js.Stream(ctx, "APPLICATIONS")
	if err != nil {
		return err
	}
	info, _ := st.Info(ctx)
	fmt.Printf("stream APPLICATIONS: %d messages\n", info.State.Msgs)
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq; seq++ {
		m, err := st.GetMsg(ctx, seq)
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", m.Subject, string(m.Data))
	}
	return nil
}
