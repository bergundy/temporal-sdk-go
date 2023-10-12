package temporalnexus

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/nexus-rpc/sdk-go/nexus"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/internal"
)

type WorkflowRunOperationIDBinding int

const (
	WorkflowRunOperationIDBindingWorkflowID WorkflowRunOperationIDBinding = iota
	WorkflowRunOperationIDBindingRunID
)

type WorkflowRunOptions[I, O any] struct {
	nexus.UnimplementedHandler

	// TODO: this isn't implemented
	// May need this eventually
	OperationIDBinding WorkflowRunOperationIDBinding

	// TODO: consider just client.WorkflowRun
	Start func(context.Context, client.Client, I) (WorkflowHandle[O], error)

	// TODO: consider removing this
	Workflow   func(internal.Context, I) (O, error)
	GetOptions func(context.Context, I) (client.StartWorkflowOptions, error)
}

type workflowRunHandler[I, O any] struct {
	options WorkflowRunOptions[I, O]
}

// Cancel implements nexus.AsyncOperationHandler.
func (*workflowRunHandler[I, O]) Cancel(ctx context.Context, id string) error {
	// TODO: support run ID
	return internal.GetClient(ctx).CancelWorkflow(ctx, id, "")
}

// GetInfo implements nexus.AsyncOperationHandler.
func (*workflowRunHandler[I, O]) GetInfo(context.Context, string) (*nexus.OperationInfo, error) {
	panic("unimplemented")
}

// GetResult implements nexus.AsyncOperationHandler.
func (*workflowRunHandler[I, O]) GetResult(context.Context, string) (O, error) {
	panic("unimplemented")
}

// Start implements nexus.AsyncOperationHandler.
func (h *workflowRunHandler[I, O]) Start(ctx context.Context, input I) (*nexus.OperationResponseAsync, error) {
	handle, err := h.options.Start(ctx, internal.GetClient(ctx), input)
	if err != nil {
		return nil, err
	}
	return &nexus.OperationResponseAsync{OperationID: handle.GetID()}, nil
}

var _ nexus.AsyncOperationHandler[any, any] = &workflowRunHandler[any, any]{}

func NewWorkflowRunOperation[I, O any](name string, options WorkflowRunOptions[I, O]) *nexus.AsyncOperation[I, O, O] {
	return nexus.NewAsyncOperation(name, &workflowRunHandler[I, O]{options})
}

func NewSyncOperation[I any, O any](name string, handler func(context.Context, client.Client, I) (O, error)) *nexus.SyncOperation[I, O] {
	return nexus.NewSyncOperation[I, O](name, func(ctx context.Context, i I) (O, error) {
		return handler(ctx, internal.GetClient(ctx), i)
	})
}

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

func StartWorkflow[I, O any, WF func(internal.Context, I) (O, error)](ctx context.Context, c client.Client, options client.StartWorkflowOptions, workflow WF, arg I) (WorkflowHandle[O], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	req := internal.GetStartOperationRequest(ctx)
	options.RequestID = req.RequestID
	options.CallbackURL = req.CallbackURL
	options.TaskQueue = "my-task-queue" // TODO: need this on the context (or better yet on the service)
	run, err := c.ExecuteWorkflow(ctx, options, workflow, arg)
	if err != nil {
		fmt.Println("Start workflow error", err)
		return nil, err
	}
	return workflowHandle[O]{id: run.GetID()}, nil
}

func StartUntypedWorkflow[R any](ctx context.Context, c client.Client, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (WorkflowHandle[R], error) {
	// Override callback URL and request ID
	// Extract header to use in "visibility scope"
	run, err := c.ExecuteWorkflow(ctx, options, workflow, args...)
	return workflowHandle[R]{id: run.GetID()}, err
}

func httpToPayload(header http.Header, body io.Reader) (*commonpb.Payload, error) {
	panic("TODO")
}

func payloadToHTTP(*commonpb.Payload) (http.Header, io.Reader, error) {
	panic("TODO")
}
