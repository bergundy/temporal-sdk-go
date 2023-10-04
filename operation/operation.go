package operation

import (
	"context"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/shared"
)

type Operation[I any, O any] interface {
	GetName() string
	io(I, O)
}

type VoidOperation[I any] interface {
	Operation[I, struct{}]
}

type Handler interface {
	nexus.Handler

	GetResultMapper() func(context.Context, any, error) (any, error)
}

type OperationHandler[I any, O any] interface {
	Operation[I, O]
	Handler
}

type VoidOperationHandler[I any] interface {
	VoidOperation[I]
	Handler
}

type AsyncResult[R any] interface {
	GetOperationID() string
	GetResult(context.Context, *nexus.GetOperationResultRequest) (R, error)

	private()
}

type AsyncOperation[I any, O any, ARO AsyncResult[O]] struct {
	nexus.UnimplementedHandler

	Name  string
	Start func(context.Context, I) (ARO, error)
}

func NewAsyncOperation[I any, O any, ARO AsyncResult[O]](name string, start func(context.Context, I) (ARO, error)) *AsyncOperation[I, O, ARO] {
	return &AsyncOperation[I, O, ARO]{
		Name:  name,
		Start: start,
	}
}

// GetResultMapper implements Handler.
func (*AsyncOperation[I, O, ARO]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// GetName implements Operation.
func (h *AsyncOperation[I, O, ARO]) GetName() string {
	return h.Name
}

// call implements Operation.
func (*AsyncOperation[I, O, ARO]) io(I, O) {}

var _ OperationHandler[any, any] = (*AsyncOperation[any, any, AsyncResult[any]])(nil)

type AsyncMappedOperation[I any, M any, O any, ARM AsyncResult[M]] struct {
	nexus.UnimplementedHandler

	Name         string
	Start        func(context.Context, I) (ARM, error)
	ResultMapper func(context.Context, M, error) (O, error)
}

func NewAsyncMappedOperation[I any, M any, O any, ARM AsyncResult[M]](name string, start func(context.Context, I) (ARM, error), mapper func(context.Context, M, error) (O, error)) OperationHandler[I, O] {
	return &AsyncMappedOperation[I, M, O, ARM]{
		Name:         name,
		Start:        start,
		ResultMapper: mapper,
	}
}

func WithResultMapper[I, M, O any, ARM AsyncResult[M]](op *AsyncOperation[I, M, ARM], mapper func(context.Context, M, error) (O, error)) *AsyncMappedOperation[I, M, O, ARM] {
	return &AsyncMappedOperation[I, M, O, ARM]{
		Name:         op.Name,
		Start:        op.Start,
		ResultMapper: mapper,
	}
}

// GetResultMapper implements Handler.
func (h *AsyncMappedOperation[I, M, O, ARM]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return func(ctx context.Context, a any, err error) (any, error) {
		var m M
		return h.ResultMapper(ctx, m, err)
	}
}

// GetName implements Operation.
func (h *AsyncMappedOperation[I, M, O, ARM]) GetName() string {
	return h.Name
}

// call implements Operation.
func (*AsyncMappedOperation[I, M, O, ARM]) io(I, O) {}

var _ OperationHandler[any, any] = (*AsyncMappedOperation[any, any, any, AsyncResult[any]])(nil)

type SyncClientOperation[I any, O any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, I, client.Client) (O, error)
}

func NewSyncClientOperation[I any, O any](name string, handler func(context.Context, I, client.Client) (O, error)) *SyncClientOperation[I, O] {
	return &SyncClientOperation[I, O]{
		Name:    name,
		Handler: handler,
	}
}

// GetResultMapper implements OperationHandler.
func (*SyncClientOperation[I, O]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// io implements Operation.
func (*SyncClientOperation[I, O]) io(I, O) {}

// GetName implements Operation.
func (h *SyncClientOperation[I, O]) GetName() string {
	return h.Name
}

// StartOperation implements Handler.
func (*SyncClientOperation[I, O]) StartOperation(context.Context, *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	panic("unimplemented")
}

var _ OperationHandler[any, any] = (*SyncClientOperation[any, any])(nil)

type VoidClientOperation[I any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, I, client.Client) error
}

func NewVoidClientOperation[I any](name string, handler func(context.Context, I, client.Client) error) VoidOperationHandler[I] {
	return &VoidClientOperation[I]{
		Name:    name,
		Handler: handler,
	}
}

// GetResultMapper implements OperationHandler.
func (*VoidClientOperation[I]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// io implements Operation.
func (*VoidClientOperation[I]) io(I, struct{}) {}

// GetName implements Operation.
func (h *VoidClientOperation[I]) GetName() string {
	return h.Name
}

var _ VoidOperationHandler[any] = (*VoidClientOperation[any])(nil)

type WorkflowHandle[R any] struct {
	WorkflowID string
	RunID      string
}

func (h *WorkflowHandle[R]) GetOperationID() string {
	return h.WorkflowID
}

func (*WorkflowHandle[R]) GetResult(ctx context.Context, request *nexus.GetOperationResultRequest) (R, error) {
	run := getContext(ctx).client.GetWorkflow(ctx, request.OperationID, "")
	// TODO: if request.Wait {}
	var ret R
	err := run.Get(ctx, &ret)
	return ret, err
}

func (*WorkflowHandle[O]) private() {
	panic("unimplemented")
}

var _ AsyncResult[any] = (*WorkflowHandle[any])(nil)

// type AsyncStarter[I, O any] interface {
// 	ExecuteWorkflow(context.Context, client.StartWorkflowOptions, func(shared.Context, I) (O, error), I) (*WorkflowHandle[O], error)
// }

func ExecuteWorkflow[I, O any, WF func(shared.Context, I) (O, error)](ctx context.Context, options client.StartWorkflowOptions, workflow WF, arg I) (*WorkflowHandle[O], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := getContext(ctx).client.ExecuteWorkflow(ctx, options, workflow, arg)
	return &WorkflowHandle[O]{run.GetID(), run.GetRunID()}, err
}

func ExecuteUntypedWorkflow[R any](ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (*WorkflowHandle[R], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := getContext(ctx).client.ExecuteWorkflow(ctx, options, workflow, args...)
	return &WorkflowHandle[R]{run.GetID(), run.GetRunID()}, err
}

func getContext(ctx context.Context) *operationContext {
	if cx, ok := ctx.Value(contextKeyOperationState).(*operationContext); ok {
		return cx
	}
	// Panic?
	return nil
}

type Error interface {
	error
	mustImplementErrorBase()
}

type unauthorizedError struct {
}

func (unauthorizedError) Error() string {
	return "TODO"
}

func (unauthorizedError) mustImplementErrorBase() {}

func UnauthorizedError(format string, args ...any) Error {
	return unauthorizedError{}
}

type operationContext struct {
	client                    client.Client
	dataConverter             converter.DataConverter
	startOperationRequest     *nexus.StartOperationRequest
	cancelOperationRequest    *nexus.CancelOperationRequest
	getOperationResultRequest *nexus.GetOperationResultRequest
	getOperationInfoRequest   *nexus.GetOperationInfoRequest
}

var contextKeyOperationState = struct{}{}
