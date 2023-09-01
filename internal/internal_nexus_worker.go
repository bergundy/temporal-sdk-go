package internal

import (
	"context"
	"fmt"
	"net/http"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	"github.com/nexus-rpc/sdk-go/nexus"
)

func (aw *AggregatedWorker) RegisterOperation(operation string, options OperationOptions) {
}

type OperationOptions interface {
	Start(context.Context, *StartOperationRequest, *AggregatedWorker) error
	Cancel(context.Context, *CancelOperationRequest, *AggregatedWorker) error
}

type StartOperationRequest struct {
	Operation   string
	RequestID   string
	CallbackURL string

	Header http.Header
	Body   []byte
}

type CancelOperationRequest struct {
	Operation   string
	OperationID string

	Header http.Header
}

func (r *StartOperationRequest) Payload() (*common.Payload, error) {
	panic("not implemented")
}

type StartWorkflowOperationRequest struct {
	StartWorkflowOptions
	WorkflowType string
	Input        any
}

type WorkflowRunOperation struct {
	GetStartRequest func(context.Context, *StartOperationRequest, converter.DataConverter) (*StartWorkflowOperationRequest, error)
}

func (o *WorkflowRunOperation) Start(ctx context.Context, request *StartOperationRequest, aw *AggregatedWorker) error {
	startRequest, err := o.GetStartRequest(ctx, request, aw.client.dataConverter)
	if err != nil {
		return err
	}
	// TODO: need a way to provide the request ID on start and we can't use the service client directly if we want eager workflow start.
	// TODO: add callback URL as well
	_, err = aw.client.ExecuteWorkflow(ctx, startRequest.StartWorkflowOptions, startRequest.WorkflowType, startRequest.Input)
	return err
}

func (o *WorkflowRunOperation) Cancel(ctx context.Context, request *CancelOperationRequest, aw *AggregatedWorker) error {
	// TODO: do we care about a run ID?
	return aw.client.CancelWorkflow(ctx, request.OperationID, "")
}

func NewWorkflowRunOperation[T any](getStartRequest func(context.Context, T, *StartOperationRequest) (*StartWorkflowOperationRequest, error)) *WorkflowRunOperation {
	return &WorkflowRunOperation{
		GetStartRequest: func(ctx context.Context, request *StartOperationRequest, dc converter.DataConverter) (*StartWorkflowOperationRequest, error) {
			payload, err := request.Payload()
			if err != nil {
				// log actual error?
				return nil, &nexus.HandlerError{StatusCode: http.StatusBadRequest, Failure: &nexus.Failure{Message: "invalid request payload"}}
			}
			var t T
			if err := dc.FromPayload(payload, &t); err != nil {
				// log actual error?
				return nil, &nexus.HandlerError{StatusCode: http.StatusBadRequest, Failure: &nexus.Failure{Message: "invalid request payload"}}
			}
			return getStartRequest(ctx, t, request)
		},
	}
}

func t() {
	type Input struct {
		CellID string
		Stuff  int64
	}
	getTenantID := func(context.Context, *StartOperationRequest) (string, error) {
		return "", nil
	}

	var w *AggregatedWorker
	w.RegisterOperation("provision-cell", NewWorkflowRunOperation(func(ctx context.Context, input Input, request *StartOperationRequest) (*StartWorkflowOperationRequest, error) {
		tenantID, err := getTenantID(ctx, request)
		if err != nil {
			return nil, &nexus.HandlerError{StatusCode: http.StatusUnauthorized, Failure: &nexus.Failure{Message: "unauthorized access"}}
		}

		return &StartWorkflowOperationRequest{
			Input:        input,
			WorkflowType: "provision-cell",
			StartWorkflowOptions: StartWorkflowOptions{
				ID: fmt.Sprintf("provision-cell-%s-%s", tenantID, input.CellID),
			},
		}, nil
	}))
}
