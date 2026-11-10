package credentials

import (
	"context"

	"github.com/nexus-rpc/sdk-go/nexus"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/credentials/binding"
	"go.temporal.io/sdk/interceptor"
)

// outer is the propagation interceptor the plugin puts first in the client's
// interceptors. On the client, it writes attached credentials into outgoing
// headers. Every Worker made from the client inherits it as its outermost
// Worker interceptor, so it reads incoming headers before other interceptors
// run.
type outer struct {
	interceptor.InterceptorBase
}

func (o *outer) InterceptClient(next interceptor.ClientOutboundInterceptor) interceptor.ClientOutboundInterceptor {
	i := &clientOutbound{}
	i.Next = next
	return i
}

type clientOutbound struct {
	interceptor.ClientOutboundInterceptorBase
}

// attach writes the innermost attached credentials into header, replacing any
// credentials fields already there.
func attach(ctx context.Context, header map[string]*commonpb.Payload) {
	if header == nil {
		return
	}
	clearHeader(header)
	a, ok := ctx.Value(attachKey{}).(attachment)
	if !ok || len(a.values) == 0 {
		return
	}
	header[binding.HeaderCredentials] = binding.PendingPayload(a.values)
	if a.key != "" {
		header[binding.HeaderKey] = binding.KeyPayload(a.key)
	}
}

func attachNexus(ctx context.Context, header nexus.Header) {
	if header == nil {
		return
	}
	clearNexusHeader(header)
	a, ok := ctx.Value(attachKey{}).(attachment)
	if !ok || len(a.values) == 0 {
		return
	}
	header[binding.HeaderCredentials] = binding.PendingNexus(a.values)
	if a.key != "" {
		header[binding.HeaderKey] = binding.KeyNexus(a.key)
	}
}

func clearHeader(header map[string]*commonpb.Payload) {
	delete(header, binding.HeaderCredentials)
	delete(header, binding.HeaderKey)
	delete(header, binding.HeaderOpened)
}

func clearNexusHeader(header nexus.Header) {
	for k := range header {
		switch k {
		case binding.HeaderCredentials, binding.HeaderKey, binding.HeaderOpened:
			delete(header, k)
		}
	}
}

func (c *clientOutbound) ExecuteWorkflow(ctx context.Context, in *interceptor.ClientExecuteWorkflowInput) (client.WorkflowRun, error) {
	attach(ctx, interceptor.Header(ctx))
	return c.Next.ExecuteWorkflow(ctx, in)
}

func (c *clientOutbound) SignalWorkflow(ctx context.Context, in *interceptor.ClientSignalWorkflowInput) error {
	attach(ctx, interceptor.Header(ctx))
	return c.Next.SignalWorkflow(ctx, in)
}

func (c *clientOutbound) SignalWithStartWorkflow(ctx context.Context, in *interceptor.ClientSignalWithStartWorkflowInput) (client.WorkflowRun, error) {
	attach(ctx, interceptor.Header(ctx))
	return c.Next.SignalWithStartWorkflow(ctx, in)
}

func (c *clientOutbound) UpdateWorkflow(ctx context.Context, in *interceptor.ClientUpdateWorkflowInput) (client.WorkflowUpdateHandle, error) {
	attach(ctx, interceptor.Header(ctx))
	return c.Next.UpdateWorkflow(ctx, in)
}

// UpdateWithStartWorkflow writes one pending value; the start and the Update
// share the header, and the binding layer seals each to its own context.
func (c *clientOutbound) UpdateWithStartWorkflow(ctx context.Context, in *interceptor.ClientUpdateWithStartWorkflowInput) (client.WorkflowUpdateHandle, error) {
	attach(ctx, interceptor.Header(ctx))
	return c.Next.UpdateWithStartWorkflow(ctx, in)
}

func (c *clientOutbound) ExecuteActivity(ctx context.Context, in *interceptor.ClientExecuteActivityInput) (client.ActivityHandle, error) {
	attach(ctx, interceptor.Header(ctx))
	return c.Next.ExecuteActivity(ctx, in)
}

func (c *clientOutbound) ExecuteNexusOperation(ctx context.Context, in *interceptor.ClientExecuteNexusOperationInput) (client.NexusOperationHandle, error) {
	attachNexus(ctx, in.NexusHeader)
	return c.Next.ExecuteNexusOperation(ctx, in)
}
