package binding

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	nexuspb "go.temporal.io/api/nexus/v1"
	updatepb "go.temporal.io/api/update/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
)

// Options configures [NewInterceptor]. These are the only per-deployment
// inputs to the binding layer.
type Options struct {
	// Keyset seals outgoing headers and opens incoming ones. Required.
	Keyset *Keyset
	// Strict ignores the keyset's default key, so a call that names no key and
	// matches no Nexus endpoint override fails instead of being sealed to a key
	// the receiver may not hold.
	Strict bool
	// Logger receives headers that fail to open and credentials dropped from
	// calls with no context to seal them to. Never their values. Defaults to
	// slog.Default().
	Logger *slog.Logger
}

type binder struct {
	keys   *Keyset
	strict bool
	log    *slog.Logger
}

// NewInterceptor returns the binding layer as a unary client interceptor. It
// must sit closest to the wire, after any payload codec, so the payloads it
// digests are exactly the bytes sent and received:
//
//   - Outbound, it seals each pending credentials header to the receiver's
//     public key, bound to the call's context, and removes every other
//     credentials field, so nothing in plaintext leaves the application. It
//     never mutates the request it's given: it seals a clone, so retries and
//     resends reseal from the original.
//   - Inbound, it rebuilds each task's context from the reply, opens its
//     header, and attaches the result next to the sealed bytes. Every other
//     credentials field that isn't sealed is removed.
//
// A request it can't seal fails with codes.FailedPrecondition without being
// sent.
func NewInterceptor(opts Options) grpc.UnaryClientInterceptor {
	if opts.Keyset == nil {
		panic("binding.NewInterceptor: Options.Keyset is required")
	}
	b := newBinder(opts)
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		callOpts ...grpc.CallOption,
	) error {
		if AnswerProbe(ctx) {
			return nil
		}
		reqMsg, _ := req.(proto.Message)
		if reqMsg != nil {
			sealed, err := b.outbound(method, reqMsg)
			if err != nil {
				return err
			}
			req, reqMsg = sealed, sealed
		}
		if err := invoker(ctx, method, req, reply, cc, callOpts...); err != nil {
			return err
		}
		if m, ok := reply.(proto.Message); ok {
			b.inbound(method, reqMsg, m)
		}
		return nil
	}
}

func newBinder(opts Options) *binder {
	b := &binder{keys: opts.Keyset, strict: opts.Strict, log: opts.Logger}
	if b.log == nil {
		b.log = slog.Default()
	}
	return b
}

// OpenHistory opens the headers of history's events the way the interceptor
// opens a GetWorkflowExecutionHistory reply, first removing every opened
// result already there, so none read from a file or other storage is
// trusted. With a nil Keyset, it only removes them. namespace and workflowID
// are the Workflow's: they're part of the contexts its headers are bound to.
func OpenHistory(opts Options, namespace, workflowID string, history *historypb.History) {
	b := newBinder(opts)
	o := &opener{b: b, rpc: "OpenHistory", ns: namespace, done: newHandled()}
	if b.keys != nil {
		o.openHistory(history.GetEvents(), workflowID, 0)
	}
	walk(history.ProtoReflect(), &stripper{b: b, done: o.done, rpc: o.rpc})
}

func rpcName(method string) string {
	if i := strings.LastIndexByte(method, '/'); i >= 0 {
		return method[i+1:]
	}
	return method
}

// Outbound.

type sealer struct {
	b    *binder
	ns   string
	done *handled
}

func (b *binder) outbound(method string, req proto.Message) (proto.Message, error) {
	if !HasCredentials(req) {
		return req, nil
	}
	req = proto.Clone(req)
	rpc := rpcName(method)
	s := &sealer{b: b, done: newHandled()}
	if r, ok := req.(interface{ GetNamespace() string }); ok {
		s.ns = r.GetNamespace()
	}

	var err error
	switch r := req.(type) {
	case *workflowservice.StartWorkflowExecutionRequest:
		err = s.sealStart(rpc, r)
	case *workflowservice.SignalWorkflowExecutionRequest:
		wfID := r.GetWorkflowExecution().GetWorkflowId()
		err = s.sealHeader(r.GetHeader(), "", rpc, wfID, func() ([]byte, error) {
			return SignalContext(s.ns, wfID, r.GetSignalName(), r.GetInput().GetPayloads()), nil
		})
	case *workflowservice.SignalWithStartWorkflowExecutionRequest:
		// One header for both the start and the Signal. It's bound to the
		// Signal, so the start carries no credentials.
		err = s.sealHeader(r.GetHeader(), "", rpc, r.GetWorkflowId(), func() ([]byte, error) {
			return SignalContext(s.ns, r.GetWorkflowId(), r.GetSignalName(), r.GetSignalInput().GetPayloads()), nil
		})
	case *workflowservice.UpdateWorkflowExecutionRequest:
		err = s.sealUpdate(rpc, r)
	case *workflowservice.ExecuteMultiOperationRequest:
		for _, op := range r.GetOperations() {
			if start := op.GetStartWorkflow(); start != nil {
				err = s.sealStart(rpc, start)
			} else if update := op.GetUpdateWorkflow(); update != nil {
				err = s.sealUpdate(rpc, update)
			}
			if err != nil {
				break
			}
		}
	case *workflowservice.StartActivityExecutionRequest:
		err = s.sealHeader(r.GetHeader(), "", rpc, r.GetActivityId(), func() ([]byte, error) {
			return ActivityContext(s.ns, "", r.GetActivityId(), r.GetActivityType().GetName(), r.GetInput().GetPayloads()), nil
		})
	case *workflowservice.StartNexusOperationExecutionRequest:
		err = s.sealNexus(r, r.GetNexusHeader(), rpc, r.GetOperationId(),
			r.GetEndpoint(), r.GetService(), r.GetOperation(), r.GetInput())
	case *workflowservice.RespondWorkflowTaskCompletedRequest:
		err = s.sealCommands(r)
	}
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "credentials: "+err.Error())
	}

	walk(req.ProtoReflect(), &stripper{b: b, done: s.done, outbound: true, rpc: rpc})
	return req, nil
}

func (s *sealer) sealStart(rpc string, r *workflowservice.StartWorkflowExecutionRequest) error {
	ns := nonEmpty(r.GetNamespace(), s.ns)
	return s.sealHeader(r.GetHeader(), "", rpc, r.GetWorkflowId(), func() ([]byte, error) {
		return WorkflowContext(ns, r.GetWorkflowType().GetName(), r.GetWorkflowId(), r.GetInput().GetPayloads()), nil
	})
}

// sealUpdate seals an Update request's header. The SDK fills in the Update ID
// before sending.
func (s *sealer) sealUpdate(rpc string, r *workflowservice.UpdateWorkflowExecutionRequest) error {
	ns := nonEmpty(r.GetNamespace(), s.ns)
	wfID := r.GetWorkflowExecution().GetWorkflowId()
	in := r.GetRequest().GetInput()
	updateID := r.GetRequest().GetMeta().GetUpdateId()
	return s.sealHeader(in.GetHeader(), "", rpc, "update "+updateID+" of workflow "+wfID, func() ([]byte, error) {
		if updateID == "" {
			return nil, errors.New("the Update has no ID")
		}
		return UpdateContext(ns, wfID, in.GetName(), updateID, in.GetArgs().GetPayloads()), nil
	})
}

// sealCommands seals the headers of the commands a Workflow task completes
// with, on the final page and on intermediate pages alike. The Workflow ID
// comes from the request's resource_id.
func (s *sealer) sealCommands(r *workflowservice.RespondWorkflowTaskCompletedRequest) error {
	workflowID := func() (string, error) {
		id, ok := strings.CutPrefix(r.GetResourceId(), "workflow:")
		if !ok || id == "" {
			return "", fmt.Errorf("resource_id is %q, not workflow:<id>; the Worker's SDK must set it on Workflow task completions", r.GetResourceId())
		}
		return id, nil
	}
	for _, cmd := range r.GetCommands() {
		var err error
		switch {
		case cmd.GetScheduleActivityTaskCommandAttributes() != nil:
			a := cmd.GetScheduleActivityTaskCommandAttributes()
			err = s.sealHeader(a.GetHeader(), "", "ScheduleActivityTask", "activity "+a.GetActivityId(), func() ([]byte, error) {
				wfID, err := workflowID()
				if err != nil {
					return nil, err
				}
				return ActivityContext(s.ns, wfID, a.GetActivityId(), a.GetActivityType().GetName(), a.GetInput().GetPayloads()), nil
			})
		case cmd.GetStartChildWorkflowExecutionCommandAttributes() != nil:
			a := cmd.GetStartChildWorkflowExecutionCommandAttributes()
			//lint:ignore SA1019 the deprecated namespace still selects where the call goes
			ns := nonEmpty(a.GetNamespace(), s.ns)
			err = s.sealHeader(a.GetHeader(), "", "StartChildWorkflowExecution", "workflow "+a.GetWorkflowId(), func() ([]byte, error) {
				return WorkflowContext(ns, a.GetWorkflowType().GetName(), a.GetWorkflowId(), a.GetInput().GetPayloads()), nil
			})
		case cmd.GetContinueAsNewWorkflowExecutionCommandAttributes() != nil:
			a := cmd.GetContinueAsNewWorkflowExecutionCommandAttributes()
			err = s.sealHeader(a.GetHeader(), "", "ContinueAsNewWorkflowExecution", "of "+r.GetResourceId(), func() ([]byte, error) {
				wfID, err := workflowID()
				if err != nil {
					return nil, err
				}
				return WorkflowContext(s.ns, a.GetWorkflowType().GetName(), wfID, a.GetInput().GetPayloads()), nil
			})
		case cmd.GetSignalExternalWorkflowExecutionCommandAttributes() != nil:
			a := cmd.GetSignalExternalWorkflowExecutionCommandAttributes()
			//lint:ignore SA1019 the deprecated namespace still selects where the call goes
			ns := nonEmpty(a.GetNamespace(), s.ns)
			wfID := a.GetExecution().GetWorkflowId()
			err = s.sealHeader(a.GetHeader(), "", "SignalExternalWorkflowExecution", "to workflow "+wfID, func() ([]byte, error) {
				return SignalContext(ns, wfID, a.GetSignalName(), a.GetInput().GetPayloads()), nil
			})
		case cmd.GetScheduleNexusOperationCommandAttributes() != nil:
			a := cmd.GetScheduleNexusOperationCommandAttributes()
			err = s.sealNexus(a, a.GetNexusHeader(), "ScheduleNexusOperation",
				fmt.Sprintf("%s/%s/%s", a.GetEndpoint(), a.GetService(), a.GetOperation()),
				a.GetEndpoint(), a.GetService(), a.GetOperation(), a.GetInput())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func sealingError(what, id string, cause error) error {
	if id == "" {
		return fmt.Errorf("sealing %s: %w", what, cause)
	}
	return fmt.Errorf("sealing %s %s: %w", what, id, cause)
}

func codecError(field, encoding, want string) error {
	return fmt.Errorf("the %s header has encoding %q, not %s: a payload codec or visitor changed it. "+
		"gRPC-level payload codecs must pass through payloads for which binding.IsCredentialsPayload is true, "+
		"and credentials headers can't be offloaded to external storage", field, encoding, want)
}

// sealHeader replaces h's pending credentials with a header sealed under the
// context info returns. It does nothing when h carries no pending value: the
// strip pass removes any other credentials field.
func (s *sealer) sealHeader(h *commonpb.Header, nexusEndpoint, what, id string, info func() ([]byte, error)) error {
	fields := h.GetFields()
	p, ok := fields[HeaderCredentials]
	if !ok {
		return nil
	}
	switch enc := payloadEncoding(p); enc {
	case EncodingPending:
	case EncodingSealed, EncodingLocal:
		// An opened result copied into an outgoing header, or a Local Activity
		// value. Neither is sealed; the strip pass removes them.
		return nil
	default:
		return sealingError(what, id, codecError(HeaderCredentials, enc, EncodingPending))
	}
	values, err := ParseValues(p.GetData())
	if err != nil {
		return sealingError(what, id, err)
	}
	keyName := ""
	if kp, ok := fields[HeaderKey]; ok {
		if enc := payloadEncoding(kp); enc != EncodingKey {
			return sealingError(what, id, codecError(HeaderKey, enc, EncodingKey))
		}
		if keyName, err = parseKeyName(kp.GetData()); err != nil {
			return sealingError(what, id, err)
		}
	}
	sealed, err := s.seal(keyName, nexusEndpoint, values, info)
	if err != nil {
		return sealingError(what, id, err)
	}
	fields[HeaderCredentials] = SealedPayload(sealed)
	delete(fields, HeaderKey)
	s.done.headers[h] = struct{}{}
	return nil
}

func (s *sealer) seal(keyName, nexusEndpoint string, values map[string]string, info func() ([]byte, error)) (string, error) {
	key, err := s.b.keys.SealingKey(keyName, nexusEndpoint, s.b.strict)
	if err != nil {
		return "", err
	}
	ctxInfo, err := info()
	if err != nil {
		return "", err
	}
	return Seal(key, ctxInfo, values)
}

// sealNexus seals a Nexus header. Nexus headers aren't payloads, so the sealed
// value is the header string itself.
func (s *sealer) sealNexus(parent proto.Message, h map[string]string, what, id, endpoint, service, operation string, input *commonpb.Payload) error {
	for k := range h {
		if isFamilyKey(k) && k != HeaderCredentials && k != HeaderKey && k != HeaderOpened {
			delete(h, k)
		}
	}
	pending, ok := h[HeaderCredentials]
	if !ok || strings.HasPrefix(pending, SealedPrefix) {
		return nil
	}
	values, err := ParseValues([]byte(pending))
	if err != nil {
		return sealingError(what, id, err)
	}
	keyName := ""
	if kv, ok := h[HeaderKey]; ok {
		if keyName, err = parseKeyName([]byte(kv)); err != nil {
			return sealingError(what, id, err)
		}
	}
	sealed, err := s.seal(keyName, endpoint, values, func() ([]byte, error) {
		return NexusContext(endpoint, service, operation, input), nil
	})
	if err != nil {
		return sealingError(what, id, err)
	}
	h[HeaderCredentials] = sealed
	delete(h, HeaderKey)
	delete(h, HeaderOpened)
	s.done.nexus[parent] = struct{}{}
	return nil
}

func nonEmpty(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// Inbound.

type opener struct {
	b    *binder
	rpc  string
	ns   string
	done *handled
}

// target describes a header being opened, for logs.
type target struct {
	kind       string
	workflowID string
	id         string
	eventID    int64
	// processed is set for history a Worker already handled, where a header
	// that opened before now doesn't.
	processed bool
}

func (b *binder) inbound(method string, req, reply proto.Message) {
	o := &opener{b: b, rpc: rpcName(method), done: newHandled()}
	if r, ok := req.(interface{ GetNamespace() string }); ok {
		o.ns = r.GetNamespace()
	}
	switch r := reply.(type) {
	case *workflowservice.PollWorkflowTaskQueueResponse:
		o.openWorkflowTask(r)
	case *workflowservice.RespondWorkflowTaskCompletedResponse:
		o.openWorkflowTask(r.GetWorkflowTask())
		for _, t := range r.GetActivityTasks() {
			o.openActivityTask(t)
		}
	case *workflowservice.StartWorkflowExecutionResponse:
		o.openWorkflowTask(r.GetEagerWorkflowTask())
	case *workflowservice.ExecuteMultiOperationResponse:
		for _, resp := range r.GetResponses() {
			o.openWorkflowTask(resp.GetStartWorkflow().GetEagerWorkflowTask())
		}
	case *workflowservice.GetWorkflowExecutionHistoryResponse:
		if hr, ok := req.(*workflowservice.GetWorkflowExecutionHistoryRequest); ok {
			o.openHistory(r.GetHistory().GetEvents(), hr.GetExecution().GetWorkflowId(), 0)
			o.openRawHistory(r.GetRawHistory(), hr.GetExecution().GetWorkflowId())
		}
	case *workflowservice.PollActivityTaskQueueResponse:
		o.openActivityTask(r)
	case *workflowservice.PollNexusTaskQueueResponse:
		o.openNexus(r.GetRequest())
	}
	walk(reply.ProtoReflect(), &stripper{b: b, done: o.done, rpc: o.rpc})
}

func (o *opener) openWorkflowTask(t *workflowservice.PollWorkflowTaskQueueResponse) {
	if t == nil {
		return
	}
	wfID := t.GetWorkflowExecution().GetWorkflowId()
	o.openHistory(t.GetHistory().GetEvents(), wfID, t.GetPreviousStartedEventId())
	for _, msg := range t.GetMessages() {
		o.openUpdateMessage(msg.GetBody(), wfID)
	}
}

// openHistory opens the headers of the events a Workflow handles: its start,
// its Signals, and its Updates.
func (o *opener) openHistory(events []*historypb.HistoryEvent, wfID string, previousStarted int64) {
	// With signal-with-start, the Started event carries the Signal's header,
	// which is bound to the Signal and doesn't open as a start.
	signaled := map[string]struct{}{}
	for _, e := range events {
		if p := e.GetWorkflowExecutionSignaledEventAttributes().GetHeader().GetFields()[HeaderCredentials]; isSealedPayload(p) {
			signaled[string(p.GetData())] = struct{}{}
		}
	}
	for _, e := range events {
		t := target{workflowID: wfID, eventID: e.GetEventId(), processed: e.GetEventId() <= previousStarted}
		switch {
		case e.GetWorkflowExecutionStartedEventAttributes() != nil:
			a := e.GetWorkflowExecutionStartedEventAttributes()
			t.kind = "workflow"
			o.openHeader(a.GetHeader(), t, func() []byte {
				return WorkflowContext(o.ns, a.GetWorkflowType().GetName(), wfID, a.GetInput().GetPayloads())
			}, func(p *commonpb.Payload) bool {
				_, ok := signaled[string(p.GetData())]
				return ok
			})
		case e.GetWorkflowExecutionSignaledEventAttributes() != nil:
			a := e.GetWorkflowExecutionSignaledEventAttributes()
			t.kind, t.id = "signal", a.GetSignalName()
			o.openHeader(a.GetHeader(), t, func() []byte {
				return SignalContext(o.ns, wfID, a.GetSignalName(), a.GetInput().GetPayloads())
			}, nil)
		case e.GetWorkflowExecutionUpdateAdmittedEventAttributes() != nil:
			o.openUpdate(e.GetWorkflowExecutionUpdateAdmittedEventAttributes().GetRequest(), t)
		case e.GetWorkflowExecutionUpdateAcceptedEventAttributes() != nil:
			o.openUpdate(e.GetWorkflowExecutionUpdateAcceptedEventAttributes().GetAcceptedRequest(), t)
		}
	}
}

// openRawHistory opens history a server sent as serialized batches, and
// re-serializes them. A batch it can't read is emptied, so its headers fail
// closed.
func (o *opener) openRawHistory(blobs []*commonpb.DataBlob, wfID string) {
	if len(blobs) == 0 {
		return
	}
	batches := make([]*historypb.History, len(blobs))
	var events []*historypb.HistoryEvent
	for i, blob := range blobs {
		h := &historypb.History{}
		if blob.GetEncodingType() != enumspb.ENCODING_TYPE_PROTO3 || proto.Unmarshal(blob.GetData(), h) != nil {
			o.b.log.Warn("Dropping a raw history batch the binding layer can't read",
				slog.String("rpc", o.rpc), slog.String("workflow_id", wfID))
			blob.Data = nil
			continue
		}
		batches[i] = h
		events = append(events, h.GetEvents()...)
	}
	o.openHistory(events, wfID, 0)
	for i, h := range batches {
		if h == nil {
			continue
		}
		walk(h.ProtoReflect(), &stripper{b: o.b, done: o.done, rpc: o.rpc})
		data, err := proto.Marshal(h)
		if err != nil {
			data = nil
		}
		blobs[i].Data = data
	}
}

func (o *opener) openUpdate(r *updatepb.Request, t target) {
	in := r.GetInput()
	t.kind, t.id = "update", r.GetMeta().GetUpdateId()
	o.openHeader(in.GetHeader(), t, func() []byte {
		return UpdateContext(o.ns, t.workflowID, in.GetName(), r.GetMeta().GetUpdateId(), in.GetArgs().GetPayloads())
	}, nil)
}

// openUpdateMessage opens the header of an Update delivered as a protocol
// message, and re-packs the message body.
func (o *opener) openUpdateMessage(body *anypb.Any, wfID string) {
	if !body.MessageIs((*updatepb.Request)(nil)) {
		return
	}
	r := &updatepb.Request{}
	if err := body.UnmarshalTo(r); err != nil {
		return
	}
	o.openUpdate(r, target{workflowID: wfID})
	walk(r.ProtoReflect(), &stripper{b: o.b, done: o.done, rpc: o.rpc})
	if repack(body, r) {
		o.done.anys[body] = struct{}{}
	}
}

func (o *opener) openActivityTask(t *workflowservice.PollActivityTaskQueueResponse) {
	if t == nil {
		return
	}
	wfID := t.GetWorkflowExecution().GetWorkflowId()
	o.openHeader(t.GetHeader(), target{kind: "activity", workflowID: wfID, id: t.GetActivityId()}, func() []byte {
		return ActivityContext(o.ns, wfID, t.GetActivityId(), t.GetActivityType().GetName(), t.GetInput().GetPayloads())
	}, nil)
}

// openHeader opens h's sealed credentials under the context info returns and
// attaches the result next to the sealed bytes. A value that isn't sealed is
// replaced with a failure marker. skip, when set, reports a failure to leave
// unmarked and unlogged.
func (o *opener) openHeader(h *commonpb.Header, t target, info func() []byte, skip func(*commonpb.Payload) bool) {
	fields := h.GetFields()
	delete(fields, HeaderKey)
	delete(fields, HeaderOpened)
	p, ok := fields[HeaderCredentials]
	if !ok {
		return
	}
	o.done.headers[h] = struct{}{}
	if !isSealedPayload(p) {
		o.fail(t, ReasonMalformed, "")
		fields[HeaderCredentials] = failurePayload(ReasonMalformed)
		return
	}
	delete(p.Metadata, HeaderOpened)
	creds, err := o.b.keys.Open(info(), string(p.GetData()))
	if err != nil {
		if skip != nil && skip(p) {
			return
		}
		reason := reasonFor(err)
		o.fail(t, reason, string(p.GetData()))
		p.Metadata[HeaderOpened] = Opened{Reason: reason}.Marshal()
		return
	}
	p.Metadata[HeaderOpened] = Opened{OK: true, Credentials: creds}.Marshal()
}

// failurePayload stands in for a credentials value that wasn't sealed. Its
// plaintext never reaches the application.
func failurePayload(reason string) *commonpb.Payload {
	p := newPayload(EncodingSealed, nil)
	p.Metadata[HeaderOpened] = Opened{Reason: reason}.Marshal()
	return p
}

// openNexus opens a Nexus start request's header, bound to the endpoint the
// Temporal Service reports. A request without an endpoint, from a server older
// than 1.30, fails closed.
func (o *opener) openNexus(r *nexuspb.Request) {
	h := r.GetHeader()
	start := r.GetStartOperation()
	if h == nil || start == nil {
		return
	}
	for k := range h {
		if isFamilyKey(k) && k != HeaderCredentials {
			delete(h, k)
		}
	}
	sealed, ok := h[HeaderCredentials]
	if !ok {
		return
	}
	o.done.nexus[r] = struct{}{}
	t := target{kind: "nexus", id: fmt.Sprintf("%s/%s/%s", r.GetEndpoint(), start.GetService(), start.GetOperation())}
	var res Opened
	switch {
	case !strings.HasPrefix(sealed, SealedPrefix):
		delete(h, HeaderCredentials)
		res.Reason = ReasonMalformed
		o.fail(t, res.Reason, "")
	case r.GetEndpoint() == "":
		res.Reason = ReasonNoEndpoint
		o.fail(t, res.Reason, sealed)
	default:
		creds, err := o.b.keys.Open(NexusContext(r.GetEndpoint(), start.GetService(), start.GetOperation(), start.GetPayload()), sealed)
		if err != nil {
			res.Reason = reasonFor(err)
			o.fail(t, res.Reason, sealed)
		} else {
			res = Opened{OK: true, Credentials: creds}
		}
	}
	h[HeaderOpened] = string(res.Marshal())
}

func reasonFor(err error) string {
	switch {
	case errors.Is(err, ErrKeyNotHeld):
		return ReasonKeyNotHeld
	case errors.Is(err, ErrOpen):
		return ReasonDoesNotOpen
	default:
		return ReasonMalformed
	}
}

func (o *opener) fail(t target, reason, sealed string) {
	attrs := []any{
		slog.String("rpc", o.rpc),
		slog.String("header", t.kind),
		slog.String("namespace", o.ns),
		slog.String("reason", reason),
	}
	if t.workflowID != "" {
		attrs = append(attrs, slog.String("workflow_id", t.workflowID))
	}
	if t.id != "" {
		attrs = append(attrs, slog.String("id", t.id))
	}
	if t.eventID != 0 {
		attrs = append(attrs, slog.Int64("event_id", t.eventID))
	}
	if id, ok := SealedKeyID(sealed); ok {
		attrs = append(attrs, slog.String("key_id", hex.EncodeToString(id[:])))
	}
	if t.processed {
		o.b.log.Error("Credentials header that a Worker already processed no longer opens; "+
			"was its private key retired while Workflows could still replay?", attrs...)
		return
	}
	o.b.log.Warn("Credentials header did not open; treating its credentials as missing", attrs...)
}

// Strip passes.

// stripper removes every credentials field in positions a pass didn't seal or
// open. Outbound, that's every one, so nothing in plaintext reaches Temporal.
// Inbound, sealed values stay as they are, so an Activity's header in its
// caller's history is still visible, but nothing in plaintext reaches the
// application.
type stripper struct {
	b        *binder
	done     *handled
	outbound bool
	rpc      string
}

func (s *stripper) header(h *commonpb.Header) bool {
	if _, ok := s.done.headers[h]; ok {
		return false
	}
	fields := h.GetFields()
	changed := false
	for _, k := range []string{HeaderKey, HeaderOpened} {
		if _, ok := fields[k]; ok {
			delete(fields, k)
			changed = true
		}
	}
	p, ok := fields[HeaderCredentials]
	if !ok {
		return changed
	}
	if s.outbound {
		if enc := payloadEncoding(p); enc != EncodingSealed {
			s.dropped(enc)
		}
		delete(fields, HeaderCredentials)
		return true
	}
	if !isSealedPayload(p) {
		delete(fields, HeaderCredentials)
		return true
	}
	if _, ok := p.Metadata[HeaderOpened]; ok {
		delete(p.Metadata, HeaderOpened)
		return true
	}
	return changed
}

func (s *stripper) nexus(parent protoreflect.Message, h protoreflect.Map) bool {
	if _, ok := s.done.nexus[parent.Interface()]; ok {
		return false
	}
	changed := false
	for _, k := range nexusFamilyKeys(h) {
		v := h.Get(k).String()
		if strings.EqualFold(k.String(), HeaderCredentials) {
			if !s.outbound && k.String() == HeaderCredentials && strings.HasPrefix(v, SealedPrefix) {
				continue
			}
			if s.outbound && !strings.HasPrefix(v, SealedPrefix) {
				s.dropped("nexus")
			}
		}
		h.Clear(k)
		changed = true
	}
	return changed
}

func (s *stripper) skipAny(a *anypb.Any) bool {
	_, ok := s.done.anys[a]
	return ok
}

func (s *stripper) dropped(encoding string) {
	s.b.log.Warn("Dropping credentials from a call with no context to bind them to",
		slog.String("rpc", s.rpc), slog.String("encoding", encoding))
}
