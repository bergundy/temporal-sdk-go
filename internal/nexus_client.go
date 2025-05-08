package internal

import (
	"context"

	"github.com/nexus-rpc/sdk-go/nexus"
)

type NexusClientOptions struct {
	Endpoint  string
	TaskQueue string
	Service   string
}

// NexusClientStartOperationResult is the return type of [NexusClient.StartOperation].
// One and only one of Successful or Pending will be non-nil.
type NexusClientLazyStartOperationResult struct {
	// Set when start completes synchronously and successfully.
	//
	// If T is a [LazyValue], ensure that your consume it or read the underlying content in its entirety and close it to
	// free up the underlying connection.
	Successful *nexus.LazyValue
	// Set when the handler indicates that it started an asynchronous operation.
	// The attached handle can be used to perform actions such as cancel the operation or get its result.
	Pending NexusOperationHandle[*nexus.LazyValue]
	// Links contain information about the operations done by the handler.
	Links []nexus.Link
}

type NexusClient interface {
	StartOperation(
		ctx context.Context,
		operation string,
		input any,
		options nexus.StartOperationOptions,
	) (*NexusClientLazyStartOperationResult, error)
	ExecuteOperation(ctx context.Context, operation string, input any, options nexus.ExecuteOperationOptions) (*nexus.LazyValue, error)
	NewHandle(operation string, token string) (NexusOperationHandle[*nexus.LazyValue], error)
}

type NexusResponse[T any] interface {
	Links() []nexus.Link
	Result() T
}

// An OperationHandle is used to cancel operations and get their result and status.
type NexusOperationHandle[T any] interface {
	// Name of the Service this handle represents.
	Service() string
	// Name of the Operation this handle represents.
	Operation() string
	// Handler generated token for this handle's operation.
	Token() string

	// GetInfo gets operation information, issuing a network request to the service handler.
	GetInfo(ctx context.Context, options nexus.GetOperationInfoOptions) (*nexus.OperationInfo, error)
	GetResult(ctx context.Context, options nexus.GetOperationResultOptions) (T, error)
	GetResultWithFullResponse(ctx context.Context, options nexus.GetOperationResultOptions) (NexusResponse[T], error)
	Cancel(ctx context.Context, options nexus.CancelOperationOptions) error
}
