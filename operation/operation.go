package operation

import (
	"context"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/shared"
)

type NoResult struct{}

type Operation[I, O any] interface {
	GetName() string
	io(I, O)
}

type Handler interface {
	nexus.Handler

	GetResultMapper() func(context.Context, any, error) (any, error)
}

type OperationHandler[I, O any] interface {
	Operation[I, O]
	Handler
}

type WorkflowRun[I, O any] struct {
	nexus.UnimplementedHandler

	Name       string
	Workflow   func(shared.Context, I) (O, error)
	GetOptions func(context.Context, I) (client.StartWorkflowOptions, error)
	Start      func(context.Context, client.Client, I) (*WorkflowHandle[O], error)
}

func NewWorkflowRun[I, O any](name string, start func(context.Context, client.Client, I) (*WorkflowHandle[O], error)) *WorkflowRun[I, O] {
	return &WorkflowRun[I, O]{
		Name:  name,
		Start: start,
	}
}

// GetResultMapper implements Handler.
func (*WorkflowRun[I, O]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// GetName implements Operation.
func (h *WorkflowRun[I, O]) GetName() string {
	return h.Name
}

// call implements Operation.
func (*WorkflowRun[I, O]) io(I, O) {}

var _ OperationHandler[any, any] = (*WorkflowRun[any, any])(nil)

type MappedResultHandler[I, M, O any] struct {
	nexus.UnimplementedHandler
	Handler      OperationHandler[I, M]
	ResultMapper func(context.Context, M, error) (O, error)
}

func WithResultMapper[I, M, O any](op OperationHandler[I, M], mapper func(context.Context, M, error) (O, error)) *MappedResultHandler[I, M, O] {
	return &MappedResultHandler[I, M, O]{
		Handler:      op,
		ResultMapper: mapper,
	}
}

// GetResultMapper implements Handler.
func (h *MappedResultHandler[I, M, O]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return func(ctx context.Context, a any, err error) (any, error) {
		var m M
		return h.ResultMapper(ctx, m, err)
	}
}

// GetName implements Operation.
func (h *MappedResultHandler[I, M, O]) GetName() string {
	return h.Handler.GetName()
}

// call implements Operation.
func (*MappedResultHandler[I, M, O]) io(I, O) {}

var _ OperationHandler[any, any] = (*MappedResultHandler[any, any, any])(nil)

type Sync[I any, O any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, client.Client, I) (O, error)
}

func NewSync[I any, O any](name string, handler func(context.Context, client.Client, I) (O, error)) *Sync[I, O] {
	return &Sync[I, O]{
		Name:    name,
		Handler: handler,
	}
}

// GetResultMapper implements Handler.
func (*Sync[I, O]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// io implements Operation.
func (*Sync[I, O]) io(I, O) {}

// GetName implements Operation.
func (h *Sync[I, O]) GetName() string {
	return h.Name
}

// StartOperation implements Handler.
func (*Sync[I, O]) StartOperation(context.Context, *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	panic("unimplemented")
}

var _ Operation[any, any] = (*Sync[any, any])(nil)
var _ Handler = (*Sync[any, any])(nil)

type VoidOperation[I any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, client.Client, I) error
}

func NewVoidOperation[I any](name string, handler func(context.Context, client.Client, I) error) *VoidOperation[I] {
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
	ID string
}

func StartWorkflow[I, O any, WF func(shared.Context, I) (O, error)](ctx context.Context, c client.Client, options client.StartWorkflowOptions, workflow WF, arg I) (*WorkflowHandle[O], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := c.ExecuteWorkflow(ctx, options, workflow, arg)
	return &WorkflowHandle[O]{ID: run.GetID()}, err
}

func StartUntypedWorkflow[R any](ctx context.Context, c client.Client, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (*WorkflowHandle[R], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := c.ExecuteWorkflow(ctx, options, workflow, args...)
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
