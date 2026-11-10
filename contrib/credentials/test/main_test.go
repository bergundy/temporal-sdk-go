package credentials_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

const namespace = "default"

// hostPort is the dev server every test shares.
var hostPort string

func TestMain(m *testing.M) {
	if os.Getenv("WORKFLOW_CACHE_SIZE") == "0" {
		worker.SetStickyWorkflowCacheSize(0)
	}
	srv, err := testsuite.StartDevServer(context.Background(), testsuite.DevServerOptions{
		CachedDownload: testsuite.CachedDownload{Version: "v1.9.3-nexus-serialization-context"},
		ClientOptions:  &client.Options{Namespace: namespace},
		LogLevel:       "warn",
		ExtraArgs: []string{
			"--dynamic-config-value", "frontend.enableExecuteMultiOperation=true",
			"--dynamic-config-value", "frontend.enableUpdateWorkflowExecution=true",
			"--dynamic-config-value", "system.enableActivityEagerExecution=true",
			"--dynamic-config-value", "system.enableEagerWorkflowStart=true",
			"--dynamic-config-value", "frontend.activityAPIsEnabled=true",
			"--dynamic-config-value", "nexusoperation.enableStandalone=true",
			"--dynamic-config-value", "history.enableChasm=true",
			"--dynamic-config-value", "history.enableCHASMCallbacks=true",
			"--dynamic-config-value", `system.refreshNexusEndpointsMinWait="0s"`,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed starting dev server: %v\n", err)
		os.Exit(1)
	}
	hostPort = srv.FrontendHostPort()
	code := m.Run()
	_ = srv.Stop()
	os.Exit(code)
}
