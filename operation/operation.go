package operation

import (
	"context"
	"io"
	"net/http"

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

type MapCompletionRequest struct {
	Context    map[string]string
	Completion nexus.OperationCompletion
}

// OperationHandler is a handler for a single operation.
type OperationHandler[I, O any] interface {
	Operation[I, O]
	nexus.Handler
	// TODO: this should be interceptable
	MapCompletion(context.Context, *MapCompletionRequest) (nexus.OperationCompletion, error)
}

type unimplementedHandler struct {
	nexus.Handler
}

func (*unimplementedHandler) MapCompletion(context.Context, *MapCompletionRequest) (nexus.OperationCompletion, error) {
	return nil, NewNotFoundError("cannot map completion")
}

type WorkflowRunOperationIDBinding int

const (
	WorkflowRunOperationIDBindingWorkflowID WorkflowRunOperationIDBinding = iota
	WorkflowRunOperationIDBindingRunID
)

type FailureVerbosity int

const (
	FailureVerbosityInternal FailureVerbosity = iota
	FailureVerbosityExternal
)

type WorkflowRun[I, O any] struct {
	unimplementedHandler

	OperationIDBinding WorkflowRunOperationIDBinding
	FailureVerbosity   FailureVerbosity

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
	payload, err := httpToPayload(request.HTTPRequest.Header, request.HTTPRequest.Body)
	if err != nil {
		// log actual error?
		return nil, NewBadRequestError("invalid request payload")
	}

	var i I
	dc := GetDataConverter(ctx)
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

type MappedCompletionHandler[I, M, O any] struct {
	unimplementedHandler
	Handler       OperationHandler[I, M]
	ResultMapper  func(context.Context, M) (O, error)
	FailureMapper func(context.Context, *nexus.Failure) (*nexus.Failure, error)
}

func WithResultMapper[I, M, O any](op OperationHandler[I, M], mapper func(context.Context, M) (O, error)) *MappedCompletionHandler[I, M, O] {
	return &MappedCompletionHandler[I, M, O]{
		Handler:      op,
		ResultMapper: mapper,
	}
}

func WithFailureMapper[I, M, O any](op OperationHandler[I, M], mapper func(context.Context, *nexus.Failure) (*nexus.Failure, error)) *MappedCompletionHandler[I, M, O] {
	return &MappedCompletionHandler[I, M, O]{
		Handler:       op,
		FailureMapper: mapper,
	}
}

func (h *MappedCompletionHandler[I, M, O]) MapCompletion(ctx context.Context, request *MapCompletionRequest) (nexus.OperationCompletion, error) {
	switch t := request.Completion.(type) {
	case *nexus.OperationCompletionSuccessful:
		payload, err := httpToPayload(t.Header, t.Body)
		if err != nil {
			// log actual error?
			return nil, NewBadRequestError("invalid request payload")
		}
		dc := GetDataConverter(ctx)
		var i M
		err = dc.FromPayload(payload, &i)
		if err != nil {
			// log actual error?
			return nil, NewBadRequestError("invalid request payload")
		}
		o, err := h.ResultMapper(ctx, i)
		if err != nil {
			return nil, err
		}
		// TODO: how would this encrypt based on the context?
		payload, err = dc.ToPayload(o)
		if err != nil {
			return nil, err
		}

		header, body, err := payloadToHTTP(payload)
		if err != nil {
			return nil, err
		}
		return &nexus.OperationCompletionSuccessful{Header: header, Body: body}, nil
	case *nexus.OperationCompletionUnsuccessful:
		failure, err := h.FailureMapper(ctx, t.Failure)
		if err != nil {
			return nil, err
		}
		return &nexus.OperationCompletionUnsuccessful{Header: t.Header, State: t.State, Failure: failure}, nil
	}
	panic("unreachable")
}

func (h *MappedCompletionHandler[I, M, O]) StartOperation(ctx context.Context, request *nexus.StartOperationRequest) (nexus.OperationResponse, error) {
	c := getContext(ctx)
	c.requiresResultMapping = c.requiresResultMapping || h.ResultMapper != nil
	c.requiresFailureMapping = c.requiresFailureMapping || h.FailureMapper != nil
	return h.Handler.StartOperation(ctx, request)
}

func (h *MappedCompletionHandler[I, M, O]) GetOperationResult(ctx context.Context, request *nexus.GetOperationResultRequest) (*nexus.OperationResponseSync, error) {
	response, err := h.Handler.GetOperationResult(ctx, request)
	// var m M
	// return h.ResultMapper(ctx, m, err)
	return response, err
}

// GetName implements Operation.
func (h *MappedCompletionHandler[I, M, O]) GetName() string {
	return h.Handler.GetName()
}

// call implements Operation.
func (*MappedCompletionHandler[I, M, O]) io(I, O) {}

var _ OperationHandler[any, any] = (*MappedCompletionHandler[any, any, any])(nil)

type Sync[I any, O any] struct {
	unimplementedHandler

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
	payload, err := httpToPayload(request.HTTPRequest.Header, request.HTTPRequest.Body)
	if err != nil {
		// log actual error?
		return nil, NewBadRequestError("invalid request payload")
	}

	var i I
	dc := GetDataConverter(ctx)
	if err := dc.FromPayload(payload, &i); err != nil {
		// log actual error?
		return nil, NewBadRequestError("invalid request payload")
	}

	o, err := h.Handler(ctx, getContext(ctx).client, i)
	if err != nil {
		return nil, err
	}

	payload, err = dc.ToPayload(o)
	header, body, err := payloadToHTTP(payload)
	if err != nil {
		return nil, err
	}
	return &nexus.OperationResponseSync{Header: header, Body: body}, nil
}

var _ OperationHandler[any, any] = (*Sync[any, any])(nil)

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
	if cx, ok := ctx.Value(contextKeyOperationState{}).(*operationContext); ok {
		return cx
	}
	// Panic?
	return nil
}

type operationContext struct {
	client                 client.Client
	dataConverter          converter.DataConverter
	requiresResultMapping  bool
	requiresFailureMapping bool
}

func GetDataConverter(ctx context.Context) converter.DataConverter {
	return getContext(ctx).dataConverter
}

func WithDataConverter(ctx context.Context, conv converter.DataConverter) context.Context {
	c := getContext(ctx)
	cc := *c
	cc.dataConverter = conv
	return context.WithValue(ctx, contextKeyOperationState{}, &cc)
}

type contextKeyOperationState = struct{}

func httpToPayload(header http.Header, body io.Reader) (*commonpb.Payload, error) {
	panic("TODO")
}

func payloadToHTTP(*commonpb.Payload) (http.Header, io.Reader, error) {
	panic("TODO")
}

type encryptionPayloadCodec struct {
	key string
}

// Decode implements converter.PayloadCodec.
func (*encryptionPayloadCodec) Decode([]*commonpb.Payload) ([]*commonpb.Payload, error) {
	panic("unimplemented")
}

// Encode implements converter.PayloadCodec.
func (*encryptionPayloadCodec) Encode([]*commonpb.Payload) ([]*commonpb.Payload, error) {
	panic("unimplemented")
}

var _ converter.PayloadCodec = &encryptionPayloadCodec{}

type decodeOnlyCodec struct {
	inner converter.PayloadCodec
}

// Decode implements converter.PayloadCodec.
func (c *decodeOnlyCodec) Decode(ps []*commonpb.Payload) ([]*commonpb.Payload, error) {
	return c.inner.Decode(ps)
}

// Encode implements converter.PayloadCodec.
func (*decodeOnlyCodec) Encode(ps []*commonpb.Payload) ([]*commonpb.Payload, error) {
	return ps, nil
}

var _ converter.PayloadCodec = &decodeOnlyCodec{}
