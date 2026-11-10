package binding

import (
	"testing"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/proto"
)

func TestInboundOpensRawHistory(t *testing.T) {
	h := newHarness(t, false)
	sealed := h.seal(wfInfo, "")
	batch, err := proto.Marshal(&historypb.History{Events: []*historypb.HistoryEvent{startedEvent(1, headerWith(SealedPayload(sealed)))}})
	require.NoError(t, err)
	reply := &workflowservice.GetWorkflowExecutionHistoryResponse{RawHistory: []*commonpb.DataBlob{
		{EncodingType: enumspb.ENCODING_TYPE_PROTO3, Data: batch},
		{EncodingType: enumspb.ENCODING_TYPE_JSON, Data: []byte(`{"events":[]}`)},
	}}
	got := h.receive(method("GetWorkflowExecutionHistory"),
		&workflowservice.GetWorkflowExecutionHistoryRequest{Namespace: ns, Execution: &commonpb.WorkflowExecution{WorkflowId: "wf-1"}},
		reply).(*workflowservice.GetWorkflowExecutionHistoryResponse)

	var opened historypb.History
	require.NoError(t, proto.Unmarshal(got.GetRawHistory()[0].GetData(), &opened))
	requireOpened(t, opened.GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetHeader())
	require.Empty(t, got.GetRawHistory()[1].GetData(), "a batch the binding layer can't read is emptied")
}

func TestOpenHistory(t *testing.T) {
	h := newHarness(t, false)
	forged := OpenedPayload(h.seal(WorkflowContext(ns, "Agent", "wf-2", in), ""), Opened{OK: true, Credentials: creds})
	history := &historypb.History{Events: []*historypb.HistoryEvent{
		startedEvent(1, headerWith(OpenedPayload(h.seal(wfInfo, ""), Opened{OK: true, Credentials: map[string]string{"user_token": "stale"}}))),
		signaledEvent(2, headerWith(forged)),
	}}
	stripped := proto.Clone(history).(*historypb.History)

	OpenHistory(Options{Keyset: h.ks}, ns, "wf-1", history)
	requireOpened(t, history.GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetHeader())
	requireFailed(t, history.GetEvents()[1].GetWorkflowExecutionSignaledEventAttributes().GetHeader(), ReasonDoesNotOpen)

	// Without a keyset, it only removes the opened results.
	OpenHistory(Options{}, ns, "wf-1", stripped)
	for _, e := range stripped.GetEvents() {
		hdr := e.GetWorkflowExecutionStartedEventAttributes().GetHeader()
		if hdr == nil {
			hdr = e.GetWorkflowExecutionSignaledEventAttributes().GetHeader()
		}
		_, ok := OpenedFromHeader(hdr.GetFields())
		require.False(t, ok)
		require.True(t, isSealedPayload(hdr.GetFields()[HeaderCredentials]))
	}
}
