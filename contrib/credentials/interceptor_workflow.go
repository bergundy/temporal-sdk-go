package credentials

import (
	"maps"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/contrib/credentials/binding"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
)

func (o *outer) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	i := &workflowInbound{run: newRunState()}
	i.Next = next
	return i
}

// workflowInbound reads the credentials each call into a Workflow carried.
// There's one per run, holding the run's state, which it puts on each
// handler's context: an Update's context comes from the root context, not
// from the Workflow function's.
type workflowInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	run *runState
}

func (w *workflowInbound) Init(outbound interceptor.WorkflowOutboundInterceptor) error {
	o := &workflowOutbound{}
	o.Next = outbound
	return w.Next.Init(o)
}

// readCall reads the opened credentials from the current call's header. A
// failure marker counts as missing; logReason logs its reason.
func readCall(ctx workflow.Context, kind callKind, logReason bool) *call {
	o, ok := binding.OpenedFromHeader(interceptor.WorkflowHeader(ctx))
	c := callFromOpened(kind, o, ok)
	if logReason && c.reason != "" {
		workflow.GetLogger(ctx).Warn("credentials: the call's credentials header didn't open; treating its credentials as missing",
			"reason", c.reason)
	}
	return c
}

func (w *workflowInbound) withCall(ctx workflow.Context, c *call) workflow.Context {
	ctx = workflow.WithValue(ctx, runKey{}, w.run)
	return workflow.WithValue(ctx, callKey{}, c)
}

func (w *workflowInbound) ExecuteWorkflow(ctx workflow.Context, in *interceptor.ExecuteWorkflowInput) (any, error) {
	c := readCall(ctx, callStart, true)
	w.run.held = maps.Clone(c.creds)
	return w.Next.ExecuteWorkflow(w.withCall(ctx, c), in)
}

func (w *workflowInbound) HandleSignal(ctx workflow.Context, in *interceptor.HandleSignalInput) error {
	return w.Next.HandleSignal(w.withCall(ctx, readCall(ctx, callSignal, true)), in)
}

func (w *workflowInbound) HandleQuery(ctx workflow.Context, in *interceptor.HandleQueryInput) (any, error) {
	return w.Next.HandleQuery(workflow.WithValue(ctx, runKey{}, w.run), in)
}

func (w *workflowInbound) ValidateUpdate(ctx workflow.Context, in *interceptor.UpdateInput) error {
	return w.Next.ValidateUpdate(w.withCall(ctx, readCall(ctx, callUpdate, true)), in)
}

// ExecuteUpdate reads the header again: validators don't run on replay. Its
// failure, if any, was logged by the validator.
func (w *workflowInbound) ExecuteUpdate(ctx workflow.Context, in *interceptor.UpdateInput) (any, error) {
	return w.Next.ExecuteUpdate(w.withCall(ctx, readCall(ctx, callUpdate, false)), in)
}

// workflowOutbound replaces the credentials of every call a Workflow makes
// with the ones its context selects with Propagate, PropagateAll, or
// PropagateWithKey, or none.
type workflowOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
}

// selected resolves the credentials ctx selects.
func selected(ctx workflow.Context) (map[string]string, string, bool) {
	sel, ok := ctx.Value(selectionKey{}).(selection)
	if !ok {
		return nil, "", false
	}
	available := map[string]Credential{}
	if run := runOf(ctx); run != nil {
		maps.Copy(available, run.held)
	} else {
		workflow.GetLogger(ctx).Warn("credentials: no credentials state on this context; is the credentials plugin installed?")
	}
	if c := callOf(ctx); c != nil && c.kind == callUpdate {
		maps.Copy(available, c.creds)
	}
	values := map[string]string{}
	if sel.all {
		for name, cred := range available {
			values[name] = cred.value
		}
	}
	for _, name := range sel.names {
		cred, ok := available[name]
		if !ok {
			workflow.GetLogger(ctx).Warn("credentials: not forwarding a credential the Workflow doesn't have", "credential", name)
			continue
		}
		values[name] = cred.value
	}
	if len(values) == 0 {
		return nil, "", false
	}
	return values, sel.key, true
}

func propagate(ctx workflow.Context, header map[string]*commonpb.Payload) {
	if header == nil {
		return
	}
	clearHeader(header)
	values, key, ok := selected(ctx)
	if !ok {
		return
	}
	header[binding.HeaderCredentials] = binding.PendingPayload(values)
	if key != "" {
		header[binding.HeaderKey] = binding.KeyPayload(key)
	}
}

func (o *workflowOutbound) ExecuteActivity(ctx workflow.Context, activityType string, args ...any) workflow.Future {
	propagate(ctx, interceptor.WorkflowHeader(ctx))
	return o.Next.ExecuteActivity(ctx, activityType, args...)
}

// ExecuteLocalActivity passes credentials in the local form: Local Activities
// run in the Worker, and their headers never reach the wire.
func (o *workflowOutbound) ExecuteLocalActivity(ctx workflow.Context, activityType string, args ...any) workflow.Future {
	if header := interceptor.WorkflowHeader(ctx); header != nil {
		clearHeader(header)
		if values, _, ok := selected(ctx); ok {
			header[binding.HeaderCredentials] = binding.LocalPayload(values)
		}
	}
	return o.Next.ExecuteLocalActivity(ctx, activityType, args...)
}

func (o *workflowOutbound) ExecuteChildWorkflow(ctx workflow.Context, childWorkflowType string, args ...any) workflow.ChildWorkflowFuture {
	propagate(ctx, interceptor.WorkflowHeader(ctx))
	return o.Next.ExecuteChildWorkflow(ctx, childWorkflowType, args...)
}

func (o *workflowOutbound) SignalExternalWorkflow(ctx workflow.Context, workflowID, runID, signalName string, arg any) workflow.Future {
	propagate(ctx, interceptor.WorkflowHeader(ctx))
	return o.Next.SignalExternalWorkflow(ctx, workflowID, runID, signalName, arg)
}

func (o *workflowOutbound) SignalExternalWorkflowWithOptions(
	ctx workflow.Context, workflowID, runID, signalName string, arg any, options workflow.SignalExternalWorkflowOptions,
) workflow.Future {
	propagate(ctx, interceptor.WorkflowHeader(ctx))
	return o.Next.SignalExternalWorkflowWithOptions(ctx, workflowID, runID, signalName, arg, options)
}

func (o *workflowOutbound) SignalChildWorkflow(ctx workflow.Context, workflowID, signalName string, arg any) workflow.Future {
	propagate(ctx, interceptor.WorkflowHeader(ctx))
	return o.Next.SignalChildWorkflow(ctx, workflowID, signalName, arg)
}

func (o *workflowOutbound) SignalChildWorkflowWithOptions(
	ctx workflow.Context, workflowID, signalName string, arg any, options workflow.SignalChildWorkflowOptions,
) workflow.Future {
	propagate(ctx, interceptor.WorkflowHeader(ctx))
	return o.Next.SignalChildWorkflowWithOptions(ctx, workflowID, signalName, arg, options)
}

func (o *workflowOutbound) NewContinueAsNewError(ctx workflow.Context, wfn any, args ...any) error {
	propagate(ctx, interceptor.WorkflowHeader(ctx))
	return o.Next.NewContinueAsNewError(ctx, wfn, args...)
}

func (o *workflowOutbound) NewContinueAsNewErrorWithOptions(
	ctx workflow.Context, options workflow.ContinueAsNewErrorOptions, wfn any, args ...any,
) error {
	propagate(ctx, interceptor.WorkflowHeader(ctx))
	return o.Next.NewContinueAsNewErrorWithOptions(ctx, options, wfn, args...)
}

func (o *workflowOutbound) ExecuteNexusOperation(ctx workflow.Context, input interceptor.ExecuteNexusOperationInput) workflow.NexusOperationFuture {
	if input.NexusHeader != nil {
		clearNexusHeader(input.NexusHeader)
		if values, key, ok := selected(ctx); ok {
			input.NexusHeader[binding.HeaderCredentials] = binding.PendingNexus(values)
			if key != "" {
				input.NexusHeader[binding.HeaderKey] = binding.KeyNexus(key)
			}
		}
	}
	return o.Next.ExecuteNexusOperation(ctx, input)
}

// signalCounter is the innermost Workflow interceptor. It counts the Signals
// that reach each channel and records the credentials each carried, so
// RetainFromSignal can find the one just received. A Signal that an
// interceptor ahead of it drops is never counted.
type signalCounter struct {
	interceptor.WorkerInterceptorBase
}

func (s *signalCounter) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	i := &signalCounterInbound{}
	i.Next = next
	return i
}

type signalCounterInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}

func (s *signalCounterInbound) HandleSignal(ctx workflow.Context, in *interceptor.HandleSignalInput) error {
	run := runOf(ctx)
	if run == nil {
		return s.Next.HandleSignal(ctx, in)
	}
	name := in.SignalName
	// Every coroutine is blocked while events are processed, so values
	// numbered up to delivered - Len have been received and checked. The
	// channel is known once RetainFromSignal has seen it: HandleSignal runs
	// outside any coroutine, where GetSignalChannel can't be called.
	if c, ok := run.channels[name]; ok {
		run.prune(name, run.delivered[name]-c.Len())
	}
	if err := s.Next.HandleSignal(ctx, in); err != nil {
		return err
	}
	run.delivered[name]++
	if c := callOf(ctx); c != nil && len(c.creds) > 0 {
		if run.signals[name] == nil {
			run.signals[name] = map[int]map[string]Credential{}
		}
		run.signals[name][run.delivered[name]] = c.creds
	}
	return nil
}
