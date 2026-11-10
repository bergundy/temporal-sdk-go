package credentials_test

import (
	"context"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/serviceerror"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/credentials"
	"go.temporal.io/sdk/contrib/credentials/binding"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// frontend is a fake Temporal frontend recording the starts it receives.
type frontend struct {
	workflowservice.UnimplementedWorkflowServiceServer
	advertise bool
	mu        sync.Mutex
	starts    []*workflowservice.StartWorkflowExecutionRequest
	infos     int
}

func (f *frontend) GetSystemInfo(ctx context.Context, _ *workflowservice.GetSystemInfoRequest) (*workflowservice.GetSystemInfoResponse, error) {
	f.mu.Lock()
	f.infos++
	f.mu.Unlock()
	if f.advertise {
		_ = grpc.SetHeader(ctx, metadata.Pairs(binding.AdvertisementHeader, binding.AdvertisementValue))
	}
	return &workflowservice.GetSystemInfoResponse{Capabilities: &workflowservice.GetSystemInfoResponse_Capabilities{}}, nil
}

func (f *frontend) StartWorkflowExecution(ctx context.Context, req *workflowservice.StartWorkflowExecutionRequest) (*workflowservice.StartWorkflowExecutionResponse, error) {
	f.mu.Lock()
	f.starts = append(f.starts, req)
	f.mu.Unlock()
	if f.advertise {
		_ = grpc.SetHeader(ctx, metadata.Pairs(binding.AdvertisementHeader, binding.AdvertisementValue))
	}
	return &workflowservice.StartWorkflowExecutionResponse{RunId: "run-1"}, nil
}

func (f *frontend) lastStart(t *testing.T) *workflowservice.StartWorkflowExecutionRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.starts)
	return f.starts[len(f.starts)-1]
}

func startFrontend(t *testing.T, advertise bool) (*frontend, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &frontend{advertise: advertise}
	srv := grpc.NewServer()
	workflowservice.RegisterWorkflowServiceServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return f, lis.Addr().String()
}

// requireFailedPrecondition checks for the error the binding layer and the
// proxy guard fail a call with, as the client surfaces it.
func requireFailedPrecondition(t *testing.T, err error, contains string) {
	t.Helper()
	var fp *serviceerror.FailedPrecondition
	require.ErrorAs(t, err, &fp)
	require.ErrorContains(t, err, contains)
}

func testKeyset(t *testing.T) *binding.Keyset {
	t.Helper()
	k, err := binding.GenerateKey("agents")
	require.NoError(t, err)
	ks, err := binding.NewKeyset(binding.KeysetJSON{Keys: []binding.KeyJSON{k}, Default: "agents"})
	require.NoError(t, err)
	return ks
}

func dial(t *testing.T, addr string, opts client.Options) client.Client {
	t.Helper()
	opts.HostPort = addr
	c, err := client.Dial(opts)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

var startOptions = client.StartWorkflowOptions{ID: "wf-1", TaskQueue: "agents"}

func attached() context.Context {
	return credentials.Attach(context.Background(), credentials.Values{"user_token": "eyJ-user"})
}

func TestPluginSealsInProcess(t *testing.T) {
	f, addr := startFrontend(t, false)
	ks := testKeyset(t)
	plugin, err := credentials.NewPlugin(credentials.PluginOptions{Keyset: ks})
	require.NoError(t, err)
	c := dial(t, addr, client.Options{Plugins: []client.Plugin{plugin}})

	_, err = c.ExecuteWorkflow(attached(), startOptions, "Agent", "plan")
	require.NoError(t, err)
	req := f.lastStart(t)
	p := req.GetHeader().GetFields()[binding.HeaderCredentials]
	require.Equal(t, binding.EncodingSealed, string(p.GetMetadata()["encoding"]))
	got, err := ks.Open(binding.WorkflowContext("default", "Agent", "wf-1", req.GetInput().GetPayloads()), string(p.GetData()))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"user_token": "eyJ-user"}, got)

	// The innermost Attach wins, and a call without one carries nothing.
	ctx := credentials.Attach(attached(), credentials.Values{"other": "x"})
	_, err = c.ExecuteWorkflow(ctx, startOptions, "Agent", "plan")
	require.NoError(t, err)
	req = f.lastStart(t)
	got, err = ks.Open(binding.WorkflowContext("default", "Agent", "wf-1", req.GetInput().GetPayloads()),
		string(req.GetHeader().GetFields()[binding.HeaderCredentials].GetData()))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"other": "x"}, got)

	_, err = c.ExecuteWorkflow(context.Background(), startOptions, "Agent", "plan")
	require.NoError(t, err)
	require.NotContains(t, f.lastStart(t).GetHeader().GetFields(), binding.HeaderCredentials)
}

func TestPluginProxyGuard(t *testing.T) {
	plugin, err := credentials.NewPlugin(credentials.PluginOptions{Proxy: true})
	require.NoError(t, err)

	t.Run("refuses a server that doesn't advertise", func(t *testing.T) {
		f, addr := startFrontend(t, false)
		c := dial(t, addr, client.Options{Plugins: []client.Plugin{plugin}})
		_, err := c.ExecuteWorkflow(attached(), startOptions, "Agent", "plan")
		requireFailedPrecondition(t, err, "the server didn't identify itself as a credential-binding proxy")
		f.mu.Lock()
		require.Empty(t, f.starts)
		f.mu.Unlock()

		_, err = c.ExecuteWorkflow(context.Background(), startOptions, "Agent", "plan")
		require.NoError(t, err, "a call without credentials isn't held back")
	})

	t.Run("sends plaintext to a proxy", func(t *testing.T) {
		f, addr := startFrontend(t, true)
		c := dial(t, addr, client.Options{Plugins: []client.Plugin{plugin}})
		_, err := c.ExecuteWorkflow(attached(), startOptions, "Agent", "plan")
		require.NoError(t, err)
		p := f.lastStart(t).GetHeader().GetFields()[binding.HeaderCredentials]
		require.Equal(t, binding.EncodingPending, string(p.GetMetadata()["encoding"]))
	})

	t.Run("a lazy client checks first", func(t *testing.T) {
		f, addr := startFrontend(t, true)
		c, err := client.NewLazyClient(client.Options{HostPort: addr, Plugins: []client.Plugin{plugin}})
		require.NoError(t, err)
		t.Cleanup(c.Close)
		_, err = c.ExecuteWorkflow(attached(), startOptions, "Agent", "plan")
		require.NoError(t, err)
		f.mu.Lock()
		defer f.mu.Unlock()
		require.Positive(t, f.infos)
	})
}

func TestNewClientFromExisting(t *testing.T) {
	_, addr := startFrontend(t, false)
	plugin, err := credentials.NewPlugin(credentials.PluginOptions{Keyset: testKeyset(t)})
	require.NoError(t, err)

	plain := dial(t, addr, client.Options{})
	_, err = client.NewClientFromExisting(plain, client.Options{Plugins: []client.Plugin{plugin}})
	require.ErrorContains(t, err, "the existing client's connection has no credentials interceptor; create it with the credentials plugin")

	withPlugin := dial(t, addr, client.Options{Plugins: []client.Plugin{plugin}})
	c, err := client.NewClientFromExisting(withPlugin, client.Options{Plugins: []client.Plugin{plugin}})
	require.NoError(t, err)
	c.Close()
}

func TestPluginOnlyOnWorker(t *testing.T) {
	_, addr := startFrontend(t, false)
	plugin, err := credentials.NewPlugin(credentials.PluginOptions{Keyset: testKeyset(t)})
	require.NoError(t, err)
	c := dial(t, addr, client.Options{})
	require.PanicsWithError(t,
		"credentials: set the credentials plugin on the client (client.Options.Plugins), not only on the Worker; Workers inherit it from the client",
		func() { worker.New(c, "agents", worker.Options{Plugins: []worker.Plugin{plugin}}) })
}

func TestNewPluginValidates(t *testing.T) {
	_, err := credentials.NewPlugin(credentials.PluginOptions{})
	require.ErrorContains(t, err, "Keyset is required unless PluginOptions.Proxy is set")
	_, err = credentials.NewPlugin(credentials.PluginOptions{Proxy: true, Strict: true})
	require.ErrorContains(t, err, "set it in the proxy's configuration")
}

// encoder is a gRPC-level payload codec that encodes every payload, or only
// those that aren't credentials payloads.
type encoder struct{ skipCredentials bool }

func (e encoder) Encode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	out := make([]*commonpb.Payload, len(payloads))
	for i, p := range payloads {
		if e.skipCredentials && binding.IsCredentialsPayload(p) {
			out[i] = p
			continue
		}
		b, err := proto.Marshal(p)
		if err != nil {
			return nil, err
		}
		out[i] = &commonpb.Payload{
			Metadata: map[string][]byte{"encoding": []byte("binary/test-encoded")},
			Data:     []byte(base64.StdEncoding.EncodeToString(b)),
		}
	}
	return out, nil
}

func (e encoder) Decode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) { return payloads, nil }

func TestGRPCCodecMustPassCredentialsThrough(t *testing.T) {
	f, addr := startFrontend(t, false)
	ks := testKeyset(t)
	plugin, err := credentials.NewPlugin(credentials.PluginOptions{Keyset: ks})
	require.NoError(t, err)
	newClient := func(codec encoder) client.Client {
		ic, err := converter.NewPayloadCodecGRPCClientInterceptor(converter.PayloadCodecGRPCClientInterceptorOptions{Codecs: []converter.PayloadCodec{codec}})
		require.NoError(t, err)
		return dial(t, addr, client.Options{
			Plugins:           []client.Plugin{plugin},
			ConnectionOptions: client.ConnectionOptions{DialOptions: []grpc.DialOption{grpc.WithChainUnaryInterceptor(ic)}},
		})
	}

	_, err = newClient(encoder{}).ExecuteWorkflow(attached(), startOptions, "Agent", "plan")
	requireFailedPrecondition(t, err, "binding.IsCredentialsPayload")

	_, err = newClient(encoder{skipCredentials: true}).ExecuteWorkflow(attached(), startOptions, "Agent", "plan")
	require.NoError(t, err)
	req := f.lastStart(t)
	require.Equal(t, "binary/test-encoded", string(req.GetInput().GetPayloads()[0].GetMetadata()["encoding"]))
	// D() covers the payload bytes as sent, after the codec.
	_, err = ks.Open(binding.WorkflowContext("default", "Agent", "wf-1", req.GetInput().GetPayloads()),
		string(req.GetHeader().GetFields()[binding.HeaderCredentials].GetData()))
	require.NoError(t, err)
}

func ReplayAgent(ctx workflow.Context) error {
	return credentials.Require(ctx, "user_token")
}

// replayHistory is a completed run of ReplayAgent whose start carries header.
func replayHistory(input *commonpb.Payloads, header *commonpb.Header) *historypb.History {
	tq := &taskqueuepb.TaskQueue{Name: "agents"}
	return &historypb.History{Events: []*historypb.HistoryEvent{
		{EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
			WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
				WorkflowType: &commonpb.WorkflowType{Name: "ReplayAgent"}, TaskQueue: tq, Input: input, Header: header,
				WorkflowId: "wf-1", WorkflowTaskTimeout: durationpb.New(10 * time.Second),
			},
		}},
		{EventId: 2, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED, Attributes: &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{
			WorkflowTaskScheduledEventAttributes: &historypb.WorkflowTaskScheduledEventAttributes{TaskQueue: tq},
		}},
		{EventId: 3, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED, Attributes: &historypb.HistoryEvent_WorkflowTaskStartedEventAttributes{
			WorkflowTaskStartedEventAttributes: &historypb.WorkflowTaskStartedEventAttributes{ScheduledEventId: 2},
		}},
		{EventId: 4, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED, Attributes: &historypb.HistoryEvent_WorkflowTaskCompletedEventAttributes{
			WorkflowTaskCompletedEventAttributes: &historypb.WorkflowTaskCompletedEventAttributes{ScheduledEventId: 2, StartedEventId: 3},
		}},
		{EventId: 5, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED, Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{
			WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{WorkflowTaskCompletedEventId: 4},
		}},
	}}
}

func TestReplayFromFile(t *testing.T) {
	ks := testKeyset(t)
	key, err := ks.SealingKey("", "", false)
	require.NoError(t, err)
	input, err := converter.GetDefaultDataConverter().ToPayloads()
	require.NoError(t, err)
	sealedFor := func(wfID string) string {
		s, err := binding.Seal(key, binding.WorkflowContext("prod", "ReplayAgent", wfID, input.GetPayloads()), map[string]string{"user_token": "eyJ-user"})
		require.NoError(t, err)
		return s
	}

	// Fetched through the binding layer, so the start header arrives opened.
	fetched := replayHistory(input, &commonpb.Header{Fields: map[string]*commonpb.Payload{binding.HeaderCredentials: binding.SealedPayload(sealedFor("wf-1"))}})
	binding.OpenHistory(binding.Options{Keyset: ks}, "prod", "wf-1", fetched)
	o, ok := binding.OpenedFromHeader(fetched.GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetHeader().GetFields())
	require.True(t, ok)
	require.True(t, o.OK)

	// A forged opened result next to a header sealed for another Workflow.
	forged := replayHistory(input, &commonpb.Header{Fields: map[string]*commonpb.Payload{
		binding.HeaderCredentials: binding.OpenedPayload(sealedFor("wf-2"), binding.Opened{OK: true, Credentials: map[string]string{"user_token": "forged"}}),
	}})

	writeJSON := func(h *historypb.History) string {
		b, err := protojson.Marshal(h)
		require.NoError(t, err)
		path := filepath.Join(t.TempDir(), "history.json")
		require.NoError(t, os.WriteFile(path, b, 0o600))
		return path
	}
	replay := func(opts credentials.PluginOptions, path string) error {
		plugin, err := credentials.NewPlugin(opts)
		require.NoError(t, err)
		r, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{Plugins: []worker.Plugin{plugin}})
		require.NoError(t, err)
		r.RegisterWorkflow(ReplayAgent)
		return r.ReplayWorkflowHistoryFromJSONFile(nil, path)
	}

	require.NoError(t, replay(credentials.PluginOptions{Keyset: ks, ReplayNamespace: "prod"}, writeJSON(fetched)))
	require.NoError(t, replay(credentials.PluginOptions{Proxy: true, Keyset: ks, ReplayNamespace: "prod"}, writeJSON(fetched)))
	// The file's opened result isn't trusted, so without the keyset, or with
	// the wrong namespace, the credential is missing and the replay diverges.
	for name, opts := range map[string]credentials.PluginOptions{
		"no keyset":       {Proxy: true},
		"wrong namespace": {Keyset: ks, ReplayNamespace: "dev"},
		"no namespace":    {Keyset: ks},
	} {
		err := replay(opts, writeJSON(fetched))
		require.Error(t, err, name)
	}
	err = replay(credentials.PluginOptions{Keyset: ks, ReplayNamespace: "prod"}, writeJSON(forged))
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), "forged"))
}
