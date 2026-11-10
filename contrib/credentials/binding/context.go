package binding

import commonpb "go.temporal.io/api/common/v1"

// The contexts a header can be bound to. Each is passed as the HPKE info, so a
// header sealed under one opens only under exactly the same one. They are
// frozen for v1: a new field means a new version.

// WorkflowContext binds a Workflow start, child Workflow, or continue-as-new
// header to its namespace, Workflow type and ID, and input.
func WorkflowContext(namespace, workflowType, workflowID string, input []*commonpb.Payload) []byte {
	return Frame(
		[]byte("workflow-credential"), []byte("v1"),
		[]byte(namespace), []byte(workflowType), []byte(workflowID),
		Digest(input),
	)
}

// UpdateContext binds an Update header to its namespace, Workflow ID, Update
// name and ID, and arguments. The Update ID keeps the header from being reused
// on another Update.
func UpdateContext(namespace, workflowID, updateName, updateID string, args []*commonpb.Payload) []byte {
	return Frame(
		[]byte("update-credential"), []byte("v1"),
		[]byte(namespace), []byte(workflowID), []byte(updateName), []byte(updateID),
		Digest(args),
	)
}

// SignalContext binds a Signal header to its namespace, Workflow ID, Signal
// name, and input. Like the Signal, it addresses the Workflow, not a run.
func SignalContext(namespace, workflowID, signalName string, input []*commonpb.Payload) []byte {
	return Frame(
		[]byte("signal-credential"), []byte("v1"),
		[]byte(namespace), []byte(workflowID), []byte(signalName),
		Digest(input),
	)
}

// ActivityContext binds an Activity header to its namespace, Workflow ID
// (empty for a standalone Activity), Activity ID and type, and input.
func ActivityContext(namespace, workflowID, activityID, activityType string, input []*commonpb.Payload) []byte {
	return Frame(
		[]byte("activity-credential"), []byte("v1"),
		[]byte(namespace), []byte(workflowID), []byte(activityID), []byte(activityType),
		Digest(input),
	)
}

// NexusContext binds a Nexus header to its endpoint, service, operation, and
// input.
func NexusContext(endpoint, service, operation string, input *commonpb.Payload) []byte {
	return Frame(
		[]byte("nexus-credential"), []byte("v1"),
		[]byte(endpoint), []byte(service), []byte(operation),
		Digest([]*commonpb.Payload{input}),
	)
}
