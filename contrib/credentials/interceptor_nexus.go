package credentials

import (
	"context"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/contrib/credentials/binding"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporalnexus"
)

func (o *outer) InterceptNexusOperation(_ context.Context, next interceptor.NexusOperationInboundInterceptor) interceptor.NexusOperationInboundInterceptor {
	i := &nexusInbound{}
	i.Next = next
	return i
}

type nexusInbound struct {
	interceptor.NexusOperationInboundInterceptorBase
}

// StartOperation reads the credentials the start request carried. Cancel
// requests carry none.
func (n *nexusInbound) StartOperation(ctx context.Context, input interceptor.NexusStartOperationInput) (nexus.HandlerStartOperationResult[any], error) {
	o, ok := binding.OpenedFromNexus(input.Options.Header)
	c := callFromOpened(callNexus, o, ok)
	if c.reason != "" {
		temporalnexus.GetLogger(ctx).Warn("credentials: the Nexus request's credentials header didn't open; treating its credentials as missing",
			"reason", c.reason)
	}
	return n.Next.StartOperation(context.WithValue(ctx, callKey{}, c), input)
}
