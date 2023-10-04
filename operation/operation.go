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

type WorkflowOperation[I, O any] struct {
	nexus.UnimplementedHandler

	Name     string
	Workflow func(shared.Context, I) (O, error)
	Options  func(context.Context, I) client.StartWorkflowOptions
	Start    func(context.Context, client.Client, I) (*WorkflowHandle[O], error)
}

func NewWorkflowOperation[I, O any](name string, start func(context.Context, client.Client, I) (*WorkflowHandle[O], error)) *WorkflowOperation[I, O] {
	return &WorkflowOperation[I, O]{
		Name:  name,
		Start: start,
	}
}

// GetResultMapper implements Handler.
func (*WorkflowOperation[I, O]) GetResultMapper() func(context.Context, any, error) (any, error) {
	return nil
}

// GetName implements Operation.
func (h *WorkflowOperation[I, O]) GetName() string {
	return h.Name
}

// call implements Operation.
func (*WorkflowOperation[I, O]) io(I, O) {}

var _ OperationHandler[any, any] = (*WorkflowOperation[any, any])(nil)

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

type SyncOperation[I any, O any] struct {
	nexus.UnimplementedHandler

	Name    string
	Handler func(context.Context, client.Client, I) (O, error)
}

func NewSyncOperation[I any, O any](name string, handler func(context.Context, client.Client, I) (O, error)) *SyncOperation[I, O] {
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

func GetClient(ctx context.Context) client.Client {
	return getContext(ctx).client
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
