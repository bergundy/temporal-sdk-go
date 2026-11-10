package credentials_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/credentials"
	"go.temporal.io/sdk/contrib/credentials/binding"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/encoding/protojson"
)

func startOpts(taskQueue string) client.StartWorkflowOptions {
	return client.StartWorkflowOptions{ID: newID("wf"), TaskQueue: taskQueue, WorkflowExecutionTimeout: time.Minute}
}

func history(t *testing.T, c client.Client, wfID, runID string) *historypb.History {
	t.Helper()
	h := &historypb.History{}
	iter := c.GetWorkflowHistory(context.Background(), wfID, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iter.HasNext() {
		e, err := iter.Next()
		require.NoError(t, err)
		h.Events = append(h.Events, e)
	}
	return h
}

func TestHappyPath(t *testing.T) {
	k := newKeys(t)
	c := pluginClient(t, k.full(t))
	tq := newID("tq")
	startWorker(t, c, tq, worker.Options{})
	ep := createEndpoint(t, c, tq)

	run, err := c.ExecuteWorkflow(user("eyJ-user"), startOpts(tq), Agent, []string{ep})
	require.NoError(t, err)
	var got map[string]string
	require.NoError(t, run.Get(context.Background(), &got))
	// The Nexus operation opening at all shows the server sends the endpoint.
	require.Equal(t, map[string]string{
		"activity": "eyJ-user", "activity-plain": missing,
		"nexus:" + ep: "eyJ-user", "nexus-plain:" + ep: missing,
	}, got)

	t.Run("history holds only ciphertext", func(t *testing.T) {
		plain := newClient(t, client.Options{})
		h := history(t, plain, run.GetID(), run.GetRunID())
		b, err := protojson.Marshal(h)
		require.NoError(t, err)
		require.NotContains(t, string(b), "eyJ-user")
		require.NotContains(t, string(b), binding.HeaderOpened)

		sealed := 0
		for _, e := range h.GetEvents() {
			var p *commonpb.Payload
			switch {
			case e.GetWorkflowExecutionStartedEventAttributes() != nil:
				p = e.GetWorkflowExecutionStartedEventAttributes().GetHeader().GetFields()[binding.HeaderCredentials]
			case e.GetActivityTaskScheduledEventAttributes() != nil:
				p = e.GetActivityTaskScheduledEventAttributes().GetHeader().GetFields()[binding.HeaderCredentials]
			case e.GetNexusOperationScheduledEventAttributes() != nil:
				if v, ok := e.GetNexusOperationScheduledEventAttributes().GetNexusHeader()[binding.HeaderCredentials]; ok {
					require.True(t, strings.HasPrefix(v, binding.SealedPrefix))
					sealed++
				}
				continue
			}
			if p != nil {
				require.Equal(t, binding.EncodingSealed, string(p.GetMetadata()["encoding"]))
				require.True(t, strings.HasPrefix(string(p.GetData()), binding.SealedPrefix))
				sealed++
			}
		}
		require.Equal(t, 3, sealed, "the start, one Activity, and one Nexus operation carry credentials")
	})

	t.Run("replay from JSON", func(t *testing.T) {
		h := history(t, c, run.GetID(), run.GetRunID())
		b, err := protojson.Marshal(h)
		require.NoError(t, err)
		path := filepath.Join(t.TempDir(), "history.json")
		require.NoError(t, os.WriteFile(path, b, 0o600))

		replay := func(opts credentials.PluginOptions) error {
			r, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{
				Plugins: []worker.Plugin{newPlugin(t, opts)},
			})
			require.NoError(t, err)
			r.RegisterWorkflow(Agent)
			return r.ReplayWorkflowHistoryFromJSONFile(nil, path)
		}
		require.NoError(t, replay(credentials.PluginOptions{Keyset: k.full(t), ReplayNamespace: namespace}))
		// The file's opened results aren't trusted: without the keyset, the
		// start's credential is missing and the replay diverges.
		require.Error(t, replay(credentials.PluginOptions{Proxy: true}))
	})
}

func TestReplayAfterWorkerRestart(t *testing.T) {
	k := newKeys(t)
	c := pluginClient(t, k.full(t))
	tq := newID("tq")
	first := worker.New(c, tq, worker.Options{})
	first.RegisterWorkflow(Waiter)
	require.NoError(t, first.Start())

	run, err := c.ExecuteWorkflow(user("eyJ-user"), startOpts(tq), Waiter)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, e := range history(t, c, run.GetID(), run.GetRunID()).GetEvents() {
			if e.GetEventType() == enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED {
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond)
	first.Stop()

	// A new Worker has nothing cached, so it replays from history fetched
	// through the client.
	startWorker(t, c, tq, worker.Options{})
	require.NoError(t, c.SignalWorkflow(context.Background(), run.GetID(), "", "go", nil))
	var got string
	require.NoError(t, run.Get(context.Background(), &got))
	require.Equal(t, "eyJ-user", got)
}

func TestRejections(t *testing.T) {
	k := newKeys(t)
	c := pluginClient(t, k.full(t))
	plain := newClient(t, client.Options{})
	tq := newID("tq")
	startWorker(t, c, tq, worker.Options{})

	t.Run("start without credentials", func(t *testing.T) {
		run, err := c.ExecuteWorkflow(context.Background(), startOpts(tq), Agent, []string{})
		require.NoError(t, err)
		requireMissing(t, run.Get(context.Background(), nil))
	})

	// The headers below are taken from a run that opened.
	run, err := c.ExecuteWorkflow(user("eyJ-user"), startOpts(tq), Agent, []string{})
	require.NoError(t, err)
	require.NoError(t, run.Get(context.Background(), nil))
	started := history(t, plain, run.GetID(), run.GetRunID()).GetEvents()[0].GetWorkflowExecutionStartedEventAttributes()

	rawStart := func(t *testing.T, wfID string, input *commonpb.Payloads, header *commonpb.Header) error {
		t.Helper()
		resp, err := plain.WorkflowService().StartWorkflowExecution(context.Background(), &workflowservice.StartWorkflowExecutionRequest{
			Namespace: namespace, WorkflowId: wfID, WorkflowType: started.GetWorkflowType(),
			TaskQueue: started.GetTaskQueue(), Input: input, Header: header, RequestId: newID("req"),
		})
		require.NoError(t, err)
		return plain.GetWorkflow(context.Background(), wfID, resp.GetRunId()).Get(context.Background(), nil)
	}

	t.Run("header copied to another Workflow ID", func(t *testing.T) {
		requireMissing(t, rawStart(t, newID("wf"), started.GetInput(), started.GetHeader()))
	})
	t.Run("changed input", func(t *testing.T) {
		input, err := converter.GetDefaultDataConverter().ToPayloads([]string{"changed"})
		require.NoError(t, err)
		requireMissing(t, rawStart(t, run.GetID(), input, started.GetHeader()))
	})
	t.Run("plaintext injected with a raw gRPC client", func(t *testing.T) {
		header := &commonpb.Header{Fields: map[string]*commonpb.Payload{
			binding.HeaderCredentials: binding.PendingPayload(map[string]string{"user_token": "eyJ-forged"}),
		}}
		requireMissing(t, rawStart(t, newID("wf"), started.GetInput(), header))
	})
	t.Run("forged opened result injected with a raw gRPC client", func(t *testing.T) {
		forged := binding.OpenedPayload(string(started.GetHeader().GetFields()[binding.HeaderCredentials].GetData()),
			binding.Opened{OK: true, Credentials: map[string]string{"user_token": "eyJ-forged"}})
		header := &commonpb.Header{Fields: map[string]*commonpb.Payload{binding.HeaderCredentials: forged}}
		requireMissing(t, rawStart(t, newID("wf"), started.GetInput(), header))
	})
}

func TestUpdatesAndRefresh(t *testing.T) {
	k := newKeys(t)
	c := pluginClient(t, k.full(t))
	tq := newID("tq")
	startWorker(t, c, tq, worker.Options{})
	alice := jwtFor("alice", "1")
	run, err := c.ExecuteWorkflow(user(alice), startOpts(tq), Approval)
	require.NoError(t, err)

	update := func(ctx context.Context, name string) error {
		h, err := c.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
			WorkflowID: run.GetID(), UpdateName: name, WaitForStage: client.WorkflowUpdateStageCompleted,
		})
		if err != nil {
			return err
		}
		return h.Get(ctx, nil)
	}
	approver := func(token string) context.Context {
		return credentials.Attach(context.Background(), credentials.Values{"approver_token": token})
	}

	requireMissing(t, update(context.Background(), "approve"))
	require.NoError(t, update(approver("eyJ-approver"), "approve"))
	requireMissing(t, update(approver("eyJ-second"), "approve"))

	fresh := jwtFor("alice", "2")
	requireMissing(t, update(user(jwtFor("mallory", "1")), "refresh"))
	require.NoError(t, update(user(fresh), "refresh"))

	// After logout, a refresh has nothing to compare with and is declined.
	require.NoError(t, c.SignalWorkflow(context.Background(), run.GetID(), "", "logout", nil))
	require.Eventually(t, func() bool {
		v, err := c.QueryWorkflow(context.Background(), run.GetID(), "", "holds", "user_token")
		var holds bool
		return err == nil && v.Get(&holds) == nil && !holds
	}, 10*time.Second, 50*time.Millisecond)
	requireMissing(t, update(user(jwtFor("alice", "3")), "refresh"))

	require.NoError(t, c.SignalWorkflow(context.Background(), run.GetID(), "", "done", nil))
	var got map[string]string
	require.NoError(t, run.Get(context.Background(), &got))
	require.Equal(t, map[string]string{"user_token": missing, "approver_token": "eyJ-approver"}, got)
}

func TestForwarding(t *testing.T) {
	k := newKeys(t)
	c := pluginClient(t, k.full(t))
	tq := newID("tq")
	startWorker(t, c, tq, worker.Options{})

	t.Run("continue-as-new and child Workflows", func(t *testing.T) {
		run, err := c.ExecuteWorkflow(user("eyJ-user"), startOpts(tq), Chain, 0)
		require.NoError(t, err)
		var got string
		require.NoError(t, run.Get(context.Background(), &got))
		require.Equal(t, "eyJ-user", got)
	})

	t.Run("signal-with-start", func(t *testing.T) {
		opts := startOpts(tq)
		run, err := c.SignalWithStartWorkflow(user("eyJ-user"), opts.ID, "approve", nil, opts, SignalKeeper, 2)
		require.NoError(t, err)
		require.NoError(t, c.SignalWorkflow(context.Background(), opts.ID, "", "approve", nil))
		var got []string
		require.NoError(t, run.Get(context.Background(), &got))
		// The start carries no credentials; the Signal does.
		require.Equal(t, []string{"start-without-credentials", "eyJ-user", "skipped"}, got)
	})

	t.Run("update-with-start", func(t *testing.T) {
		opts := startOpts(tq)
		opts.WorkflowIDConflictPolicy = enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL
		h, err := c.UpdateWithStartWorkflow(user("eyJ-user"), client.UpdateWithStartWorkflowOptions{
			StartWorkflowOperation: c.NewWithStartWorkflowOperation(opts, UpdateWithStart),
			UpdateOptions:          client.UpdateWorkflowOptions{UpdateName: "token", WaitForStage: client.WorkflowUpdateStageCompleted},
		})
		require.NoError(t, err)
		var token string
		require.NoError(t, h.Get(context.Background(), &token))
		require.Equal(t, "eyJ-user", token)
		var got string
		require.NoError(t, c.GetWorkflow(context.Background(), opts.ID, "").Get(context.Background(), &got))
		require.Equal(t, "start-ok", got)
	})

	t.Run("eager start and eager activities", func(t *testing.T) {
		opts := startOpts(tq)
		opts.EnableEagerStart = true
		run, err := c.ExecuteWorkflow(user("eyJ-user"), opts, Agent, []string{})
		require.NoError(t, err)
		var got map[string]string
		require.NoError(t, run.Get(context.Background(), &got))
		require.Equal(t, "eyJ-user", got["activity"])
	})
}

func TestStandalone(t *testing.T) {
	k := newKeys(t)
	c := pluginClient(t, k.full(t))
	tq := newID("tq")
	startWorker(t, c, tq, worker.Options{})

	t.Run("Activity", func(t *testing.T) {
		h, err := c.ExecuteActivity(user("eyJ-user"), client.StartActivityOptions{
			ID: newID("act"), TaskQueue: tq, StartToCloseTimeout: time.Minute,
		}, TokenOf, "user_token")
		require.NoError(t, err)
		var got string
		require.NoError(t, h.Get(context.Background(), &got))
		require.Equal(t, "eyJ-user", got)
	})

	t.Run("Nexus operation", func(t *testing.T) {
		ep := createEndpoint(t, c, tq)
		nc, err := c.NewNexusClient(client.NexusClientOptions{Endpoint: ep, Service: "tools"})
		require.NoError(t, err)
		h, err := nc.ExecuteOperation(user("eyJ-user"), tokenOp, "user_token", client.StartNexusOperationOptions{
			ID: newID("op"), ScheduleToCloseTimeout: time.Minute,
		})
		require.NoError(t, err)
		var got string
		require.NoError(t, h.Get(context.Background(), &got))
		require.Equal(t, "eyJ-user", got)
	})
}

func TestKeySelection(t *testing.T) {
	k := newKeys(t)

	t.Run("strict mode and named keys", func(t *testing.T) {
		c := newClient(t, client.Options{Plugins: []client.Plugin{newPlugin(t, credentials.PluginOptions{Keyset: k.full(t), Strict: true})}})
		tq := newID("tq")
		startWorker(t, c, tq, worker.Options{})

		_, err := c.ExecuteWorkflow(user("eyJ-user"), startOpts(tq), ChildToken)
		var fp *serviceerror.FailedPrecondition
		require.ErrorAs(t, err, &fp)
		require.ErrorContains(t, err, "no key selected")

		ctx := credentials.AttachWithKey(context.Background(), "payments", credentials.Values{"user_token": "eyJ-user"})
		run, err := c.ExecuteWorkflow(ctx, startOpts(tq), ChildToken)
		require.NoError(t, err)
		var got string
		require.NoError(t, run.Get(context.Background(), &got))
		require.Equal(t, "eyJ-user", got)
	})

	t.Run("Nexus endpoint overrides", func(t *testing.T) {
		// The payments service holds only its own private key.
		paymentsClient := pluginClient(t, k.keyset(t, nil, "payments"))
		paymentsTQ := newID("tq")
		startWorker(t, paymentsClient, paymentsTQ, worker.Options{})
		payments := createEndpoint(t, paymentsClient, paymentsTQ)
		other := createEndpoint(t, paymentsClient, paymentsTQ)

		// The caller seals Nexus calls to the payments endpoint to its key, and
		// everything else to the default, which the payments service can't open.
		c := pluginClient(t, k.keyset(t, map[string]string{payments: "payments"}, "agents"))
		tq := newID("tq")
		startWorker(t, c, tq, worker.Options{})
		run, err := c.ExecuteWorkflow(user("eyJ-user"), startOpts(tq), Agent, []string{payments, other})
		require.NoError(t, err)
		var got map[string]string
		require.NoError(t, run.Get(context.Background(), &got))
		require.Equal(t, "eyJ-user", got["nexus:"+payments])
		require.Equal(t, missing, got["nexus:"+other])
	})
}

// codec stands in for an encrypting payload codec.
type codec struct{}

func (codec) Encode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	out := make([]*commonpb.Payload, len(payloads))
	for i, p := range payloads {
		b, err := p.Marshal()
		if err != nil {
			return nil, err
		}
		for j := range b {
			b[j] ^= 0x5a
		}
		out[i] = &commonpb.Payload{Metadata: map[string][]byte{"encoding": []byte("binary/test-xor")}, Data: b}
	}
	return out, nil
}

func (codec) Decode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	out := make([]*commonpb.Payload, len(payloads))
	for i, p := range payloads {
		if string(p.GetMetadata()["encoding"]) != "binary/test-xor" {
			out[i] = p
			continue
		}
		b := append([]byte(nil), p.GetData()...)
		for j := range b {
			b[j] ^= 0x5a
		}
		out[i] = &commonpb.Payload{}
		if err := out[i].Unmarshal(b); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func TestCodecEncryptedPayloadsThroughNexus(t *testing.T) {
	k := newKeys(t)
	c := newClient(t, client.Options{
		Plugins:       []client.Plugin{newPlugin(t, credentials.PluginOptions{Keyset: k.full(t)})},
		DataConverter: converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), codec{}),
	})
	tq := newID("tq")
	startWorker(t, c, tq, worker.Options{})
	ep := createEndpoint(t, c, tq)

	run, err := c.ExecuteWorkflow(user("eyJ-user"), startOpts(tq), Agent, []string{ep})
	require.NoError(t, err)
	var got map[string]string
	require.NoError(t, run.Get(context.Background(), &got))
	require.Equal(t, "eyJ-user", got["activity"])
	require.Equal(t, "eyJ-user", got["nexus:"+ep], "D() must survive the server's payload round trip")

	started := history(t, c, run.GetID(), run.GetRunID()).GetEvents()[0].GetWorkflowExecutionStartedEventAttributes()
	require.Equal(t, "binary/test-xor", string(started.GetInput().GetPayloads()[0].GetMetadata()["encoding"]))
}
