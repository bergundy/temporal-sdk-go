package binding

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// headerLocations lists every field holding a Temporal header or a Nexus
// header map that a WorkflowService request or response can reach, through
// message fields and through Any bodies holding Temporal API types, as
// "message.field". It also lists every other map<string, string> field with
// "header" in its name, so a new Nexus-like header can't go unnoticed.
func headerLocations(t *testing.T) []string {
	t.Helper()
	svc := workflowservice.File_temporal_api_workflowservice_v1_service_proto.Services().Get(0)
	all := apiMessages()

	locations := map[string]struct{}{}
	seen := map[protoreflect.FullName]bool{}
	var visit func(md protoreflect.MessageDescriptor)
	visit = func(md protoreflect.MessageDescriptor) {
		if seen[md.FullName()] {
			return
		}
		seen[md.FullName()] = true
		if md.FullName() == anyName {
			for _, body := range all {
				if strings.HasPrefix(string(body.FullName()), "temporal.api.") {
					visit(body)
				}
			}
			return
		}
		fields := md.Fields()
		for i := range fields.Len() {
			fd := fields.Get(i)
			loc := string(md.FullName()) + "." + string(fd.Name())
			if isNexusHeaderField(fd) {
				locations[loc] = struct{}{}
				continue
			}
			if fd.IsMap() && fd.MapValue().Kind() == protoreflect.StringKind && strings.Contains(string(fd.Name()), "header") {
				locations[loc+" (not a Nexus header)"] = struct{}{}
				continue
			}
			m := fieldMessage(fd)
			if m == nil {
				continue
			}
			if m.FullName() == headerName {
				locations[loc] = struct{}{}
				continue
			}
			visit(m)
		}
	}
	methods := svc.Methods()
	for i := range methods.Len() {
		visit(methods.Get(i).Input())
		visit(methods.Get(i).Output())
	}
	out := make([]string, 0, len(locations))
	for loc := range locations {
		out = append(out, loc)
	}
	slices.Sort(out)
	return out
}

// TestHeaderCoverage keeps the binding layer's explicit tables and its strip
// passes complete: every header location in the API must be classified, so a
// new API field fails this test until someone decides how to treat it.
func TestHeaderCoverage(t *testing.T) {
	got := headerLocations(t)
	if os.Getenv("PRINT_HEADER_LOCATIONS") != "" {
		for _, loc := range got {
			t.Log(loc)
		}
	}
	var unclassified, stale []string
	for _, loc := range got {
		if _, ok := headerClassification[loc]; !ok {
			unclassified = append(unclassified, loc)
		}
	}
	for loc := range headerClassification {
		if !slices.Contains(got, loc) {
			stale = append(stale, loc)
		}
	}
	slices.Sort(stale)
	require.Empty(t, unclassified, "new header locations: classify each in headerClassification and, if it's sealed or opened, handle it in the interceptor")
	require.Empty(t, stale, "header locations no longer in the API")

	for loc, classes := range headerClassification {
		require.NotEmpty(t, classes, loc)
		for _, class := range classes {
			switch class {
			case sealed:
				require.Contains(t, sealedLocations(), loc, "no outbound test seals %s", loc)
			case opened:
				require.Contains(t, openedLocations(), loc, "no inbound test opens %s", loc)
			}
		}
	}
	for _, loc := range sealedLocations() {
		require.Contains(t, headerClassification[loc], sealed, loc)
	}
	for _, loc := range openedLocations() {
		require.Contains(t, headerClassification[loc], opened, loc)
	}
}

type classification string

const (
	// sealed: the interceptor seals pending values here on the way out.
	sealed classification = "sealed"
	// opened: the interceptor opens sealed values here on the way in.
	opened classification = "opened"
	// stripped: no context; the strip passes clean it in both directions.
	// Every sealed or opened location is also stripped wherever else its
	// message appears, such as the Update echo or history read in reverse.
	stripped classification = "stripped"
	// ignored: a map<string, string> that isn't a Nexus header.
	ignored classification = "ignored"
)

var headerClassification = map[string][]classification{
	// Client calls.
	"temporal.api.workflowservice.v1.StartWorkflowExecutionRequest.header":             {sealed},
	"temporal.api.workflowservice.v1.SignalWorkflowExecutionRequest.header":            {sealed},
	"temporal.api.workflowservice.v1.SignalWithStartWorkflowExecutionRequest.header":   {sealed},
	"temporal.api.workflowservice.v1.StartActivityExecutionRequest.header":             {sealed},
	"temporal.api.workflowservice.v1.StartNexusOperationExecutionRequest.nexus_header": {sealed},
	// Update requests, live and in history. Stripped in the acceptance and
	// rejection echoes.
	"temporal.api.update.v1.Input.header": {sealed, opened, stripped},

	// Workflow task completions.
	"temporal.api.command.v1.ScheduleActivityTaskCommandAttributes.header":            {sealed},
	"temporal.api.command.v1.StartChildWorkflowExecutionCommandAttributes.header":     {sealed},
	"temporal.api.command.v1.ContinueAsNewWorkflowExecutionCommandAttributes.header":  {sealed},
	"temporal.api.command.v1.SignalExternalWorkflowExecutionCommandAttributes.header": {sealed},
	"temporal.api.command.v1.ScheduleNexusOperationCommandAttributes.nexus_header":    {sealed},
	// Local Activity markers have no header.
	"temporal.api.command.v1.RecordMarkerCommandAttributes.header": {stripped},

	// Tasks.
	"temporal.api.history.v1.WorkflowExecutionStartedEventAttributes.header":  {opened, stripped},
	"temporal.api.history.v1.WorkflowExecutionSignaledEventAttributes.header": {opened, stripped},
	"temporal.api.workflowservice.v1.PollActivityTaskQueueResponse.header":    {opened},
	"temporal.api.nexus.v1.Request.header":                                    {opened, stripped},

	// History of calls a Workflow made: sealed for their receivers.
	"temporal.api.history.v1.ActivityTaskScheduledEventAttributes.header":                    {stripped},
	"temporal.api.history.v1.ChildWorkflowExecutionStartedEventAttributes.header":            {stripped},
	"temporal.api.history.v1.MarkerRecordedEventAttributes.header":                           {stripped},
	"temporal.api.history.v1.NexusOperationScheduledEventAttributes.nexus_header":            {stripped},
	"temporal.api.history.v1.SignalExternalWorkflowExecutionInitiatedEventAttributes.header": {stripped},
	"temporal.api.history.v1.StartChildWorkflowExecutionInitiatedEventAttributes.header":     {stripped},
	"temporal.api.history.v1.WorkflowExecutionContinuedAsNewEventAttributes.header":          {stripped},
	"temporal.api.activity.v1.ActivityExecutionInfo.header":                                  {stripped},
	"temporal.api.nexus.v1.NexusOperationExecutionInfo.nexus_header":                         {stripped},

	// Calls with no context: Queries, Schedules, batch Signals, and Signals
	// sent on reset.
	"temporal.api.query.v1.WorkflowQuery.header":                        {stripped},
	"temporal.api.workflow.v1.NewWorkflowExecutionInfo.header":          {stripped},
	"temporal.api.batch.v1.BatchOperationSignal.header":                 {stripped},
	"temporal.api.workflow.v1.PostResetOperation.SignalWorkflow.header": {stripped},

	"temporal.api.common.v1.Callback.Nexus.header (not a Nexus header)":                {ignored},
	"temporal.api.nexus.v1.StartOperationRequest.callback_header (not a Nexus header)": {ignored},
}
