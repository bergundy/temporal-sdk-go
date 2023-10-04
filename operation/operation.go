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

type AsyncResult[R any] interface {
	GetOperationID() string
	GetResult(context.Context, *nexus.GetOperationResultRequest) (R, error)

	private()
}

type AsyncHandler[I any, O any, ARO AsyncResult[O]] struct {
	nexus.UnimplementedHandler

	Name  string
	Start func(context.Context, I) (ARO, error)
}

func NewAsyncHandler[I any, O any, ARO AsyncResult[O]](name string, start func(context.Context, I) (ARO, error)) *AsyncHandler[I, O, ARO] {
	return &AsyncHandler[I, O, ARO]{
		Name:  name,
		Start: start,
	}
}

// GetResultMapper implements Handler.
func (*AsyncHandler[I, O, ARO]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// GetName implements Operation.
func (h *AsyncHandler[I, O, ARO]) GetName() string {
	return h.Name
}

// call implements Operation.
func (*AsyncHandler[I, O, ARO]) io(I, O) {}

var _ Operation[any, any] = (*AsyncHandler[any, any, AsyncResult[any]])(nil)
var _ Handler = (*AsyncHandler[any, any, AsyncResult[any]])(nil)

type AsyncHandlerWithResultMapper[I any, M any, O any, ARM AsyncResult[M]] struct {
	nexus.UnimplementedHandler

	Name         string
	Start        func(context.Context, I) (ARM, error)
	ResultMapper func(context.Context, M, error) (O, error)
}

func NewAsyncHandlerWithResultMapper[I any, M any, O any, ARM AsyncResult[M]](name string, start func(context.Context, I) (ARM, error), mapper func(context.Context, M, error) (O, error)) *AsyncHandlerWithResultMapper[I, M, O, ARM] {
	return &AsyncHandlerWithResultMapper[I, M, O, ARM]{
		Name:         name,
		Start:        start,
		ResultMapper: mapper,
	}
}

func WithResultMapper[I, M, O any, ARM AsyncResult[M]](op *AsyncHandler[I, M, ARM], mapper func(context.Context, M, error) (O, error)) *AsyncHandlerWithResultMapper[I, M, O, ARM] {
	return &AsyncHandlerWithResultMapper[I, M, O, ARM]{
		Name:         op.Name,
		Start:        op.Start,
		ResultMapper: mapper,
	}
}

// GetResultMapper implements Handler.
func (h *AsyncHandlerWithResultMapper[I, M, O, ARM]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return func(ctx context.Context, a any, err error) (any, error) {
		var m M
		return h.ResultMapper(ctx, m, err)
	}
}

// GetName implements Operation.
func (h *AsyncHandlerWithResultMapper[I, M, O, ARM]) GetName() string {
	return h.Name
}

// call implements Operation.
func (*AsyncHandlerWithResultMapper[I, M, O, ARM]) io(I, O) {}

var _ Operation[any, any] = (*AsyncHandlerWithResultMapper[any, any, any, AsyncResult[any]])(nil)
var _ Handler = (*AsyncHandlerWithResultMapper[any, any, any, AsyncResult[any]])(nil)

type SyncHandler[I any, O any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, I) (O, error)
}

func NewSyncHandler[I any, O any](name string, handler func(context.Context, I) (O, error)) *SyncHandler[I, O] {
	return &SyncHandler[I, O]{
		Name:    name,
		Handler: handler,
	}
}

// GetResultMapper implements Handler.
func (*SyncHandler[I, O]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// io implements Operation.
func (*SyncHandler[I, O]) io(I, O) {}

// GetName implements Operation.
func (h *SyncHandler[I, O]) GetName() string {
	return h.Name
}

// StartOperation implements Handler.
func (*SyncHandler[I, O]) StartOperation(context.Context, *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	panic("unimplemented")
}

var _ Operation[any, any] = (*SyncHandler[any, any])(nil)
var _ Handler = (*SyncHandler[any, any])(nil)

type VoidHandler[I any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, I) error
}

func NewVoidHandler[I any](name string, handler func(context.Context, I) error) *VoidHandler[I] {
	return &VoidHandler[I]{
		Name:    name,
		Handler: handler,
	}
}

// GetResultMapper implements Handler.
func (*VoidHandler[I]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// io implements Operation.
func (*VoidHandler[I]) io(I, struct{}) {}

// GetName implements Operation.
func (h *VoidHandler[I]) GetName() string {
	return h.Name
}

var _ VoidOperation[any] = (*VoidHandler[any])(nil)
var _ Handler = (*VoidHandler[any])(nil)

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

func GetClient(ctx context.Context) client.Client {
	return getContext(ctx).client
}

func StartWorkflow[I, O any, WF func(shared.Context, I) (O, error)](ctx context.Context, options client.StartWorkflowOptions, workflow WF, arg I) (*WorkflowHandle[O], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := GetClient(ctx).ExecuteWorkflow(ctx, options, workflow, arg)
	return &WorkflowHandle[O]{run.GetID(), run.GetRunID()}, err
}

func StartUntypedWorkflow[R any](ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (*WorkflowHandle[R], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := GetClient(ctx).ExecuteWorkflow(ctx, options, workflow, args...)
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
