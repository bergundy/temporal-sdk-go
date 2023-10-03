package operation

import (
	"context"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
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

type asyncOperation[I any, O any, ARO AsyncResult[O]] struct {
	nexus.UnimplementedHandler

	Name  string
	Start func(context.Context, I) (ARO, error)
}

func AsyncOperation[I any, O any, ARO AsyncResult[O]](name string, start func(context.Context, I) (ARO, error)) OperationHandler[I, O] {
	return &asyncOperation[I, O, ARO]{
		Name:  name,
		Start: start,
	}
}

// GetResultMapper implements Handler.
func (*asyncOperation[I, O, ARO]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// GetName implements Operation.
func (h *asyncOperation[I, O, ARO]) GetName() string {
	return h.Name
}

// call implements Operation.
func (*asyncOperation[I, O, ARO]) io(I, O) {}

var _ OperationHandler[any, any] = (*asyncOperation[any, any, AsyncResult[any]])(nil)

type asyncMappedOperation[I any, M any, O any, ARM AsyncResult[M]] struct {
	nexus.UnimplementedHandler

	Name         string
	Start        func(context.Context, I) (ARM, error)
	ResultMapper func(context.Context, M, error) (O, error)
}

func AsyncMappedOperation[I any, M any, O any, ARM AsyncResult[M]](name string, start func(context.Context, I) (ARM, error), mapper func(context.Context, M, error) (O, error)) OperationHandler[I, O] {
	return &asyncMappedOperation[I, M, O, ARM]{
		Name:         name,
		Start:        start,
		ResultMapper: mapper,
	}
}

// GetResultMapper implements Handler.
func (h *asyncMappedOperation[I, M, O, ARM]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return func(ctx context.Context, a any, err error) (any, error) {
		var m M
		return h.ResultMapper(ctx, m, err)
	}
}

// GetName implements Operation.
func (h *asyncMappedOperation[I, M, O, ARM]) GetName() string {
	return h.Name
}

// call implements Operation.
func (*asyncMappedOperation[I, M, O, ARM]) io(I, O) {}

var _ OperationHandler[any, any] = (*asyncMappedOperation[any, any, any, AsyncResult[any]])(nil)

type clientOperation[I any, O any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, I, client.Client) (O, error)
}

func ClientOperation[I any, O any](name string, handler func(context.Context, I, client.Client) (O, error)) *clientOperation[I, O] {
	return &clientOperation[I, O]{
		Name:    name,
		Handler: handler,
	}
}

// GetResultMapper implements OperationHandler.
func (*clientOperation[I, O]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// io implements Operation.
func (*clientOperation[I, O]) io(I, O) {}

// GetName implements Operation.
func (h *clientOperation[I, O]) GetName() string {
	return h.Name
}

// StartOperation implements Handler.
func (*clientOperation[I, O]) StartOperation(context.Context, *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	panic("unimplemented")
}

var _ OperationHandler[any, any] = (*clientOperation[any, any])(nil)

type voidClientOperation[I any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, I, client.Client) error
}

func VoidClientOperation[I any](name string, handler func(context.Context, I, client.Client) error) VoidOperationHandler[I] {
	return &voidClientOperation[I]{
		Name:    name,
		Handler: handler,
	}
}

// GetResultMapper implements OperationHandler.
func (*voidClientOperation[I]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// io implements Operation.
func (*voidClientOperation[I]) io(I, struct{}) {}

// GetName implements Operation.
func (h *voidClientOperation[I]) GetName() string {
	return h.Name
}

var _ VoidOperationHandler[any] = (*voidClientOperation[any])(nil)

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

func ExecuteWorkflow[R any](ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (*WorkflowHandle[R], error) {
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
