package binding

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	commandpb "go.temporal.io/api/command/v1"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	nexuspb "go.temporal.io/api/nexus/v1"
	protocolpb "go.temporal.io/api/protocol/v1"
	querypb "go.temporal.io/api/query/v1"
	updatepb "go.temporal.io/api/update/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
)

const ns = "default"

var creds = map[string]string{"user_token": "eyJ-user"}

func pendingHeader(key string) *commonpb.Header {
	h := &commonpb.Header{Fields: map[string]*commonpb.Payload{
		HeaderCredentials: PendingPayload(creds),
		"other":           payload(`"kept"`),
	}}
	if key != "" {
		h.Fields[HeaderKey] = KeyPayload(key)
	}
	return h
}

func pendingNexus() map[string]string {
	return map[string]string{HeaderCredentials: PendingNexus(creds), "other": "kept"}
}

type harness struct {
	t    *testing.T
	ks   *Keyset
	f    KeysetJSON
	ic   grpc.UnaryClientInterceptor
	logs *bytes.Buffer
}

func newHarness(t *testing.T, strict bool) *harness {
	t.Helper()
	_, f := newKeyset(t, "agents", "agents", "payments")
	f.NexusEndpoints = map[string]string{"payments-ep": "payments"}
	ks, err := NewKeyset(f)
	require.NoError(t, err)
	logs := &bytes.Buffer{}
	return &harness{
		t: t, ks: ks, f: f, logs: logs,
		ic: NewInterceptor(Options{Keyset: ks, Strict: strict, Logger: slog.New(slog.NewTextHandler(logs, nil))}),
	}
}

// send runs req through the interceptor and returns what reached the wire.
func (h *harness) send(method string, req proto.Message) (proto.Message, error) {
	var sent proto.Message
	err := h.ic(context.Background(), method, req, &workflowservice.GetSystemInfoResponse{}, nil,
		func(_ context.Context, _ string, req, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			sent = req.(proto.Message)
			return nil
		})
	return sent, err
}

// receive runs a reply through the interceptor and returns what the
// application gets.
func (h *harness) receive(method string, req, reply proto.Message) proto.Message {
	got := reply.ProtoReflect().New().Interface()
	err := h.ic(context.Background(), method, req, got, nil,
		func(_ context.Context, _ string, _, out any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			proto.Merge(out.(proto.Message), reply)
			return nil
		})
	require.NoError(h.t, err)
	return got
}

func (h *harness) seal(info []byte, key string) string {
	k, err := h.ks.SealingKey(key, "", false)
	require.NoError(h.t, err)
	s, err := Seal(k, info, creds)
	require.NoError(h.t, err)
	return s
}

func (h *harness) requireSealed(p *commonpb.Payload, info []byte) {
	h.t.Helper()
	require.Equal(h.t, EncodingSealed, payloadEncoding(p))
	require.NotContains(h.t, string(p.GetData()), "eyJ")
	got, err := h.ks.Open(info, string(p.GetData()))
	require.NoError(h.t, err)
	require.Equal(h.t, creds, got)
}

func requireOpened(t *testing.T, h *commonpb.Header) {
	t.Helper()
	o, ok := OpenedFromHeader(h.GetFields())
	require.True(t, ok, "no opened result")
	require.True(t, o.OK, "failed to open: %s", o.Reason)
	require.Equal(t, creds, o.Credentials)
}

func requireFailed(t *testing.T, h *commonpb.Header, reason string) {
	t.Helper()
	o, ok := OpenedFromHeader(h.GetFields())
	require.True(t, ok, "no opened result")
	require.False(t, o.OK)
	require.Equal(t, reason, o.Reason)
	require.Empty(t, o.Credentials)
}

func method(name string) string { return "/temporal.api.workflowservice.v1.WorkflowService/" + name }

func command(attrs any) *commandpb.Command {
	c := &commandpb.Command{}
	switch a := attrs.(type) {
	case *commandpb.ScheduleActivityTaskCommandAttributes:
		c.Attributes = &commandpb.Command_ScheduleActivityTaskCommandAttributes{ScheduleActivityTaskCommandAttributes: a}
	case *commandpb.StartChildWorkflowExecutionCommandAttributes:
		c.Attributes = &commandpb.Command_StartChildWorkflowExecutionCommandAttributes{StartChildWorkflowExecutionCommandAttributes: a}
	case *commandpb.ContinueAsNewWorkflowExecutionCommandAttributes:
		c.Attributes = &commandpb.Command_ContinueAsNewWorkflowExecutionCommandAttributes{ContinueAsNewWorkflowExecutionCommandAttributes: a}
	case *commandpb.SignalExternalWorkflowExecutionCommandAttributes:
		c.Attributes = &commandpb.Command_SignalExternalWorkflowExecutionCommandAttributes{SignalExternalWorkflowExecutionCommandAttributes: a}
	case *commandpb.ScheduleNexusOperationCommandAttributes:
		c.Attributes = &commandpb.Command_ScheduleNexusOperationCommandAttributes{ScheduleNexusOperationCommandAttributes: a}
	case *commandpb.RecordMarkerCommandAttributes:
		c.Attributes = &commandpb.Command_RecordMarkerCommandAttributes{RecordMarkerCommandAttributes: a}
	case *commandpb.ProtocolMessageCommandAttributes:
		c.Attributes = &commandpb.Command_ProtocolMessageCommandAttributes{ProtocolMessageCommandAttributes: a}
	}
	return c
}

func completion(cmds ...*commandpb.Command) *workflowservice.RespondWorkflowTaskCompletedRequest {
	return &workflowservice.RespondWorkflowTaskCompletedRequest{Namespace: ns, ResourceId: "workflow:wf-1", Commands: cmds}
}

func updateRequest(header *commonpb.Header, updateID string) *workflowservice.UpdateWorkflowExecutionRequest {
	return &workflowservice.UpdateWorkflowExecutionRequest{
		Namespace:         ns,
		WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: "wf-1"},
		Request: &updatepb.Request{
			Meta:  &updatepb.Meta{UpdateId: updateID},
			Input: &updatepb.Input{Header: header, Name: "approve", Args: &commonpb.Payloads{Payloads: payloads(`"yes"`)}},
		},
	}
}

func startRequest(header *commonpb.Header) *workflowservice.StartWorkflowExecutionRequest {
	return &workflowservice.StartWorkflowExecutionRequest{
		Namespace: ns, WorkflowId: "wf-1", WorkflowType: &commonpb.WorkflowType{Name: "Agent"},
		Input: &commonpb.Payloads{Payloads: payloads(`"in"`)}, Header: header,
	}
}

type outboundCase struct {
	name     string
	location string
	method   string
	req      func(h *commonpb.Header, n map[string]string) proto.Message
	// sealedAt returns the sealed value as sent: a payload or a Nexus string.
	sealedAt func(m proto.Message) (*commonpb.Payload, string)
	info     []byte
	nexus    bool
}

var in = payloads(`"in"`)

func outboundCases() []outboundCase {
	nexusIn := payload(`{"x":1}`)
	return []outboundCase{
		{
			name: "StartWorkflowExecution", location: "temporal.api.workflowservice.v1.StartWorkflowExecutionRequest.header",
			method: method("StartWorkflowExecution"),
			req:    func(h *commonpb.Header, _ map[string]string) proto.Message { return startRequest(h) },
			sealedAt: func(m proto.Message) (*commonpb.Payload, string) {
				return m.(*workflowservice.StartWorkflowExecutionRequest).GetHeader().GetFields()[HeaderCredentials], ""
			},
			info: WorkflowContext(ns, "Agent", "wf-1", in),
		},
		{
			name: "SignalWorkflowExecution", location: "temporal.api.workflowservice.v1.SignalWorkflowExecutionRequest.header",
			method: method("SignalWorkflowExecution"),
			req: func(h *commonpb.Header, _ map[string]string) proto.Message {
				return &workflowservice.SignalWorkflowExecutionRequest{
					Namespace: ns, WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: "wf-1"}, SignalName: "approve",
					Input: &commonpb.Payloads{Payloads: in}, Header: h,
				}
			},
			sealedAt: func(m proto.Message) (*commonpb.Payload, string) {
				return m.(*workflowservice.SignalWorkflowExecutionRequest).GetHeader().GetFields()[HeaderCredentials], ""
			},
			info: SignalContext(ns, "wf-1", "approve", in),
		},
		{
			name: "SignalWithStartWorkflowExecution", location: "temporal.api.workflowservice.v1.SignalWithStartWorkflowExecutionRequest.header",
			method: method("SignalWithStartWorkflowExecution"),
			req: func(h *commonpb.Header, _ map[string]string) proto.Message {
				return &workflowservice.SignalWithStartWorkflowExecutionRequest{
					Namespace: ns, WorkflowId: "wf-1", WorkflowType: &commonpb.WorkflowType{Name: "Agent"},
					Input: &commonpb.Payloads{Payloads: payloads(`"start"`)}, SignalName: "approve",
					SignalInput: &commonpb.Payloads{Payloads: in}, Header: h,
				}
			},
			sealedAt: func(m proto.Message) (*commonpb.Payload, string) {
				return m.(*workflowservice.SignalWithStartWorkflowExecutionRequest).GetHeader().GetFields()[HeaderCredentials], ""
			},
			info: SignalContext(ns, "wf-1", "approve", in),
		},
		{
			name: "UpdateWorkflowExecution", location: "temporal.api.update.v1.Input.header",
			method: method("UpdateWorkflowExecution"),
			req:    func(h *commonpb.Header, _ map[string]string) proto.Message { return updateRequest(h, "u-1") },
			sealedAt: func(m proto.Message) (*commonpb.Payload, string) {
				return m.(*workflowservice.UpdateWorkflowExecutionRequest).GetRequest().GetInput().GetHeader().GetFields()[HeaderCredentials], ""
			},
			info: UpdateContext(ns, "wf-1", "approve", "u-1", payloads(`"yes"`)),
		},
		{
			name: "StartActivityExecution", location: "temporal.api.workflowservice.v1.StartActivityExecutionRequest.header",
			method: method("StartActivityExecution"),
			req: func(h *commonpb.Header, _ map[string]string) proto.Message {
				return &workflowservice.StartActivityExecutionRequest{
					Namespace: ns, ActivityId: "a-1", ActivityType: &commonpb.ActivityType{Name: "CallLLM"},
					Input: &commonpb.Payloads{Payloads: in}, Header: h,
				}
			},
			sealedAt: func(m proto.Message) (*commonpb.Payload, string) {
				return m.(*workflowservice.StartActivityExecutionRequest).GetHeader().GetFields()[HeaderCredentials], ""
			},
			info: ActivityContext(ns, "", "a-1", "CallLLM", in),
		},
		{
			name: "StartNexusOperationExecution", location: "temporal.api.workflowservice.v1.StartNexusOperationExecutionRequest.nexus_header",
			method: method("StartNexusOperationExecution"), nexus: true,
			req: func(_ *commonpb.Header, n map[string]string) proto.Message {
				return &workflowservice.StartNexusOperationExecutionRequest{
					Namespace: ns, OperationId: "op-1", Endpoint: "tools", Service: "Svc", Operation: "op",
					Input: nexusIn, NexusHeader: n,
				}
			},
			sealedAt: func(m proto.Message) (*commonpb.Payload, string) {
				return nil, m.(*workflowservice.StartNexusOperationExecutionRequest).GetNexusHeader()[HeaderCredentials]
			},
			info: NexusContext("tools", "Svc", "op", nexusIn),
		},
		{
			name: "ScheduleActivityTask", location: "temporal.api.command.v1.ScheduleActivityTaskCommandAttributes.header",
			method: method("RespondWorkflowTaskCompleted"),
			req: func(h *commonpb.Header, _ map[string]string) proto.Message {
				return completion(command(&commandpb.ScheduleActivityTaskCommandAttributes{
					ActivityId: "5", ActivityType: &commonpb.ActivityType{Name: "CallLLM"}, Input: &commonpb.Payloads{Payloads: in}, Header: h,
				}))
			},
			sealedAt: func(m proto.Message) (*commonpb.Payload, string) {
				return m.(*workflowservice.RespondWorkflowTaskCompletedRequest).GetCommands()[0].GetScheduleActivityTaskCommandAttributes().GetHeader().GetFields()[HeaderCredentials], ""
			},
			info: ActivityContext(ns, "wf-1", "5", "CallLLM", in),
		},
		{
			name: "StartChildWorkflowExecution", location: "temporal.api.command.v1.StartChildWorkflowExecutionCommandAttributes.header",
			method: method("RespondWorkflowTaskCompleted"),
			req: func(h *commonpb.Header, _ map[string]string) proto.Message {
				return completion(command(&commandpb.StartChildWorkflowExecutionCommandAttributes{
					WorkflowId: "child-1", WorkflowType: &commonpb.WorkflowType{Name: "Child"}, Input: &commonpb.Payloads{Payloads: in}, Header: h,
				}))
			},
			sealedAt: func(m proto.Message) (*commonpb.Payload, string) {
				return m.(*workflowservice.RespondWorkflowTaskCompletedRequest).GetCommands()[0].GetStartChildWorkflowExecutionCommandAttributes().GetHeader().GetFields()[HeaderCredentials], ""
			},
			info: WorkflowContext(ns, "Child", "child-1", in),
		},
		{
			name: "ContinueAsNewWorkflowExecution", location: "temporal.api.command.v1.ContinueAsNewWorkflowExecutionCommandAttributes.header",
			method: method("RespondWorkflowTaskCompleted"),
			req: func(h *commonpb.Header, _ map[string]string) proto.Message {
				return completion(command(&commandpb.ContinueAsNewWorkflowExecutionCommandAttributes{
					WorkflowType: &commonpb.WorkflowType{Name: "Agent"}, Input: &commonpb.Payloads{Payloads: in}, Header: h,
				}))
			},
			sealedAt: func(m proto.Message) (*commonpb.Payload, string) {
				return m.(*workflowservice.RespondWorkflowTaskCompletedRequest).GetCommands()[0].GetContinueAsNewWorkflowExecutionCommandAttributes().GetHeader().GetFields()[HeaderCredentials], ""
			},
			info: WorkflowContext(ns, "Agent", "wf-1", in),
		},
		{
			name: "SignalExternalWorkflowExecution", location: "temporal.api.command.v1.SignalExternalWorkflowExecutionCommandAttributes.header",
			method: method("RespondWorkflowTaskCompleted"),
			req: func(h *commonpb.Header, _ map[string]string) proto.Message {
				return completion(command(&commandpb.SignalExternalWorkflowExecutionCommandAttributes{
					Execution: &commonpb.WorkflowExecution{WorkflowId: "other-1"}, SignalName: "go", Input: &commonpb.Payloads{Payloads: in}, Header: h,
				}))
			},
			sealedAt: func(m proto.Message) (*commonpb.Payload, string) {
				return m.(*workflowservice.RespondWorkflowTaskCompletedRequest).GetCommands()[0].GetSignalExternalWorkflowExecutionCommandAttributes().GetHeader().GetFields()[HeaderCredentials], ""
			},
			info: SignalContext(ns, "other-1", "go", in),
		},
		{
			name: "ScheduleNexusOperation", location: "temporal.api.command.v1.ScheduleNexusOperationCommandAttributes.nexus_header",
			method: method("RespondWorkflowTaskCompleted"), nexus: true,
			req: func(_ *commonpb.Header, n map[string]string) proto.Message {
				return completion(command(&commandpb.ScheduleNexusOperationCommandAttributes{
					Endpoint: "tools", Service: "Svc", Operation: "op", Input: nexusIn, NexusHeader: n,
				}))
			},
			sealedAt: func(m proto.Message) (*commonpb.Payload, string) {
				return nil, m.(*workflowservice.RespondWorkflowTaskCompletedRequest).GetCommands()[0].GetScheduleNexusOperationCommandAttributes().GetNexusHeader()[HeaderCredentials]
			},
			info: NexusContext("tools", "Svc", "op", nexusIn),
		},
	}
}

func sealedLocations() []string {
	var out []string
	for _, c := range outboundCases() {
		out = append(out, c.location)
	}
	return out
}

func TestOutboundSealsEveryPosition(t *testing.T) {
	for _, c := range outboundCases() {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, false)
			req := c.req(pendingHeader(""), pendingNexus())
			original := proto.Clone(req)

			sent, err := h.send(c.method, req)
			require.NoError(t, err)
			require.True(t, proto.Equal(original, req), "the original request changed")

			p, n := c.sealedAt(sent)
			if c.nexus {
				require.True(t, strings.HasPrefix(n, SealedPrefix))
				got, err := h.ks.Open(c.info, n)
				require.NoError(t, err)
				require.Equal(t, creds, got)
			} else {
				h.requireSealed(p, c.info)
			}
			requireNoPlaintext(t, sent)

			// A named key wins over the default.
			sent, err = h.send(c.method, c.req(pendingHeader("payments"), withKey(pendingNexus(), "payments")))
			require.NoError(t, err)
			p, n = c.sealedAt(sent)
			if c.nexus {
				n2 := n
				id, _ := SealedKeyID(n2)
				require.Equal(t, h.f.Keys[1].ID, hexID(id))
			} else {
				id, _ := SealedKeyID(string(p.GetData()))
				require.Equal(t, h.f.Keys[1].ID, hexID(id))
			}
			requireNoPlaintext(t, sent)
		})
	}
}

func withKey(n map[string]string, key string) map[string]string {
	n[HeaderKey] = KeyNexus(key)
	return n
}

// requireNoPlaintext checks that nothing but sealed values is left in m.
func requireNoPlaintext(t *testing.T, m proto.Message) {
	t.Helper()
	walk(m.ProtoReflect(), &plaintextCheck{t: t})
}

type plaintextCheck struct{ t *testing.T }

func (c *plaintextCheck) header(h *commonpb.Header) bool {
	for k, p := range h.GetFields() {
		switch k {
		case HeaderKey, HeaderOpened:
			c.t.Errorf("%s left in a header", k)
		case HeaderCredentials:
			if !isSealedPayload(p) {
				c.t.Errorf("unsealed credentials left in a header: %v", p)
			}
			if _, ok := p.GetMetadata()[HeaderOpened]; ok {
				c.t.Errorf("opened credentials left in a header")
			}
		}
	}
	return false
}

func (c *plaintextCheck) nexus(_ protoreflect.Message, h protoreflect.Map) bool {
	for _, k := range nexusFamilyKeys(h) {
		if k.String() != HeaderCredentials || !strings.HasPrefix(h.Get(k).String(), SealedPrefix) {
			c.t.Errorf("Nexus header %s left: %s", k.String(), h.Get(k).String())
		}
	}
	return false
}

func (c *plaintextCheck) skipAny(*anypb.Any) bool { return false }

func TestExecuteMultiOperationSealsEachOperation(t *testing.T) {
	h := newHarness(t, false)
	// UpdateWithStart's start and Update requests share one header map.
	shared := pendingHeader("")
	req := &workflowservice.ExecuteMultiOperationRequest{Namespace: ns, Operations: []*workflowservice.ExecuteMultiOperationRequest_Operation{
		{Operation: &workflowservice.ExecuteMultiOperationRequest_Operation_StartWorkflow{StartWorkflow: startRequest(shared)}},
		{Operation: &workflowservice.ExecuteMultiOperationRequest_Operation_UpdateWorkflow{UpdateWorkflow: updateRequest(shared, "u-1")}},
	}}
	sent, err := h.send(method("ExecuteMultiOperation"), req)
	require.NoError(t, err)
	r := sent.(*workflowservice.ExecuteMultiOperationRequest)
	start := r.GetOperations()[0].GetStartWorkflow().GetHeader().GetFields()[HeaderCredentials]
	update := r.GetOperations()[1].GetUpdateWorkflow().GetRequest().GetInput().GetHeader().GetFields()[HeaderCredentials]
	h.requireSealed(start, WorkflowContext(ns, "Agent", "wf-1", in))
	h.requireSealed(update, UpdateContext(ns, "wf-1", "approve", "u-1", payloads(`"yes"`)))
	require.NotEqual(t, start.GetData(), update.GetData())
	require.Equal(t, EncodingPending, payloadEncoding(shared.GetFields()[HeaderCredentials]), "the shared map changed")
}

func TestResendReseals(t *testing.T) {
	// Retries and completion pagination resend the same request object.
	h := newHarness(t, false)
	req := completion(command(&commandpb.ScheduleActivityTaskCommandAttributes{
		ActivityId: "5", ActivityType: &commonpb.ActivityType{Name: "CallLLM"}, Input: &commonpb.Payloads{Payloads: in}, Header: pendingHeader(""),
	}))
	req.IntermediatePage = true
	info := ActivityContext(ns, "wf-1", "5", "CallLLM", in)
	var seen []string
	for range 2 {
		sent, err := h.send(method("RespondWorkflowTaskCompleted"), req)
		require.NoError(t, err)
		p := sent.(*workflowservice.RespondWorkflowTaskCompletedRequest).GetCommands()[0].GetScheduleActivityTaskCommandAttributes().GetHeader().GetFields()[HeaderCredentials]
		h.requireSealed(p, info)
		seen = append(seen, string(p.GetData()))
	}
	require.NotEqual(t, seen[0], seen[1])
}

func TestSealingErrors(t *testing.T) {
	sched := func(h *commonpb.Header) *workflowservice.RespondWorkflowTaskCompletedRequest {
		return completion(command(&commandpb.ScheduleActivityTaskCommandAttributes{
			ActivityId: "5", ActivityType: &commonpb.ActivityType{Name: "CallLLM"}, Header: h,
		}))
	}
	encrypted := pendingHeader("")
	encrypted.Fields[HeaderCredentials].Metadata["encoding"] = []byte("binary/encrypted")
	badJSON := pendingHeader("")
	badJSON.Fields[HeaderCredentials].Data = []byte(`["x"]`)
	badResource := sched(pendingHeader(""))
	badResource.ResourceId = ""

	for name, tc := range map[string]struct {
		strict bool
		method string
		req    proto.Message
		want   string
	}{
		"unknown key":     {false, method("StartWorkflowExecution"), startRequest(pendingHeader("nope")), `sealing StartWorkflowExecution wf-1: unknown key name "nope"`},
		"strict":          {true, method("StartWorkflowExecution"), startRequest(pendingHeader("")), "sealing StartWorkflowExecution wf-1: no key selected"},
		"codec encoded":   {false, method("StartWorkflowExecution"), startRequest(encrypted), `has encoding "binary/encrypted", not json/credentials-pending: a payload codec or visitor changed it. gRPC-level payload codecs must pass through payloads for which binding.IsCredentialsPayload is true`},
		"malformed":       {false, method("StartWorkflowExecution"), startRequest(badJSON), "pending credentials must be a JSON object of strings"},
		"no resource_id":  {false, method("RespondWorkflowTaskCompleted"), badResource, `sealing ScheduleActivityTask activity 5: resource_id is "", not workflow:<id>`},
		"no update ID":    {false, method("UpdateWorkflowExecution"), updateRequest(pendingHeader(""), ""), "the Update has no ID"},
		"nexus malformed": {false, method("StartNexusOperationExecution"), &workflowservice.StartNexusOperationExecutionRequest{OperationId: "op-1", NexusHeader: map[string]string{HeaderCredentials: "eyJ"}}, "sealing StartNexusOperationExecution op-1: pending credentials must be a JSON object of strings"},
	} {
		h := newHarness(t, tc.strict)
		_, err := h.send(tc.method, tc.req)
		require.Equal(t, codes.FailedPrecondition, status.Code(err), name)
		require.ErrorContains(t, err, "credentials: ", name)
		require.ErrorContains(t, err, tc.want, name)
	}

	// Strict mode still seals with a Nexus endpoint override.
	h := newHarness(t, true)
	_, err := h.send(method("RespondWorkflowTaskCompleted"), completion(command(&commandpb.ScheduleNexusOperationCommandAttributes{
		Endpoint: "payments-ep", Service: "Svc", Operation: "op", NexusHeader: pendingNexus(),
	})))
	require.NoError(t, err)
}

func TestOutboundStripsEverythingElse(t *testing.T) {
	h := newHarness(t, false)
	opened := OpenedPayload(h.seal(UpdateContext(ns, "wf-1", "approve", "u-1", nil), ""), Opened{OK: true, Credentials: creds})
	echoHeader := &commonpb.Header{Fields: map[string]*commonpb.Payload{HeaderCredentials: opened}}
	echo, err := anypb.New(&updatepb.Acceptance{AcceptedRequest: &updatepb.Request{
		Meta: &updatepb.Meta{UpdateId: "u-1"}, Input: &updatepb.Input{Name: "approve", Header: echoHeader},
	}})
	require.NoError(t, err)
	local := &commonpb.Header{Fields: map[string]*commonpb.Payload{HeaderCredentials: LocalPayload(creds)}}
	copied := &commonpb.Header{Fields: map[string]*commonpb.Payload{HeaderCredentials: proto.Clone(opened).(*commonpb.Payload)}}
	keyOnly := &commonpb.Header{Fields: map[string]*commonpb.Payload{HeaderKey: KeyPayload("agents"), HeaderOpened: payload("{}")}}

	req := completion(
		command(&commandpb.RecordMarkerCommandAttributes{MarkerName: "LocalActivity", Header: local}),
		command(&commandpb.ScheduleActivityTaskCommandAttributes{ActivityId: "1", ActivityType: &commonpb.ActivityType{Name: "A"}, Header: copied}),
		command(&commandpb.ScheduleActivityTaskCommandAttributes{ActivityId: "2", ActivityType: &commonpb.ActivityType{Name: "A"}, Header: keyOnly}),
		command(&commandpb.ScheduleNexusOperationCommandAttributes{Endpoint: "tools", NexusHeader: map[string]string{
			"Credentials": PendingNexus(creds), HeaderOpened: "{}", "other": "kept",
		}}),
		command(&commandpb.ProtocolMessageCommandAttributes{MessageId: "m-1"}),
	)
	req.Messages = []*protocolpb.Message{{Id: "m-1", ProtocolInstanceId: "u-1", Body: echo}}

	sent, err := h.send(method("RespondWorkflowTaskCompleted"), req)
	require.NoError(t, err)
	requireNoPlaintext(t, sent)
	r := sent.(*workflowservice.RespondWorkflowTaskCompletedRequest)
	require.Empty(t, r.GetCommands()[0].GetRecordMarkerCommandAttributes().GetHeader().GetFields())
	require.Empty(t, r.GetCommands()[1].GetScheduleActivityTaskCommandAttributes().GetHeader().GetFields(), "a copied opened result is never sealed")
	require.Empty(t, r.GetCommands()[2].GetScheduleActivityTaskCommandAttributes().GetHeader().GetFields())
	require.Equal(t, map[string]string{"other": "kept"}, r.GetCommands()[3].GetScheduleNexusOperationCommandAttributes().GetNexusHeader())
	var acc updatepb.Acceptance
	require.NoError(t, r.GetMessages()[0].GetBody().UnmarshalTo(&acc))
	require.Empty(t, acc.GetAcceptedRequest().GetInput().GetHeader().GetFields(), "the Update echo still carries credentials")

	// Positions with no context: a Query.
	sent, err = h.send(method("QueryWorkflow"), &workflowservice.QueryWorkflowRequest{
		Namespace: ns, Query: &querypb.WorkflowQuery{QueryType: "q", Header: pendingHeader("")},
	})
	require.NoError(t, err)
	requireNoPlaintext(t, sent)
	require.Contains(t, h.logs.String(), "Dropping credentials from a call with no context")
	require.NotContains(t, h.logs.String(), "eyJ")
}

func TestOutboundPassesThroughWithoutCredentials(t *testing.T) {
	h := newHarness(t, false)
	req := startRequest(&commonpb.Header{Fields: map[string]*commonpb.Payload{"other": payload(`"x"`)}})
	sent, err := h.send(method("StartWorkflowExecution"), req)
	require.NoError(t, err)
	require.Same(t, req, sent, "a request without credentials is cloned")
}

// Inbound.

type inboundCase struct {
	name      string
	locations []string
	method    string
	req       proto.Message
	// reply builds a reply whose header at the position carries value, a
	// credentials payload sealed under the position's context (or another
	// value for the strip tests).
	reply  func(h *harness, p *commonpb.Payload, n string) proto.Message
	header func(m proto.Message) *commonpb.Header
	nexus  bool
}

func pollResponse(events ...*historypb.HistoryEvent) *workflowservice.PollWorkflowTaskQueueResponse {
	return &workflowservice.PollWorkflowTaskQueueResponse{
		WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: "wf-1"},
		History:           &historypb.History{Events: events},
	}
}

func startedEvent(id int64, h *commonpb.Header) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{EventId: id, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
			WorkflowType: &commonpb.WorkflowType{Name: "Agent"}, Input: &commonpb.Payloads{Payloads: in}, Header: h,
		}}}
}

func signaledEvent(id int64, h *commonpb.Header) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{EventId: id, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionSignaledEventAttributes{WorkflowExecutionSignaledEventAttributes: &historypb.WorkflowExecutionSignaledEventAttributes{
			SignalName: "approve", Input: &commonpb.Payloads{Payloads: in}, Header: h,
		}}}
}

func updateReq(h *commonpb.Header) *updatepb.Request {
	return &updatepb.Request{Meta: &updatepb.Meta{UpdateId: "u-1"}, Input: &updatepb.Input{Name: "approve", Args: &commonpb.Payloads{Payloads: in}, Header: h}}
}

func activityTask(h *commonpb.Header) *workflowservice.PollActivityTaskQueueResponse {
	return &workflowservice.PollActivityTaskQueueResponse{
		WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: "wf-1"}, ActivityId: "5",
		ActivityType: &commonpb.ActivityType{Name: "CallLLM"}, Input: &commonpb.Payloads{Payloads: in}, Header: h,
	}
}

func headerWith(p *commonpb.Payload) *commonpb.Header {
	return &commonpb.Header{Fields: map[string]*commonpb.Payload{HeaderCredentials: p, "other": payload(`"kept"`)}}
}

var (
	wfInfo       = WorkflowContext(ns, "Agent", "wf-1", in)
	signalInfo   = SignalContext(ns, "wf-1", "approve", in)
	updateInfo   = UpdateContext(ns, "wf-1", "approve", "u-1", in)
	activityInfo = ActivityContext(ns, "wf-1", "5", "CallLLM", in)
	nexusIn      = payload(`{"x":1}`)
	nexusInfo    = NexusContext("tools", "Svc", "op", nexusIn)
)

func inboundCases() []inboundCase {
	poll := &workflowservice.PollWorkflowTaskQueueRequest{Namespace: ns}
	return []inboundCase{
		{
			name: "Started", locations: []string{"temporal.api.history.v1.WorkflowExecutionStartedEventAttributes.header"},
			method: method("PollWorkflowTaskQueue"), req: poll,
			reply: func(h *harness, p *commonpb.Payload, _ string) proto.Message {
				return pollResponse(startedEvent(1, headerWith(p)))
			},
			header: func(m proto.Message) *commonpb.Header {
				return m.(*workflowservice.PollWorkflowTaskQueueResponse).GetHistory().GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetHeader()
			},
		},
		{
			name: "Signaled", locations: []string{"temporal.api.history.v1.WorkflowExecutionSignaledEventAttributes.header"},
			method: method("PollWorkflowTaskQueue"), req: poll,
			reply: func(h *harness, p *commonpb.Payload, _ string) proto.Message {
				return pollResponse(signaledEvent(3, headerWith(p)))
			},
			header: func(m proto.Message) *commonpb.Header {
				return m.(*workflowservice.PollWorkflowTaskQueueResponse).GetHistory().GetEvents()[0].GetWorkflowExecutionSignaledEventAttributes().GetHeader()
			},
		},
		{
			name: "UpdateAdmitted", locations: []string{"temporal.api.update.v1.Input.header"},
			method: method("PollWorkflowTaskQueue"), req: poll,
			reply: func(h *harness, p *commonpb.Payload, _ string) proto.Message {
				return pollResponse(&historypb.HistoryEvent{EventId: 3, Attributes: &historypb.HistoryEvent_WorkflowExecutionUpdateAdmittedEventAttributes{
					WorkflowExecutionUpdateAdmittedEventAttributes: &historypb.WorkflowExecutionUpdateAdmittedEventAttributes{Request: updateReq(headerWith(p))},
				}})
			},
			header: func(m proto.Message) *commonpb.Header {
				return m.(*workflowservice.PollWorkflowTaskQueueResponse).GetHistory().GetEvents()[0].GetWorkflowExecutionUpdateAdmittedEventAttributes().GetRequest().GetInput().GetHeader()
			},
		},
		{
			name: "UpdateAccepted", locations: []string{"temporal.api.update.v1.Input.header"},
			method: method("PollWorkflowTaskQueue"), req: poll,
			reply: func(h *harness, p *commonpb.Payload, _ string) proto.Message {
				return pollResponse(&historypb.HistoryEvent{EventId: 3, Attributes: &historypb.HistoryEvent_WorkflowExecutionUpdateAcceptedEventAttributes{
					WorkflowExecutionUpdateAcceptedEventAttributes: &historypb.WorkflowExecutionUpdateAcceptedEventAttributes{AcceptedRequest: updateReq(headerWith(p))},
				}})
			},
			header: func(m proto.Message) *commonpb.Header {
				return m.(*workflowservice.PollWorkflowTaskQueueResponse).GetHistory().GetEvents()[0].GetWorkflowExecutionUpdateAcceptedEventAttributes().GetAcceptedRequest().GetInput().GetHeader()
			},
		},
		{
			name: "Update message", locations: []string{"temporal.api.update.v1.Input.header"},
			method: method("PollWorkflowTaskQueue"), req: poll,
			reply: func(h *harness, p *commonpb.Payload, _ string) proto.Message {
				body, err := anypb.New(updateReq(headerWith(p)))
				require.NoError(h.t, err)
				r := pollResponse()
				r.Messages = []*protocolpb.Message{{Id: "m", ProtocolInstanceId: "u-1", Body: body}}
				return r
			},
			header: func(m proto.Message) *commonpb.Header {
				var r updatepb.Request
				if err := m.(*workflowservice.PollWorkflowTaskQueueResponse).GetMessages()[0].GetBody().UnmarshalTo(&r); err != nil {
					panic(err)
				}
				return r.GetInput().GetHeader()
			},
		},
		{
			name: "RespondWorkflowTaskCompleted.workflow_task", locations: []string{"temporal.api.history.v1.WorkflowExecutionStartedEventAttributes.header"},
			method: method("RespondWorkflowTaskCompleted"), req: &workflowservice.RespondWorkflowTaskCompletedRequest{Namespace: ns},
			reply: func(h *harness, p *commonpb.Payload, _ string) proto.Message {
				return &workflowservice.RespondWorkflowTaskCompletedResponse{WorkflowTask: pollResponse(startedEvent(1, headerWith(p)))}
			},
			header: func(m proto.Message) *commonpb.Header {
				return m.(*workflowservice.RespondWorkflowTaskCompletedResponse).GetWorkflowTask().GetHistory().GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetHeader()
			},
		},
		{
			name: "eager_workflow_task", locations: []string{"temporal.api.history.v1.WorkflowExecutionStartedEventAttributes.header"},
			method: method("StartWorkflowExecution"), req: &workflowservice.StartWorkflowExecutionRequest{Namespace: ns},
			reply: func(h *harness, p *commonpb.Payload, _ string) proto.Message {
				return &workflowservice.StartWorkflowExecutionResponse{EagerWorkflowTask: pollResponse(startedEvent(1, headerWith(p)))}
			},
			header: func(m proto.Message) *commonpb.Header {
				return m.(*workflowservice.StartWorkflowExecutionResponse).GetEagerWorkflowTask().GetHistory().GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetHeader()
			},
		},
		{
			name: "GetWorkflowExecutionHistory", locations: []string{"temporal.api.history.v1.WorkflowExecutionStartedEventAttributes.header"},
			method: method("GetWorkflowExecutionHistory"),
			req:    &workflowservice.GetWorkflowExecutionHistoryRequest{Namespace: ns, Execution: &commonpb.WorkflowExecution{WorkflowId: "wf-1"}},
			reply: func(h *harness, p *commonpb.Payload, _ string) proto.Message {
				return &workflowservice.GetWorkflowExecutionHistoryResponse{History: &historypb.History{Events: []*historypb.HistoryEvent{startedEvent(1, headerWith(p))}}}
			},
			header: func(m proto.Message) *commonpb.Header {
				return m.(*workflowservice.GetWorkflowExecutionHistoryResponse).GetHistory().GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetHeader()
			},
		},
		{
			name: "PollActivityTaskQueue", locations: []string{"temporal.api.workflowservice.v1.PollActivityTaskQueueResponse.header"},
			method: method("PollActivityTaskQueue"), req: &workflowservice.PollActivityTaskQueueRequest{Namespace: ns},
			reply: func(h *harness, p *commonpb.Payload, _ string) proto.Message { return activityTask(headerWith(p)) },
			header: func(m proto.Message) *commonpb.Header {
				return m.(*workflowservice.PollActivityTaskQueueResponse).GetHeader()
			},
		},
		{
			name: "eager activity", locations: []string{"temporal.api.workflowservice.v1.PollActivityTaskQueueResponse.header"},
			method: method("RespondWorkflowTaskCompleted"), req: &workflowservice.RespondWorkflowTaskCompletedRequest{Namespace: ns},
			reply: func(h *harness, p *commonpb.Payload, _ string) proto.Message {
				return &workflowservice.RespondWorkflowTaskCompletedResponse{ActivityTasks: []*workflowservice.PollActivityTaskQueueResponse{activityTask(headerWith(p))}}
			},
			header: func(m proto.Message) *commonpb.Header {
				return m.(*workflowservice.RespondWorkflowTaskCompletedResponse).GetActivityTasks()[0].GetHeader()
			},
		},
		{
			name: "PollNexusTaskQueue", locations: []string{"temporal.api.nexus.v1.Request.header"}, nexus: true,
			method: method("PollNexusTaskQueue"), req: &workflowservice.PollNexusTaskQueueRequest{Namespace: ns},
			reply: func(h *harness, _ *commonpb.Payload, n string) proto.Message {
				return nexusTask("tools", map[string]string{HeaderCredentials: n, "other": "kept"})
			},
		},
	}
}

func nexusTask(endpoint string, header map[string]string) *workflowservice.PollNexusTaskQueueResponse {
	return &workflowservice.PollNexusTaskQueueResponse{Request: &nexuspb.Request{
		Endpoint: endpoint, Header: header,
		Variant: &nexuspb.Request_StartOperation{StartOperation: &nexuspb.StartOperationRequest{Service: "Svc", Operation: "op", Payload: nexusIn}},
	}}
}

func infoFor(name string) []byte {
	switch name {
	case "Signaled":
		return signalInfo
	case "UpdateAdmitted", "UpdateAccepted", "Update message":
		return updateInfo
	case "PollActivityTaskQueue", "eager activity":
		return activityInfo
	case "PollNexusTaskQueue":
		return nexusInfo
	}
	return wfInfo
}

func openedLocations() []string {
	var out []string
	for _, c := range inboundCases() {
		out = append(out, c.locations...)
	}
	return out
}

func nexusHeaderOf(m proto.Message) map[string]string {
	return m.(*workflowservice.PollNexusTaskQueueResponse).GetRequest().GetHeader()
}

func TestInboundOpensEveryPosition(t *testing.T) {
	for _, c := range inboundCases() {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, false)
			sealed := h.seal(infoFor(c.name), "")

			// Opens, next to the sealed bytes.
			got := h.receive(c.method, c.req, c.reply(h, SealedPayload(sealed), sealed))
			if c.nexus {
				hdr := nexusHeaderOf(got)
				require.Equal(t, sealed, hdr[HeaderCredentials])
				require.Equal(t, "kept", hdr["other"])
				o, ok := OpenedFromNexus(hdr)
				require.True(t, ok)
				require.True(t, o.OK, o.Reason)
				require.Equal(t, creds, o.Credentials)
			} else {
				hdr := c.header(got)
				require.Equal(t, sealed, string(hdr.GetFields()[HeaderCredentials].GetData()))
				require.Equal(t, `"kept"`, string(hdr.GetFields()["other"].GetData()))
				requireOpened(t, hdr)
			}

			// An injected opened result is replaced by the real one: here, a
			// header sealed for another call claims to carry credentials.
			other := h.seal(WorkflowContext(ns, "Agent", "other-wf", in), "")
			forged := OpenedPayload(other, Opened{OK: true, Credentials: map[string]string{"user_token": "forged"}})
			got = h.receive(c.method, c.req, c.reply(h, forged, other))
			if c.nexus {
				hdr := nexusHeaderOf(got)
				o, _ := OpenedFromNexus(hdr)
				require.False(t, o.OK)
				require.Equal(t, ReasonDoesNotOpen, o.Reason)
			} else {
				requireFailed(t, c.header(got), ReasonDoesNotOpen)
			}

			// A plaintext value is replaced with a failure marker, and its
			// plaintext never reaches the application.
			got = h.receive(c.method, c.req, c.reply(h, PendingPayload(creds), PendingNexus(creds)))
			if c.nexus {
				hdr := nexusHeaderOf(got)
				_, ok := hdr[HeaderCredentials]
				require.False(t, ok)
				o, _ := OpenedFromNexus(hdr)
				require.Equal(t, ReasonMalformed, o.Reason)
			} else {
				hdr := c.header(got)
				requireFailed(t, hdr, ReasonMalformed)
				require.Empty(t, hdr.GetFields()[HeaderCredentials].GetData())
			}
			requireNoPlaintextInbound(t, got)
			require.NotContains(t, h.logs.String(), "eyJ")
		})
	}
}

// requireNoPlaintextInbound checks that m carries no credentials outside
// sealed values and opened results.
func requireNoPlaintextInbound(t *testing.T, m proto.Message) {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	require.NotContains(t, string(b), "eyJ-user\"", "plaintext reached the application")
}

func TestInboundNexusInjectedOpened(t *testing.T) {
	h := newHarness(t, false)
	sealed := h.seal(nexusInfo, "")
	got := h.receive(method("PollNexusTaskQueue"), &workflowservice.PollNexusTaskQueueRequest{Namespace: ns}, nexusTask("tools", map[string]string{
		HeaderCredentials: sealed, "Credentials-Opened": `{"ok":true,"credentials":{"user_token":"forged"}}`, HeaderKey: `"x"`,
	}))
	hdr := nexusHeaderOf(got)
	require.Len(t, hdr, 2)
	o, ok := OpenedFromNexus(hdr)
	require.True(t, ok)
	require.Equal(t, creds, o.Credentials)
}

func TestInboundNexusWithoutEndpoint(t *testing.T) {
	h := newHarness(t, false)
	sealed := h.seal(nexusInfo, "")
	got := h.receive(method("PollNexusTaskQueue"), &workflowservice.PollNexusTaskQueueRequest{Namespace: ns}, nexusTask("", map[string]string{HeaderCredentials: sealed}))
	o, ok := OpenedFromNexus(nexusHeaderOf(got))
	require.True(t, ok)
	require.Equal(t, ReasonNoEndpoint, o.Reason)
	require.Contains(t, h.logs.String(), "reason=no-endpoint")
}

func TestInboundKeyNotHeld(t *testing.T) {
	h := newHarness(t, false)
	other, _ := newKeyset(t, "x", "x")
	k, _ := other.SealingKey("", "", false)
	sealed, err := Seal(k, activityInfo, creds)
	require.NoError(t, err)
	got := h.receive(method("PollActivityTaskQueue"), &workflowservice.PollActivityTaskQueueRequest{Namespace: ns}, activityTask(headerWith(SealedPayload(sealed))))
	requireFailed(t, got.(*workflowservice.PollActivityTaskQueueResponse).GetHeader(), ReasonKeyNotHeld)
	logs := h.logs.String()
	require.Contains(t, logs, "level=WARN")
	for _, want := range []string{"rpc=PollActivityTaskQueue", "header=activity", "namespace=default", "workflow_id=wf-1", "id=5", "reason=key-not-held", "key_id="} {
		require.Contains(t, logs, want)
	}
}

func TestInboundCopiedToAnotherWorkflow(t *testing.T) {
	h := newHarness(t, false)
	sealed := h.seal(WorkflowContext(ns, "Agent", "wf-2", in), "")
	r := pollResponse(startedEvent(1, headerWith(SealedPayload(sealed))))
	got := h.receive(method("PollWorkflowTaskQueue"), &workflowservice.PollWorkflowTaskQueueRequest{Namespace: ns}, r)
	requireFailed(t, got.(*workflowservice.PollWorkflowTaskQueueResponse).GetHistory().GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetHeader(), ReasonDoesNotOpen)
	require.Contains(t, h.logs.String(), "event_id=1")
}

func TestInboundProcessedHistoryLogsError(t *testing.T) {
	h := newHarness(t, false)
	other, _ := newKeyset(t, "x", "x")
	k, _ := other.SealingKey("", "", false)
	sealed, err := Seal(k, signalInfo, creds)
	require.NoError(t, err)
	r := pollResponse(signaledEvent(3, headerWith(SealedPayload(sealed))))
	r.PreviousStartedEventId = 4
	h.receive(method("PollWorkflowTaskQueue"), &workflowservice.PollWorkflowTaskQueueRequest{Namespace: ns}, r)
	require.Contains(t, h.logs.String(), "level=ERROR")
	require.Contains(t, h.logs.String(), "private key retired")
}

func TestSignalWithStartSkipsTheStart(t *testing.T) {
	h := newHarness(t, false)
	sent, err := h.send(method("SignalWithStartWorkflowExecution"), &workflowservice.SignalWithStartWorkflowExecutionRequest{
		Namespace: ns, WorkflowId: "wf-1", WorkflowType: &commonpb.WorkflowType{Name: "Agent"},
		Input: &commonpb.Payloads{Payloads: in}, SignalName: "approve", SignalInput: &commonpb.Payloads{Payloads: in}, Header: pendingHeader(""),
	})
	require.NoError(t, err)
	// The server writes the one header to both events.
	header := sent.(*workflowservice.SignalWithStartWorkflowExecutionRequest).GetHeader()
	r := pollResponse(startedEvent(1, proto.Clone(header).(*commonpb.Header)), signaledEvent(2, proto.Clone(header).(*commonpb.Header)))
	got := h.receive(method("PollWorkflowTaskQueue"), &workflowservice.PollWorkflowTaskQueueRequest{Namespace: ns}, r).(*workflowservice.PollWorkflowTaskQueueResponse)

	start := got.GetHistory().GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetHeader()
	_, ok := OpenedFromHeader(start.GetFields())
	require.False(t, ok, "the start carries no credentials and no failure marker")
	requireOpened(t, got.GetHistory().GetEvents()[1].GetWorkflowExecutionSignaledEventAttributes().GetHeader())
	require.Empty(t, h.logs.String())
}

func TestInboundStripsEverywhereElse(t *testing.T) {
	h := newHarness(t, false)
	sealed := h.seal(activityInfo, "")
	scheduled := &historypb.HistoryEvent{EventId: 5, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED,
		Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{
			ActivityId: "5", Header: &commonpb.Header{Fields: map[string]*commonpb.Payload{
				HeaderCredentials: OpenedPayload(sealed, Opened{OK: true, Credentials: creds}), HeaderKey: KeyPayload("x"),
			}},
		}}}
	marker := &historypb.HistoryEvent{EventId: 6, EventType: enumspb.EVENT_TYPE_MARKER_RECORDED,
		Attributes: &historypb.HistoryEvent_MarkerRecordedEventAttributes{MarkerRecordedEventAttributes: &historypb.MarkerRecordedEventAttributes{
			Header: &commonpb.Header{Fields: map[string]*commonpb.Payload{HeaderCredentials: LocalPayload(creds)}},
		}}}
	nexusScheduled := &historypb.HistoryEvent{EventId: 7, EventType: enumspb.EVENT_TYPE_NEXUS_OPERATION_SCHEDULED,
		Attributes: &historypb.HistoryEvent_NexusOperationScheduledEventAttributes{NexusOperationScheduledEventAttributes: &historypb.NexusOperationScheduledEventAttributes{
			NexusHeader: map[string]string{HeaderCredentials: sealed, HeaderOpened: "{}", "CREDENTIALS-KEY": `"x"`},
		}}}
	pendingNexusEvent := &historypb.HistoryEvent{EventId: 8, EventType: enumspb.EVENT_TYPE_NEXUS_OPERATION_SCHEDULED,
		Attributes: &historypb.HistoryEvent_NexusOperationScheduledEventAttributes{NexusOperationScheduledEventAttributes: &historypb.NexusOperationScheduledEventAttributes{
			NexusHeader: map[string]string{HeaderCredentials: PendingNexus(creds)},
		}}}
	r := pollResponse(scheduled, marker, nexusScheduled, pendingNexusEvent)
	r.Query = &querypb.WorkflowQuery{Header: pendingHeader("x")}
	got := h.receive(method("PollWorkflowTaskQueue"), &workflowservice.PollWorkflowTaskQueueRequest{Namespace: ns}, r).(*workflowservice.PollWorkflowTaskQueueResponse)

	events := got.GetHistory().GetEvents()
	sched := events[0].GetActivityTaskScheduledEventAttributes().GetHeader().GetFields()
	require.Len(t, sched, 1)
	require.Equal(t, sealed, string(sched[HeaderCredentials].GetData()), "sealed values elsewhere stay")
	_, ok := sched[HeaderCredentials].GetMetadata()[HeaderOpened]
	require.False(t, ok)
	require.Empty(t, events[1].GetMarkerRecordedEventAttributes().GetHeader().GetFields())
	require.Equal(t, map[string]string{HeaderCredentials: sealed}, events[2].GetNexusOperationScheduledEventAttributes().GetNexusHeader())
	require.Empty(t, events[3].GetNexusOperationScheduledEventAttributes().GetNexusHeader())
	require.Equal(t, map[string]*commonpb.Payload{"other": payload(`"kept"`)}, got.GetQuery().GetHeader().GetFields())
	requireNoPlaintextInbound(t, got)

	// GetWorkflowExecutionHistoryReverse isn't opened; it's only stripped.
	hist := &workflowservice.GetWorkflowExecutionHistoryReverseResponse{History: &historypb.History{Events: []*historypb.HistoryEvent{
		startedEvent(1, headerWith(OpenedPayload(h.seal(wfInfo, ""), Opened{OK: true, Credentials: creds}))),
	}}}
	gotRev := h.receive(method("GetWorkflowExecutionHistoryReverse"), &workflowservice.GetWorkflowExecutionHistoryReverseRequest{Namespace: ns}, hist).(*workflowservice.GetWorkflowExecutionHistoryReverseResponse)
	_, ok = OpenedFromHeader(gotRev.GetHistory().GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetHeader().GetFields())
	require.False(t, ok)
}

func TestProbeIsAnsweredLocally(t *testing.T) {
	h := newHarness(t, false)
	ctx, answered := WithProbe(context.Background())
	require.False(t, answered())
	err := h.ic(ctx, method("GetSystemInfo"), &workflowservice.GetSystemInfoRequest{}, &workflowservice.GetSystemInfoResponse{}, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			t.Fatal("the probe reached the server")
			return nil
		})
	require.NoError(t, err)
	require.True(t, answered())
	require.False(t, AnswerProbe(context.Background()))
}

func TestIsCredentialsPayload(t *testing.T) {
	for _, p := range []*commonpb.Payload{PendingPayload(creds), KeyPayload("k"), LocalPayload(creds), SealedPayload("v1.start.x")} {
		require.True(t, IsCredentialsPayload(p))
	}
	require.False(t, IsCredentialsPayload(payload("x")))
	require.False(t, IsCredentialsPayload(nil))
}

func hexID(id KeyID) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(id))
	for _, b := range id {
		out = append(out, digits[b>>4], digits[b&0xf])
	}
	return string(out)
}
