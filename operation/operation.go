package operation

import (
	"context"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

type Operation[I any, R any] interface {
	GetName() string
}

type WorkflowRunHandler[I any, R any] struct {
	nexus.UnimplementedHandler

	Name         string
	IDMapper     func(I) string
	ResultMapper func(context.Context, any, error) (R, error)
	Start        func(context.Context, I) (WorkflowRun, error)
}

// CancelOperation implements Handler.
func (*WorkflowRunHandler[I, R]) CancelOperation(context.Context, *nexus.CancelOperationRequest) error {
	panic("unimplemented")
}

// GetOperationInfo implements Handler.
func (*WorkflowRunHandler[I, R]) GetOperationInfo(context.Context, *nexus.GetOperationInfoRequest) (*nexus.OperationInfo, error) {
	panic("unimplemented")
}

// GetOperationResult implements Handler.
func (*WorkflowRunHandler[I, R]) GetOperationResult(context.Context, *nexus.GetOperationResultRequest) (*nexus.OperationResponseSync, error) {
	panic("unimplemented")
}

// StartOperation implements Handler.
func (*WorkflowRunHandler[I, R]) StartOperation(context.Context, *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	panic("unimplemented")
}

// GetName implements Operation.
func (h *WorkflowRunHandler[I, R]) GetName() string {
	return h.Name
}

var _ nexus.Handler = (*WorkflowRunHandler[any, any])(nil)
var _ Operation[any, any] = (*WorkflowRunHandler[any, any])(nil)

type SyncHandler[I any, R any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, I, client.Client) (R, error)
}

func NewSyncHandler[I any, R any](name string, handler func(context.Context, I, client.Client) (R, error)) *SyncHandler[I, R] {
	return &SyncHandler[I, R]{
		Name:    name,
		Handler: handler,
	}
}

// GetName implements Operation.
func (h *SyncHandler[I, R]) GetName() string {
	return h.Name
}

// StartOperation implements Handler.
func (*SyncHandler[I, R]) StartOperation(context.Context, *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	panic("unimplemented")
}

type VoidHandler[I any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, I, client.Client) error
}

// GetName implements Operation.
func (h *VoidHandler[I]) GetName() string {
	return h.Name
}

func NewVoidHandler[I any](name string, handler func(context.Context, I, client.Client) error) *VoidHandler[I] {
	return &VoidHandler[I]{
		Name:    name,
		Handler: handler,
	}
}

// StartOperation implements Handler.
func (*VoidHandler[I]) StartOperation(context.Context, *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	panic("unimplemented")
}

var _ nexus.Handler = (*WorkflowRunHandler[any, any])(nil)
var _ Operation[any, any] = (*WorkflowRunHandler[any, any])(nil)

type WorkflowRun struct {
	client.WorkflowRun
}

func ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (WorkflowRun, error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := getContext(ctx).client.ExecuteWorkflow(ctx, options, workflow, args...)
	return WorkflowRun{run}, err
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
	client        client.Client
	dataConverter converter.DataConverter
	// TODO: should support any request type
	request *nexus.StartOperationRequest
}

var contextKeyOperationState = struct{}{}

// func WorkflowRunOperation[I any, R any](name string, handler func(context.Context, I) (WorkflowHandle[R], error)) nexus.Handler {
// 	panic("not implemented")
// }

// func WorkflowRunOperationWithResultMapper[I any, R any, M any](name string, handler func(context.Context, I) (WorkflowHandle[M], error), mapper func(context.Context, M, error) (R, error)) nexus.Handler {
// 	panic("not implemented")
// }

// // TODO
// func WorkflowRunOperationWithPayloadMapper[I any, R any](name string, handler func(context.Context, I) (WorkflowHandle[any], error), mapper func(context.Context, *commonpb.Payload, error) (R, error)) nexus.Handler {
// 	panic("not implemented")
// }

// func WorkflowRunIDMapper[T any](func(context.Context, T) string) nexus.Handler {
// 	panic("not implemented")
// }

// type WorkflowHandle[T any] struct {
// 	ID    string
// 	RunID string
// }

// func newWorkflowHandle[T any](r client.WorkflowRun) *WorkflowHandle[T] {
// 	panic("")
// }

// func (h *WorkflowHandle[T]) Get(context.Context) (T, error) {
// 	panic("")
// }

// func (h *WorkflowHandle[T]) GetWithOptions(ctx context.Context, valuePtr interface{}, options client.WorkflowRunGetOptions) error {
// 	panic("")
// }

// func ExecuteWorkflow[I any, R any](ctx context.Context, options client.StartWorkflowOptions, workflow func(workflow.Context, I) (R, error), arg I) (*WorkflowHandle[R], error) {
// 	// Override callback URL and request ID
// 	// Extract header to use in visibility scope
// 	run, err := getContext(ctx).client.ExecuteWorkflow(ctx, options, workflow, arg)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return newWorkflowHandle[R](run), err
// }
