package operation

import (
	"context"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/shared"
)

type NoResult struct{}

type Operation[I any, O any] interface {
	GetName() string
	io(I, O)
}

type Handler interface {
	nexus.Handler

	GetResultMapper() func(context.Context, any, error) (any, error)
}

type AsyncResult[R any] interface {
	// Implementors must implement the entire handler API apart from StartOperation.
	GetOperationResult(context.Context, *nexus.GetOperationResultRequest) (R, error)
	GetOperationInfo(context.Context, *nexus.GetOperationInfoRequest) (*nexus.OperationInfo, error)
	CancelOperation(context.Context, *nexus.CancelOperationRequest) error

	GetOperationID() string
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

var _ Operation[any, any] = (*AsyncOperation[any, any, AsyncResult[any]])(nil)
var _ Handler = (*AsyncOperation[any, any, AsyncResult[any]])(nil)

type AsyncOperationWithResultMapper[I any, M any, O any, ARM AsyncResult[M]] struct {
	nexus.UnimplementedHandler

	Name         string
	Start        func(context.Context, I) (ARM, error)
	ResultMapper func(context.Context, M, error) (O, error)
}

func NewAsyncOperationWithResultMapper[I any, M any, O any, ARM AsyncResult[M]](name string, start func(context.Context, I) (ARM, error), mapper func(context.Context, M, error) (O, error)) *AsyncOperationWithResultMapper[I, M, O, ARM] {
	return &AsyncOperationWithResultMapper[I, M, O, ARM]{
		Name:         name,
		Start:        start,
		ResultMapper: mapper,
	}
}

func WithResultMapper[I, M, O any, ARM AsyncResult[M]](op *AsyncOperation[I, M, ARM], mapper func(context.Context, M, error) (O, error)) *AsyncOperationWithResultMapper[I, M, O, ARM] {
	return &AsyncOperationWithResultMapper[I, M, O, ARM]{
		Name:         op.Name,
		Start:        op.Start,
		ResultMapper: mapper,
	}
}

// GetResultMapper implements Handler.
func (h *AsyncOperationWithResultMapper[I, M, O, ARM]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return func(ctx context.Context, a any, err error) (any, error) {
		var m M
		return h.ResultMapper(ctx, m, err)
	}
}

// GetName implements Operation.
func (h *AsyncOperationWithResultMapper[I, M, O, ARM]) GetName() string {
	return h.Name
}

// call implements Operation.
func (*AsyncOperationWithResultMapper[I, M, O, ARM]) io(I, O) {}

var _ Operation[any, any] = (*AsyncOperationWithResultMapper[any, any, any, AsyncResult[any]])(nil)
var _ Handler = (*AsyncOperationWithResultMapper[any, any, any, AsyncResult[any]])(nil)

type SyncOperation[I any, O any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, I) (O, error)
}

func NewSyncOperation[I any, O any](name string, handler func(context.Context, I) (O, error)) *SyncOperation[I, O] {
	return &SyncOperation[I, O]{
		Name:    name,
		Handler: handler,
	}
}

// GetResultMapper implements Handler.
func (*SyncOperation[I, O]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// io implements Operation.
func (*SyncOperation[I, O]) io(I, O) {}

// GetName implements Operation.
func (h *SyncOperation[I, O]) GetName() string {
	return h.Name
}

// StartOperation implements Handler.
func (*SyncOperation[I, O]) StartOperation(context.Context, *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	panic("unimplemented")
}

var _ Operation[any, any] = (*SyncOperation[any, any])(nil)
var _ Handler = (*SyncOperation[any, any])(nil)

type VoidOperation[I any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, I) error
}

func NewVoidOperation[I any](name string, handler func(context.Context, I) error) *VoidOperation[I] {
	return &VoidOperation[I]{
		Name:    name,
		Handler: handler,
	}
}

// GetResultMapper implements Handler.
func (*VoidOperation[I]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// io implements Operation.
func (*VoidOperation[I]) io(I, NoResult) {}

// GetName implements Operation.
func (h *VoidOperation[I]) GetName() string {
	return h.Name
}

var _ Operation[any, NoResult] = (*VoidOperation[any])(nil)
var _ Handler = (*VoidOperation[any])(nil)

type WorkflowHandle[R any] struct {
	nexus.UnimplementedHandler

	ID string
}

func (h *WorkflowHandle[R]) GetOperationID() string {
	return h.ID
}

func (h *WorkflowHandle[R]) GetOperationResult(ctx context.Context, request *nexus.GetOperationResultRequest) (R, error) {
	panic("TODO")
}

// GetOperationInfo implements the Handler interface.
func (h *WorkflowHandle[R]) GetOperationInfo(ctx context.Context, request *nexus.GetOperationInfoRequest) (*nexus.OperationInfo, error) {
	panic("TODO")
}

// CancelOperation implements the Handler interface.
func (h *WorkflowHandle[R]) CancelOperation(ctx context.Context, request *nexus.CancelOperationRequest) error {
	panic("TODO")
}

var _ AsyncResult[any] = (*WorkflowHandle[any])(nil)

func GetClient(ctx context.Context) client.Client {
	return getContext(ctx).client
}

func StartWorkflow[I, O any, WF func(shared.Context, I) (O, error)](ctx context.Context, options client.StartWorkflowOptions, workflow WF, arg I) (*WorkflowHandle[O], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := GetClient(ctx).ExecuteWorkflow(ctx, options, workflow, arg)
	return &WorkflowHandle[O]{ID: run.GetID()}, err
}

func StartUntypedWorkflow[R any](ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (*WorkflowHandle[R], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := GetClient(ctx).ExecuteWorkflow(ctx, options, workflow, args...)
	return &WorkflowHandle[R]{ID: run.GetID()}, err
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
