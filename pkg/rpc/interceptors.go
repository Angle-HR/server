package rpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
)

// DefaultTimeout is applied to outgoing calls whose context has no deadline.
const DefaultTimeout = 5 * time.Second

// Options tune the interceptors.
type Options struct {
	// DefaultTimeout overrides DefaultTimeout when positive.
	DefaultTimeout time.Duration
	// TraceID lets an OpenTelemetry integration supply the active trace id (for
	// example from trace.SpanContextFromContext). It is consulted only when the
	// call context carries none. Nil means "generate one".
	TraceID func(ctx context.Context) string
}

func (o Options) timeout() time.Duration {
	if o.DefaultTimeout > 0 {
		return o.DefaultTimeout
	}
	return DefaultTimeout
}

// ensure returns a copy of the context's call context with request and trace ids
// filled in, and the ctx that carries it.
func (o Options) ensure(ctx context.Context) (context.Context, *commonv1.CallContext) {
	cur, _ := CallContextFrom(ctx)
	cc := proto.Clone(cur).(*commonv1.CallContext)
	if cc.RequestId == "" {
		cc.RequestId = NewRequestID()
	}
	if cc.TraceId == "" && o.TraceID != nil {
		cc.TraceId = o.TraceID(ctx)
	}
	if cc.TraceId == "" {
		cc.TraceId = NewTraceID()
	}
	return WithCallContext(ctx, cc), cc
}

func outgoing(ctx context.Context, cc *commonv1.CallContext) context.Context {
	kv := []string{
		MDRequestID, cc.RequestId,
		MDTraceparent, FormatTraceparent(cc.TraceId, NewSpanID()),
	}
	add := func(k, v string) {
		if v != "" {
			kv = append(kv, k, v)
		}
	}
	add(MDRegion, cc.Region)
	add(MDCompanyID, cc.CompanyId)
	add(MDUserID, cc.UserId)
	if cc.ActorType != commonv1.ActorType_ACTOR_TYPE_UNSPECIFIED {
		kv = append(kv, MDActorType, cc.ActorType.String())
	}
	for _, p := range cc.Permissions {
		kv = append(kv, MDPermissions, p)
	}
	// Replace, not append: a forwarded incoming context must not duplicate keys.
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	for i := 0; i < len(kv); i += 2 {
		md.Delete(kv[i])
	}
	for i := 0; i < len(kv); i += 2 {
		md.Append(kv[i], kv[i+1])
	}
	return metadata.NewOutgoingContext(ctx, md)
}

func withDefaultDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// ClientUnaryInterceptor forwards the call context as metadata, fills in missing
// request and trace ids, and applies a default deadline.
func ClientUnaryInterceptor(o Options) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx, call := o.ensure(ctx)
		ctx, cancel := withDefaultDeadline(outgoing(ctx, call), o.timeout())
		defer cancel()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// ClientStreamInterceptor is the streaming counterpart. The default deadline is
// not applied to streams, which are expected to manage their own lifetime.
func ClientStreamInterceptor(o Options) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx, call := o.ensure(ctx)
		return streamer(outgoing(ctx, call), desc, cc, method, opts...)
	}
}

// fromIncoming rebuilds the call context from request metadata.
func (o Options) fromIncoming(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	first := func(k string) string {
		if v := md.Get(k); len(v) > 0 {
			return v[0]
		}
		return ""
	}
	cc := &commonv1.CallContext{
		Region:      first(MDRegion),
		CompanyId:   first(MDCompanyID),
		UserId:      first(MDUserID),
		Permissions: md.Get(MDPermissions),
		RequestId:   first(MDRequestID),
	}
	if v, ok := commonv1.ActorType_value[first(MDActorType)]; ok {
		cc.ActorType = commonv1.ActorType(v)
	}
	if tid, ok := ParseTraceparent(first(MDTraceparent)); ok {
		cc.TraceId = tid
	}
	ctx, _ = o.ensure(WithCallContext(ctx, cc))
	return ctx
}

// ServerUnaryInterceptor installs the caller's CallContext into the handler context.
// Handlers read it with CallContextFrom.
func ServerUnaryInterceptor(o Options) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		return h(o.fromIncoming(ctx), req)
	}
}

// ServerStreamInterceptor is the streaming counterpart.
func ServerStreamInterceptor(o Options) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		return h(srv, &wrappedStream{ServerStream: ss, ctx: o.fromIncoming(ss.Context())})
	}
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }
