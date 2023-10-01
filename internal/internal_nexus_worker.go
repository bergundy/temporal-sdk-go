package internal

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/converter"

	"github.com/nexus-rpc/sdk-go/nexus"
)

func (aw *AggregatedWorker) RegisterOperation(operation string, handler OperationHandler) {
}

type OperationHandler interface {
	StartOperation(context.Context, *StartOperationRequest) (OperationResponse, error)
	CancelOperation(context.Context, *CancelOperationRequest) error
}

type OperationResponse interface {
	// TODO
}

type StartOperationRequest struct {
	Operation   string
	RequestID   string
	CallbackURL string

	URL    *url.URL
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

type StartWorkflowOperationOptions struct {
	StartWorkflowOptions
	WorkflowType string
	Input        []any
}

type QueryWorkflowOperationOptions struct {
	WorkflowID           string
	QueryType            string
	QueryRejectCondition enums.QueryRejectCondition
	Input                any
}

type WorkflowRunOperationHandler struct {
	StartWorkflow func(context.Context, *StartOperationRequest) (WorkflowRun, error)
}

func NewWorkflowRunOperation[T any](getOptions func(context.Context, T, *StartOperationRequest) (*StartWorkflowOperationOptions, error)) *WorkflowRunOperationHandler {
	return &WorkflowRunOperationHandler{
		StartWorkflow: func(ctx context.Context, request *StartOperationRequest) (WorkflowRun, error) {
			payload, err := request.Payload()
			if err != nil {
				// log actual error?
				return nil, &nexus.HandlerError{StatusCode: http.StatusBadRequest, Failure: &nexus.Failure{Message: "invalid request payload"}}
			}
			var t T
			dc := GetDataConverter(ctx)
			if err := dc.FromPayload(payload, &t); err != nil {
				// log actual error?
				return nil, &nexus.HandlerError{StatusCode: http.StatusBadRequest, Failure: &nexus.Failure{Message: "invalid request payload"}}
			}
			options, err := getOptions(ctx, t, request)
			if err != nil {
				return nil, err
			}
			client := GetClient(ctx)
			if err != nil {
				return nil, err
			}
			// TODO: need a way to provide the request ID on start and we can't use the service client directly if we want eager workflow start.
			// TODO: add callback URL as well, when supported.
			return client.ExecuteWorkflow(ctx, options.StartWorkflowOptions, options.WorkflowType, options.Input)
		},
	}
}

func (o *WorkflowRunOperationHandler) StartOperation(ctx context.Context, request *StartOperationRequest) (OperationResponse, error) {
	_, err := o.StartWorkflow(ctx, request)
	// TODO: return async with operation ID
	return nil, err
}

func (o *WorkflowRunOperationHandler) CancelOperation(ctx context.Context, request *CancelOperationRequest) error {
	client := GetClient(ctx)
	// NOTE: Do not provide a run ID to allow the workflow to continue as new and retry.
	return client.CancelWorkflow(ctx, request.OperationID, "")
}

type SyncOperationHandler struct {
	Run func(context.Context, *StartOperationRequest) (any, error)
}

func NewSyncOperationHandler[I any, R any](handle func(context.Context, I, *StartOperationRequest) (R, error)) *SyncOperationHandler {
	return &SyncOperationHandler{
		Run: func(ctx context.Context, request *StartOperationRequest) (any, error) {
			payload, err := request.Payload()
			if err != nil {
				// log actual error?
				return nil, &nexus.HandlerError{StatusCode: http.StatusBadRequest, Failure: &nexus.Failure{Message: "invalid request payload"}}
			}
			var i I
			dc := GetDataConverter(ctx)
			if err := dc.FromPayload(payload, &i); err != nil {
				// log actual error?
				return nil, &nexus.HandlerError{StatusCode: http.StatusBadRequest, Failure: &nexus.Failure{Message: "invalid request payload"}}
			}
			return handle(ctx, i, request)
		},
	}
}

func (o *SyncOperationHandler) StartOperation(ctx context.Context, request *StartOperationRequest) (OperationResponse, error) {
	// client := GetClient(ctx)
	ret, err := o.Run(ctx, request)
	if err != nil {
		// TODO: payload and stuff
		return nexus.NewOperationResponseSync(ret)
	}
	return nil, err
}

func (o *SyncOperationHandler) CancelOperation(ctx context.Context, request *CancelOperationRequest) error {
	return &nexus.HandlerError{StatusCode: http.StatusNotImplemented}
}

type operationContextKey int

const (
	contextKeyClient operationContextKey = iota
	contextKeyDataConverter
)

func GetClient(ctx context.Context) Client {
	if client, ok := ctx.Value(contextKeyClient).(Client); ok {
		return client
	}
	// Panic?
	return nil
}

func GetDataConverter(ctx context.Context) converter.DataConverter {
	if client, ok := ctx.Value(contextKeyDataConverter).(converter.DataConverter); ok {
		return client
	}
	// Panic?
	return nil
}

func getTenantID(context.Context, http.Header) (string, error) {
	return "", nil
}

type OperationAuthorizationInterceptor struct {
	OperationInboundInterceptorBase
}

func (*OperationAuthorizationInterceptor) CancelOperation(ctx context.Context, request *CancelOperationRequest) error {
	tenantID, err := getTenantID(ctx, request.Header)
	if err != nil {
		return &nexus.HandlerError{StatusCode: http.StatusUnauthorized, Failure: &nexus.Failure{Message: "unauthorized access"}}
	}
	if !strings.HasPrefix(request.OperationID, fmt.Sprintf("%s-%s-", request.Operation, tenantID)) {
		return &nexus.HandlerError{StatusCode: http.StatusUnauthorized, Failure: &nexus.Failure{Message: "unauthorized access"}}
	}
	return nil
}

func t() {
	type Input struct {
		CellID string
		Stuff  int64
	}
	type MyOutput struct {
	}
	var w *AggregatedWorker
	w.RegisterOperation("provision-cell", NewWorkflowRunOperation(
		func(ctx context.Context, input Input, request *StartOperationRequest) (*StartWorkflowOperationOptions, error) {
			tenantID, err := getTenantID(ctx, request.Header)
			if err != nil {
				return nil, &nexus.HandlerError{StatusCode: http.StatusUnauthorized, Failure: &nexus.Failure{Message: "unauthorized access"}}
			}

			return &StartWorkflowOperationOptions{
				Input:        []any{input},
				WorkflowType: "provision-cell",
				StartWorkflowOptions: StartWorkflowOptions{
					ID: fmt.Sprintf("%s-%s-%s", request.Operation, tenantID, input.CellID),
				},
			}, nil
		}))
	w.RegisterOperation("get-cell-status", NewSyncOperationHandler(
		func(ctx context.Context, input Input, request *StartOperationRequest) (*MyOutput, error) {
			c := GetClient(ctx)
			payload, _ := c.QueryWorkflow(ctx, fmt.Sprintf("%s-%s", "provision-cell", input.CellID), "", "get-cell-status")
			var output *MyOutput
			return output, payload.Get(&output)
		}))
}
