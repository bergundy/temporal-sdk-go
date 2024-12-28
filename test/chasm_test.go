package test_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/chasm"
)

func TestStartActivity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	client, err := chasm.NewClient(chasm.ClientOptions{
		Address:   "localhost:7243",
		Namespace: "default",
	})
	require.NoError(t, err)
	handle, err := client.Activity().Start(ctx, t.Name(), chasm.StartActivityOptions{
		StartToCloseTimeout: time.Minute,
	})
	require.NoError(t, err)
	result, err := handle.Result(ctx)
	require.NoError(t, err)
	var output string
	require.NoError(t, result.Output(&output))
	require.Equal(t, "success", output)
}
