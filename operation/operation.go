package operation

import (
	"context"
	"net/http"
	"net/url"

	"github.com/nexus-rpc/sdk-go/nexus"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/shared"
)

type NoResult interface{}

type Operation[I, O any] interface {
	GetName() string
	io(I, O)
}

// OperationHandler is a handler for a single operation.
type OperationHandler[I, O any] interface {
	Operation[I, O]
	nexus.Handler
}

type WorkflowRunOperationIDBinding int

const (
	WorkflowRunOperationIDBindingWorkflowID WorkflowRunOperationIDBinding = iota
	WorkflowRunOperationIDBindingRunID
)

type WorkflowRun[I, O any] struct {
	nexus.UnimplementedHandler

	OperationIDBinding WorkflowRunOperationIDBinding

	Name  string
	Start func(context.Context, client.Client, I) (WorkflowHandle[O], error)

	// TODO: consider removing this
	Workflow   func(shared.Context, I) (O, error)
	GetOptions func(context.Context, I) (client.StartWorkflowOptions, error)
}

func NewWorkflowRun[I, O any](name string, start func(context.Context, client.Client, I) (WorkflowHandle[O], error)) *WorkflowRun[I, O] {
	return &WorkflowRun[I, O]{
		Name:  name,
		Start: start,
	}
}

// GetName implements Operation.
func (h *WorkflowRun[I, O]) GetName() string {
	return h.Name
}

func (h *WorkflowRun[I, O]) StartOperation(ctx context.Context, request *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	payload, err := payloadFromHTTP(request.HTTPRequest)
	if err != nil {
		// log actual error?
		return nil, NewBadRequestError("invalid request payload")
	}

	var i I
	dc := getContext(ctx).dataConverter
	if err := dc.FromPayload(payload, &i); err != nil {
		// log actual error?
		return nil, NewBadRequestError("invalid request payload")
	}

	handle, err := h.Start(ctx, getContext(ctx).client, i)
	if err != nil {
		return nil, err
	}
	return &nexus.OperationResponseAsync{OperationID: handle.GetID()}, nil
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

func (h *MappedResultHandler[I, M, O]) StartOperation(ctx context.Context, request *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	if next := request.HTTPRequest.URL.Query().Get("next"); next != "" {
		if request.HTTPRequest.URL.Query().Get("token") != "TODO" {
			// someone's trying to use us a relay.
			return nil, NewBadRequestError("next query param not supported")
		}
		httpReq, err := http.NewRequestWithContext(ctx, request.HTTPRequest.Method, next, request.HTTPRequest.Body)
		if err != nil {
			// TODO: non-retryable
			return nil, err
		}
		// TODO: make this configurable
		response, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			// TODO: consider dropping after a few attempts, for now internal server error seems fine
			return nil, err
		}
		return &nexus.OperationResponseSync{Body: response.Body, Header: response.Header}, nil
	}
	u := *request.HTTPRequest.URL
	var q url.Values
	q.Add("next", request.CallbackURL)
	q.Add("token", "TODO")
	u.RawQuery = q.Encode()
	request.CallbackURL = u.String()
	return h.Handler.StartOperation(ctx, request)
}

func (h *MappedResultHandler[I, M, O]) GetOperationResult(ctx context.Context, request *nexus.GetOperationResultRequest) (*nexus.OperationResponseSync, error) {
	response, err := h.Handler.GetOperationResult(ctx, request)
	// var m M
	// return h.ResultMapper(ctx, m, err)
	return response, err
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

// io implements Operation.
func (*Sync[I, O]) io(I, O) {}

// GetName implements Operation.
func (h *Sync[I, O]) GetName() string {
	return h.Name
}

// StartOperation implements Handler.
func (h *Sync[I, O]) StartOperation(ctx context.Context, request *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	payload, err := payloadFromHTTP(request.HTTPRequest)
	if err != nil {
		// log actual error?
		return nil, NewBadRequestError("invalid request payload")
	}

	var i I
	dc := getContext(ctx).dataConverter
	if err := dc.FromPayload(payload, &i); err != nil {
		// log actual error?
		return nil, NewBadRequestError("invalid request payload")
	}

	o, err := h.Handler(ctx, getContext(ctx).client, i)
	if err != nil {
		return nil, err
	}

	// TODO: support more payloads
	return nexus.NewOperationResponseSync(o)
}

var _ OperationHandler[any, any] = (*Sync[any, any])(nil)

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

// io implements Operation.
func (*VoidOperation[I]) io(I, NoResult) {}

// GetName implements Operation.
func (h *VoidOperation[I]) GetName() string {
	return h.Name
}

var _ OperationHandler[any, NoResult] = (*VoidOperation[any])(nil)

type WorkflowHandle[T any] interface {
	GetID() string
	GetRunID() string
}

type workflowHandle[T any] struct {
	id    string
	runID string
}

func (h workflowHandle[T]) GetID() string {
	return h.id
}

func (h workflowHandle[T]) GetRunID() string {
	return h.runID
}

func StartWorkflow[I, O any, WF func(shared.Context, I) (O, error)](ctx context.Context, c client.Client, options client.StartWorkflowOptions, workflow WF, arg I) (WorkflowHandle[O], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := c.ExecuteWorkflow(ctx, options, workflow, arg)
	return workflowHandle[O]{id: run.GetID()}, err
}

func StartUntypedWorkflow[R any](ctx context.Context, c client.Client, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (WorkflowHandle[R], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := c.ExecuteWorkflow(ctx, options, workflow, args...)
	return workflowHandle[R]{id: run.GetID()}, err
}

func getContext(ctx context.Context) *operationContext {
	if cx, ok := ctx.Value(contextKeyOperationState).(*operationContext); ok {
		return cx
	}
	// Panic?
	return nil
}

type operationContext struct {
	client        client.Client
	dataConverter converter.DataConverter
}

var contextKeyOperationState = struct{}{}

func payloadFromHTTP(request *http.Request) (*commonpb.Payload, error) {
	panic("TODO")
}
