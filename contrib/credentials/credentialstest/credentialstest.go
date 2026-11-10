// Package credentialstest helps unit test code that uses credentials with
// the SDK's TestWorkflowEnvironment and TestActivityEnvironment, which have no
// client and no binding layer.
//
//	plugin := credentialstest.NewPlugin()
//	env := suite.NewTestWorkflowEnvironment()
//	env.SetWorkerOptions(worker.Options{Plugins: []worker.Plugin{plugin}})
//	env.SetHeader(credentialstest.StartHeader(credentials.Values{"user_token": token}))
//	env.ExecuteWorkflow(Agent, input)
package credentialstest

import (
	"context"
	"sync"

	"github.com/nexus-rpc/sdk-go/nexus"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/contrib/credentials"
	"go.temporal.io/sdk/contrib/credentials/binding"
	"go.temporal.io/sdk/contrib/credentials/internal/testhooks"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// placeholder stands in for sealed bytes. Nothing opens it: the opened result
// next to it is what the propagation layer reads.
const placeholder = binding.SealedPrefix + "credentialstest"

func opened(values credentials.Values) *commonpb.Payload {
	return binding.OpenedPayload(placeholder, binding.Opened{OK: true, Credentials: values})
}

// StartHeader returns a Workflow start header carrying values as the binding
// layer would deliver them, for TestWorkflowEnvironment.SetHeader.
func StartHeader(values credentials.Values) *commonpb.Header {
	return &commonpb.Header{Fields: map[string]*commonpb.Payload{binding.HeaderCredentials: opened(values)}}
}

// ActivityHeader returns an Activity header carrying values as the binding
// layer would deliver them, for TestActivityEnvironment.SetHeader.
func ActivityHeader(values credentials.Values) *commonpb.Header {
	return StartHeader(values)
}

// Plugin installs the credentials interceptors in a test environment and
// stands in for the binding layer. Credentials a Workflow forwards arrive
// opened at the Activity, child Workflow, or Nexus operation, so tests can
// assert what each receives. The test environment sends Signals and Updates
// without headers, so the credentials they carry are set with
// [Plugin.QueueSignal] and [Plugin.SetUpdate].
type Plugin struct {
	worker.PluginBase
	mu      sync.Mutex
	signals map[string][]credentials.Values
	updates map[string]credentials.Values
}

// NewPlugin returns a test plugin. Pass it in worker.Options.Plugins to
// TestWorkflowEnvironment.SetWorkerOptions or
// TestActivityEnvironment.SetWorkerOptions.
func NewPlugin() *Plugin {
	return &Plugin{signals: map[string][]credentials.Values{}, updates: map[string]credentials.Values{}}
}

// Name implements worker.Plugin.
func (p *Plugin) Name() string { return "credentialstest" }

// QueueSignal queues the credentials the next Signal named name to arrive
// carries. Signals arrive in the order they're sent, and each takes the next
// queued entry; a Signal with none queued carries no credentials.
func (p *Plugin) QueueSignal(name string, values credentials.Values) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signals[name] = append(p.signals[name], values)
}

func (p *Plugin) nextSignal(name string) (credentials.Values, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	queue := p.signals[name]
	if len(queue) == 0 {
		return nil, false
	}
	p.signals[name] = queue[1:]
	return queue[0], true
}

// SetUpdate sets the credentials the Update with ID updateID carries.
func (p *Plugin) SetUpdate(updateID string, values credentials.Values) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updates[updateID] = values
}

func (p *Plugin) update(id string) (credentials.Values, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.updates[id]
	return v, ok
}

// ConfigureWorker installs the loopback ahead of the propagation
// interceptors, which are first, and the Signal counter last.
func (p *Plugin) ConfigureWorker(_ context.Context, opts worker.PluginConfigureWorkerOptions) error {
	first, last := testhooks.Interceptors()
	wo := opts.WorkerOptions
	wo.Interceptors = append([]interceptor.WorkerInterceptor{&loopback{p: p}, first}, wo.Interceptors...)
	wo.Interceptors = append(wo.Interceptors, last)
	return nil
}

// loopback turns pending credentials into opened ones as each call arrives,
// as a binding layer would after sealing and opening them.
type loopback struct {
	interceptor.WorkerInterceptorBase
	p *Plugin
}

func openPending(header map[string]*commonpb.Payload) {
	if header == nil {
		return
	}
	delete(header, binding.HeaderKey)
	p, ok := header[binding.HeaderCredentials]
	if !ok || !binding.IsCredentialsPayload(p) || string(p.GetMetadata()["encoding"]) != binding.EncodingPending {
		return
	}
	values, _ := binding.ParseValues(p.GetData())
	header[binding.HeaderCredentials] = opened(values)
}

func inject(header map[string]*commonpb.Payload, values credentials.Values) {
	if header != nil {
		header[binding.HeaderCredentials] = opened(values)
	}
}

func (l *loopback) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	i := &loopbackWorkflow{p: l.p}
	i.Next = next
	return i
}

type loopbackWorkflow struct {
	interceptor.WorkflowInboundInterceptorBase
	p *Plugin
}

func (l *loopbackWorkflow) ExecuteWorkflow(ctx workflow.Context, in *interceptor.ExecuteWorkflowInput) (any, error) {
	openPending(interceptor.WorkflowHeader(ctx))
	return l.Next.ExecuteWorkflow(ctx, in)
}

func (l *loopbackWorkflow) HandleSignal(ctx workflow.Context, in *interceptor.HandleSignalInput) error {
	if values, ok := l.p.nextSignal(in.SignalName); ok {
		inject(interceptor.WorkflowHeader(ctx), values)
	} else {
		openPending(interceptor.WorkflowHeader(ctx))
	}
	return l.Next.HandleSignal(ctx, in)
}

func (l *loopbackWorkflow) update(ctx workflow.Context) {
	if values, ok := l.p.update(workflow.GetCurrentUpdateInfo(ctx).ID); ok {
		inject(interceptor.WorkflowHeader(ctx), values)
	}
}

func (l *loopbackWorkflow) ValidateUpdate(ctx workflow.Context, in *interceptor.UpdateInput) error {
	l.update(ctx)
	return l.Next.ValidateUpdate(ctx, in)
}

func (l *loopbackWorkflow) ExecuteUpdate(ctx workflow.Context, in *interceptor.UpdateInput) (any, error) {
	l.update(ctx)
	return l.Next.ExecuteUpdate(ctx, in)
}

func (l *loopback) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	i := &loopbackActivity{}
	i.Next = next
	return i
}

type loopbackActivity struct {
	interceptor.ActivityInboundInterceptorBase
}

func (l *loopbackActivity) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (any, error) {
	if !activity.GetInfo(ctx).IsLocalActivity {
		openPending(interceptor.Header(ctx))
	}
	return l.Next.ExecuteActivity(ctx, in)
}

func (l *loopback) InterceptNexusOperation(_ context.Context, next interceptor.NexusOperationInboundInterceptor) interceptor.NexusOperationInboundInterceptor {
	i := &loopbackNexus{}
	i.Next = next
	return i
}

type loopbackNexus struct {
	interceptor.NexusOperationInboundInterceptorBase
}

func (l *loopbackNexus) StartOperation(ctx context.Context, input interceptor.NexusStartOperationInput) (nexus.HandlerStartOperationResult[any], error) {
	if h := input.Options.Header; h != nil {
		delete(h, binding.HeaderKey)
		if pending, ok := h[binding.HeaderCredentials]; ok {
			values, _ := binding.ParseValues([]byte(pending))
			h[binding.HeaderCredentials] = placeholder
			h[binding.HeaderOpened] = string(binding.Opened{OK: true, Credentials: values}.Marshal())
		}
	}
	return l.Next.StartOperation(ctx, input)
}
