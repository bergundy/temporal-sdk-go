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
	// A proxy that forwards unary calls through a stream handler uses the
	// stream interceptor.
	forward := func(_ any, stream grpc.ServerStream) error {
		if err := stream.RecvMsg(&workflowservice.GetSystemInfoRequest{}); err != nil {
			return err
		}
		return stream.SendMsg(&workflowservice.GetSystemInfoResponse{})
	}
	for name, opts := range map[string][]grpc.ServerOption{
		"unary":  {grpc.UnaryInterceptor(UnaryServerInterceptor())},
		"stream": {grpc.StreamInterceptor(StreamServerInterceptor()), grpc.UnknownServiceHandler(forward)},
	} {
		t.Run(name, func(t *testing.T) {
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			srv := grpc.NewServer(opts...)
			if name == "unary" {
				workflowservice.RegisterWorkflowServiceServer(srv, systemInfoServer{})
			}
			go func() { _ = srv.Serve(lis) }()
			t.Cleanup(srv.Stop)

			conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })

			var md metadata.MD
			_, err = workflowservice.NewWorkflowServiceClient(conn).GetSystemInfo(t.Context(), &workflowservice.GetSystemInfoRequest{}, grpc.Header(&md))
			require.NoError(t, err)
			require.True(t, IsAdvertised(md))
		})
	}
	require.False(t, IsAdvertised(metadata.Pairs(AdvertisementHeader, "v0")))
}
