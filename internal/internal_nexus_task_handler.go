package internal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/nexus-rpc/sdk-go/nexus"
	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/workflowservice/v1"
)

func httpHeaderToProtoHeader(hh http.Header) map[string]*nexuspb.HeaderValues {
	ph := make(map[string]*nexuspb.HeaderValues, len(hh))
	for k, vs := range hh {
		ph[k] = &nexuspb.HeaderValues{Elements: vs}
	}
	return ph
}

func protoHeaderToHTTPHeader(ph map[string]*nexuspb.HeaderValues) http.Header {
	hh := make(http.Header, len(ph))
	for k, vs := range ph {
		hh[k] = vs.GetElements()
	}
	return hh
}

type contextKeyClient struct{}
type contextKeyTaskQueue struct{}

// TODO: this shouldn't be exposed here
func GetClient(ctx context.Context) Client {
	return ctx.Value(contextKeyClient{}).(Client)
}

func withClient(ctx context.Context, client Client) context.Context {
	return context.WithValue(ctx, contextKeyClient{}, client)
}

// TODO: this shouldn't be exposed here
func GetTaskQueue(ctx context.Context) string {
	return ctx.Value(contextKeyTaskQueue{}).(string)
}

func withTaskQueue(ctx context.Context, q string) context.Context {
	return context.WithValue(ctx, contextKeyTaskQueue{}, q)
}

// TODO: this should be deleted once nexus operation handlers can get this
type contextKeyStartOperationRequest struct{}

func withStartOperationRequest(ctx context.Context, request *nexus.StartOperationRequest) context.Context {
	return context.WithValue(ctx, contextKeyStartOperationRequest{}, request)
}

func GetStartOperationRequest(ctx context.Context) *nexus.StartOperationRequest {
	return ctx.Value(contextKeyStartOperationRequest{}).(*nexus.StartOperationRequest)
}

type nexusTaskHandler struct {
	serviceHandler *nexus.ServiceHandler
	baseResponse   workflowservice.RespondNexusTaskCompletedRequest
	client         Client
}

func (h *nexusTaskHandler) fillInCompletion(taskToken []byte, res *nexuspb.Response) *workflowservice.RespondNexusTaskCompletedRequest {
	// TODO:
	// Namespace: h.namespace,
	// Identity: h.identity,
	r := h.baseResponse
	r.TaskToken = taskToken
	r.Response = res
	return &r
}

func (h *nexusTaskHandler) internalServerError(taskToken []byte, err error) *workflowservice.RespondNexusTaskCompletedRequest {
	// TODO: log
	return h.fillInCompletion(taskToken, &nexuspb.Response{
		Variant: &nexuspb.Response_Error{
			Error: &nexuspb.HandlerError{
				StatusCode: http.StatusInternalServerError,
				Failure: &nexuspb.Failure{
					Message: "internal server error",
				},
			},
		},
	})
}

func (h *nexusTaskHandler) execute(taskQueue string, response *workflowservice.PollNexusTaskQueueResponse) (*workflowservice.RespondNexusTaskCompletedRequest, error) {
	httpReq := &http.Request{
		Header: protoHeaderToHTTPHeader(response.Request.Headers),
	}
	// TODO: timeout on context
	ctx := withTaskQueue(withClient(context.TODO(), h.client), taskQueue)
	switch req := response.Request.Variant.(type) {
	case *nexuspb.Request_StartOperation:
		httpReq.Body = io.NopCloser(bytes.NewReader(req.StartOperation.Body))
		// TODO: do we only want Content-* headers or some other convention?
		message := nexus.Message{Header: httpReq.Header, Body: httpReq.Body}
		startOp := nexus.StartOperationRequest{
			Operation:   req.StartOperation.Operation,
			RequestID:   req.StartOperation.RequestId,
			CallbackURL: req.StartOperation.Callback,
			Input:       &message,
			HTTPRequest: httpReq,
		}
		// TODO: this should move out of here
		ctx = withStartOperationRequest(ctx, &startOp)
		// TODO: capture panic (here or in Nexus SDK?)
		// TODO: pass in client somehow...
		opres, err := h.serviceHandler.StartOperation(ctx, &startOp)
		if err != nil {
			// TODO: unsuccesful operation
			return h.internalServerError(response.TaskToken, err), nil
		}
		// httpRes
		switch t := opres.(type) {
		case *nexus.OperationResponseAsync:
			pres := &nexuspb.Response{
				Variant: &nexuspb.Response_StartOperation{
					StartOperation: &nexuspb.StartOperationResponse{
						Variant: &nexuspb.StartOperationResponse_AsyncSuccess{
							AsyncSuccess: &nexuspb.StartOperationResponseAsync{OperationId: t.OperationID},
						},
					},
				},
			}
			return h.fillInCompletion(response.TaskToken, pres), nil
		case *nexus.OperationResponseSync:
			body, err := io.ReadAll(t.Message.Body)
			if err != nil {
				return h.internalServerError(response.TaskToken, err), nil
			}
			fmt.Println(t.Message.Header, body)
			return h.fillInCompletion(response.TaskToken, &nexuspb.Response{
				Variant: &nexuspb.Response_StartOperation{
					StartOperation: &nexuspb.StartOperationResponse{
						Variant: &nexuspb.StartOperationResponse_SyncSuccess{
							SyncSuccess: &nexuspb.StartOperationResponseSync{
								Payload: &nexuspb.Payload{
									Headers: httpHeaderToProtoHeader(t.Message.Header),
									Body:    body,
								},
							},
						},
					},
				},
			}), nil
		}
		return h.internalServerError(response.TaskToken, err), nil
	}
	panic("unknown request type")
}
