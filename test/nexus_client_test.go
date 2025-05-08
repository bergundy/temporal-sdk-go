package test

import (
	"context"
	"testing"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/client"
)

func TestNexusClient(t *testing.T) {
	ctx := context.TODO()
	op := nexus.NewOperationReference[string, string]("my-op")

	c, _ := client.Dial(client.Options{Namespace: "my-namespace"})
	s := c.NexusClient(client.NexusClientOptions{Endpoint: "my-endpoint", Service: "my-service"})
	// Alternatively connect to the same namespace.
	_ = c.NexusClient(client.NexusClientOptions{TaskQueue: "my-task-queue", Service: "my-service"})

	var output string
	output, _ = client.ExecuteNexusOperation(ctx, s, op, "hello", nexus.ExecuteOperationOptions{
		// The following options can be provided:
		// Links
		// Header
		// Wait - time.Duration
		// RequestID
		// CallbackURL
		// CallbackHeader
	})
	res, _ := client.StartNexusOperation(ctx, s, op, "hello", nexus.StartOperationOptions{})
	// res.Links
	if handle := res.Pending; handle != nil {
		output, _ = handle.GetResult(ctx, nexus.GetOperationResultOptions{})
		resp, _ := handle.GetResultWithFullResponse(ctx, nexus.GetOperationResultOptions{})
		// resp.Links()
		output = resp.Result()
		_ = handle.Cancel(ctx, nexus.CancelOperationOptions{})
		info, _ := handle.GetInfo(ctx, nexus.GetOperationInfoOptions{})
		// info.State
	} else {
		output = res.Successful
	}

	// String based invocation supported without type inference, ExecuteOperation also available as a shorthand.
	lazyRes, _ := s.StartOperation(ctx, "my-op", "input", nexus.StartOperationOptions{})
	if handle := lazyRes.Pending; handle != nil {
		_, _ = client.NewHandle(s, op, handle.Token())
		// Get the service and operation names.
		_, _ = handle.Service(), handle.Operation()
	} else {
		_ = lazyRes.Successful.Consume(&output)
	}

	// await handle.cancel();
	// const { state } = await handle.getInfo();
}
