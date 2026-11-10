package binding

import (
	"context"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// AdvertisementHeader is the gRPC response header a credential-binding proxy
// sets on every response, with the value [AdvertisementValue]. An application
// in proxy mode sends plaintext credentials only to a server that sets it.
const (
	AdvertisementHeader = "credentials-binding"
	AdvertisementValue  = "v1"
)

type probeKey struct{}

type probe struct{ answered atomic.Bool }

// WithProbe marks ctx as a probe for a credentials interceptor. A call made
// with the returned context is answered locally by the first credentials
// interceptor it reaches, without contacting the server. The returned func
// reports whether one did.
func WithProbe(ctx context.Context) (context.Context, func() bool) {
	p := &probe{}
	return context.WithValue(ctx, probeKey{}, p), p.answered.Load
}

// AnswerProbe reports whether ctx is a probe, marking it answered. Credentials
// interceptors call it first and return without invoking when it's true.
func AnswerProbe(ctx context.Context) bool {
	p, ok := ctx.Value(probeKey{}).(*probe)
	if !ok {
		return false
	}
	p.answered.Store(true)
	return true
}

// IsAdvertised reports whether response headers md identify a credential-binding
// proxy.
func IsAdvertised(md metadata.MD) bool {
	for _, v := range md.Get(AdvertisementHeader) {
		if v == AdvertisementValue {
			return true
		}
	}
	return false
}

func advertise(ctx context.Context) {
	_ = grpc.SetHeader(ctx, metadata.Pairs(AdvertisementHeader, AdvertisementValue))
}

// UnaryServerInterceptor sets [AdvertisementHeader] on every unary response. A
// proxy that runs the binding layer installs it on its server.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		advertise(ctx)
		return handler(ctx, req)
	}
}

// StreamServerInterceptor sets [AdvertisementHeader] on every stream, for
// proxies that forward unary calls through a stream handler.
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		_ = ss.SetHeader(metadata.Pairs(AdvertisementHeader, AdvertisementValue))
		return handler(srv, ss)
	}
}
