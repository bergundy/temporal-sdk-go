package internal

import (
	"context"

	"go.temporal.io/sdk/converter"
)

// func[I any, R any](context.Context, i I) (R, error)

type operationContext struct {
	client        Client
	dataConverter converter.DataConverter
	// TODO: should support any request type
	request StartOperationRequest
}

var contextKeyOperationState = struct{}{}

type WorkflowRunOperation[I any, R any] struct {
	Name         string
	IDMapper     func(I) string
	ResultMapper func(any, error) (R, error)
	Start        func(context.Context, I) (WorkflowRun, error)
}

func ExecuteWorkflow(ctx context.Context, options StartWorkflowOptions, workflow any, args ...any) (WorkflowRun, error) {
	// Override callback URL and request ID
	// Extract header to use in visibility scope
	return getContext(ctx).client.ExecuteWorkflow(ctx, options, workflow, args...)
}

func QueryWorkflow(ctx context.Context, workflowID string, runID string, queryType string, args ...any) (converter.EncodedValue, error) {
	return getContext(ctx).client.QueryWorkflow(ctx, workflowID, runID, queryType, args...)
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
