package binding

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type systemInfoServer struct {
	workflowservice.UnimplementedWorkflowServiceServer
}

func (systemInfoServer) GetSystemInfo(context.Context, *workflowservice.GetSystemInfoRequest) (*workflowservice.GetSystemInfoResponse, error) {
	return &workflowservice.GetSystemInfoResponse{}, nil
}

func TestServerInterceptorAdvertises(t *testing.T) {
	for name, opt := range map[string]grpc.ServerOption{
		"unary":  grpc.UnaryInterceptor(UnaryServerInterceptor()),
		"stream": grpc.StreamInterceptor(StreamServerInterceptor()),
	} {
		t.Run(name, func(t *testing.T) {
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			srv := grpc.NewServer(opt)
			workflowservice.RegisterWorkflowServiceServer(srv, systemInfoServer{})
			go func() { _ = srv.Serve(lis) }()
			t.Cleanup(srv.Stop)

			conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })

			var md metadata.MD
			_, err = workflowservice.NewWorkflowServiceClient(conn).GetSystemInfo(t.Context(), &workflowservice.GetSystemInfoRequest{}, grpc.Header(&md))
			require.NoError(t, err)
			if name == "unary" {
				require.True(t, IsAdvertised(md))
			} else {
				// A stream interceptor doesn't run for unary handlers; proxies
				// that forward through a stream handler use it.
				require.False(t, IsAdvertised(md))
			}
		})
	}
	require.False(t, IsAdvertised(metadata.Pairs(AdvertisementHeader, "v0")))
}
