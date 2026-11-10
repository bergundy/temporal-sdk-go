package credentials

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/contrib/credentials/binding"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// proxyGuard keeps an application in proxy mode from sending plaintext
// credentials anywhere but a credential-binding proxy. It watches responses
// for the proxy's advertisement header, and holds back a request that carries
// credentials until the connection has seen one.
type proxyGuard struct {
	advertised atomic.Bool
}

const getSystemInfoMethod = "/temporal.api.workflowservice.v1.WorkflowService/GetSystemInfo"

func (g *proxyGuard) unary(
	ctx context.Context,
	method string,
	req, reply any,
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	if binding.AnswerProbe(ctx) {
		return nil
	}
	if m, ok := req.(proto.Message); ok && binding.HasCredentials(m) && !g.advertised.Load() {
		// A lazy client hasn't made a call yet.
		if err := g.observe(ctx, getSystemInfoMethod, &workflowservice.GetSystemInfoRequest{},
			&workflowservice.GetSystemInfoResponse{}, cc, invoker); err != nil {
			return fmt.Errorf("credentials: checking whether the server is a credential-binding proxy: %w", err)
		}
		if !g.advertised.Load() {
			return status.Error(codes.FailedPrecondition,
				"credentials: not sending plaintext credentials: the server didn't identify itself as a credential-binding proxy")
		}
	}
	return g.observe(ctx, method, req, reply, cc, invoker, opts...)
}

// observe invokes a call and records whether its response advertised a
// credential-binding proxy. A failed call says nothing either way.
func (g *proxyGuard) observe(
	ctx context.Context,
	method string,
	req, reply any,
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	var md metadata.MD
	if err := invoker(ctx, method, req, reply, cc, append(opts, grpc.Header(&md))...); err != nil {
		return err
	}
	g.advertised.Store(binding.IsAdvertised(md))
	return nil
}
