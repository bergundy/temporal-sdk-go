package internal

import (
	"net/http"
	"net/url"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/api/common/v1"
)

func (aw *AggregatedWorker) RegisterOperation(nexus.Handler) {
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

// func NewWorkflowRunOperation[T any](getOptions func(context.Context, T, *StartOperationRequest) (*StartWorkflowOperationOptions, error)) *WorkflowRunOperationHandler {
// 	return &WorkflowRunOperationHandler{
// 		StartWorkflow: func(ctx context.Context, request *StartOperationRequest) (WorkflowRun, error) {
// 			payload, err := request.Payload()
// 			if err != nil {
// 				// log actual error?
// 				return nil, &nexus.HandlerError{StatusCode: http.StatusBadRequest, Failure: &nexus.Failure{Message: "invalid request payload"}}
// 			}
// 			var t T
// 			dc := GetDataConverter(ctx)
// 			if err := dc.FromPayload(payload, &t); err != nil {
// 				// log actual error?
// 				return nil, &nexus.HandlerError{StatusCode: http.StatusBadRequest, Failure: &nexus.Failure{Message: "invalid request payload"}}
// 			}
// 			options, err := getOptions(ctx, t, request)
// 			if err != nil {
// 				return nil, err
// 			}
// 			client := GetClient(ctx)
// 			if err != nil {
// 				return nil, err
// 			}
// 			// TODO: need a way to provide the request ID on start and we can't use the service client directly if we want eager workflow start.
// 			// TODO: add callback URL as well, when supported.
// 			return client.ExecuteWorkflow(ctx, options.StartWorkflowOptions, options.WorkflowType, options.Input)
// 		},
// 	}
// }

// func NewSyncOperationHandler[I any, R any](handle func(context.Context, I, *StartOperationRequest) (R, error)) *SyncOperationHandler {
// 	return &SyncOperationHandler{
// 		Run: func(ctx context.Context, request *StartOperationRequest) (any, error) {
// 			payload, err := request.Payload()
// 			if err != nil {
// 				// log actual error?
// 				return nil, &nexus.HandlerError{StatusCode: http.StatusBadRequest, Failure: &nexus.Failure{Message: "invalid request payload"}}
// 			}
// 			var i I
// 			dc := GetDataConverter(ctx)
// 			if err := dc.FromPayload(payload, &i); err != nil {
// 				// log actual error?
// 				return nil, &nexus.HandlerError{StatusCode: http.StatusBadRequest, Failure: &nexus.Failure{Message: "invalid request payload"}}
// 			}
// 			return handle(ctx, i, request)
// 		},
// 	}
// }

// func (o *SyncOperationHandler) StartOperation(ctx context.Context, request *StartOperationRequest) (OperationResponse, error) {
// 	// client := GetClient(ctx)
// 	ret, err := o.Run(ctx, request)
// 	if err != nil {
// 		// TODO: payload and stuff
// 		return nexus.NewOperationResponseSync(ret)
// 	}
// 	return nil, err
// }

// func (o *SyncOperationHandler) CancelOperation(ctx context.Context, request *CancelOperationRequest) error {
// 	return &nexus.HandlerError{StatusCode: http.StatusNotImplemented}
// }
