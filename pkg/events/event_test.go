package events_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/Angle-HR/server/pkg/events"
)

func TestEnvelopeJSONRoundTrip(t *testing.T) {
	tp := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	in, err := events.NewJSON("/services/hiring", "hiring.application.submitted.v1", map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	in = in.WithSubject("app-1").WithRegion("eu").WithCompany("co").WithPartitionKey("app-1").WithExtension("x1", "y")
	in.TraceParent = tp
	b, err := events.Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	_ = json.Unmarshal(b, &wire)
	for k, want := range map[string]any{"specversion": "1.0", "type": in.Type, "region": "eu", "companyid": "co",
		"partitionkey": "app-1", "traceparent": tp, "datacontenttype": "application/json", "x1": "y"} {
		if wire[k] != want {
			t.Errorf("wire[%s] = %v, want %v", k, wire[k], want)
		}
	}
	if d, ok := wire["data"].(map[string]any); !ok || d["n"] != float64(1) {
		t.Errorf("JSON data must be embedded, got %v", wire["data"])
	}
	out, err := events.Decode(b)
	if err != nil || out.ID != in.ID || !out.Time.Equal(in.Time) || out.Extensions["x1"] != "y" || string(out.Data) != `{"n":1}` {
		t.Errorf("round trip: %+v err=%v", out, err)
	}
}

func TestProtoPayloadIsBase64(t *testing.T) {
	in := events.NewProto("/s", "a.b", []byte{0x08, 0x96, 0x01})
	b, _ := events.Encode(in)
	if !strings.Contains(string(b), "data_base64") {
		t.Fatalf("binary payload must use data_base64: %s", b)
	}
	out, err := events.Decode(b)
	if err != nil || string(out.Data) != string(in.Data) || out.DataContentType != events.ContentTypeProto {
		t.Errorf("out = %+v err=%v", out, err)
	}
}

func TestValidate(t *testing.T) {
	ok := events.New("/s", "a.b")
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]events.Event{
		"no source":     {ID: "1", Type: "a.b"},
		"wildcard type": events.New("/s", "a.*"),
		"bad trace":     func() events.Event { e := ok; e.TraceParent = "nope"; return e }(),
		"reserved ext":  ok.WithExtension("region", "x"),
		"bad ext name":  ok.WithExtension("Bad_Name", "x"),
	}
	for name, e := range bad {
		if e.Validate() == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		f, s string
		want bool
	}{
		{"a.b.c", "a.b.c", true}, {"a.*", "a.b", true}, {"a.*", "a.b.c", false}, {"a.>", "a.b.c", true},
		{"a.>", "a", false}, {"a.*.c", "a.b.c", true}, {">", "a.b", true}, {"a.b", "a.b.c", false},
	} {
		if events.Match(c.f, c.s) != c.want {
			t.Errorf("Match(%q,%q) != %v", c.f, c.s, c.want)
		}
	}
}

type fakeProp struct{}

func (fakeProp) Inject(context.Context) (string, string) {
	return "00-11111111111111111111111111111111-2222222222222222-01", "k=v"
}
func (fakeProp) Extract(ctx context.Context, _, _ string) context.Context { return ctx }

func TestTraceInjectExtract(t *testing.T) {
	tp := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	ctx := events.WithTraceParent(context.Background(), tp)
	e := events.InjectTrace(ctx, events.New("/s", "a.b"))
	if e.TraceParent != tp {
		t.Fatalf("inject: %q", e.TraceParent)
	}
	again := events.InjectTrace(context.Background(), e) // an existing value is kept
	if again.TraceParent != tp {
		t.Error("existing traceparent overwritten")
	}
	if events.InjectTrace(context.Background(), events.New("/s", "a.b")).TraceParent != "" {
		t.Error("no trace in ctx must leave it empty")
	}
	got := events.ExtractTrace(context.Background(), e)
	if p, _ := (events.ContextPropagator{}).Inject(got); p != tp {
		t.Errorf("extract: %q", p)
	}
	// A custom propagator (for example OpenTelemetry) replaces the default.
	events.SetPropagator(fakeProp{})
	defer events.SetPropagator(nil)
	if e := events.InjectTrace(context.Background(), events.New("/s", "a.b")); e.TraceState != "k=v" {
		t.Errorf("custom propagator not used: %+v", e)
	}
}

// TestNoCloudSDKs enforces the port rule: no AWS or Azure SDK anywhere below pkg/events.
func TestNoCloudSDKs(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-test", "./...").CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, banned := range []string{"github.com/aws/", "github.com/Azure/", "github.com/microsoft/", "cloud.google.com/"} {
			if strings.HasPrefix(dep, banned) {
				t.Errorf("pkg/events depends on cloud SDK package %s", dep)
			}
		}
	}
}
