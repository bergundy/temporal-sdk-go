package credentials

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/contrib/credentials/binding"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ErrorTypeCredentialMissing is the ApplicationError type of the error [Get],
// [Require], [CheckRetain], and [Retain] return when a credential is missing
// or a retain policy declines it. The error names the credential, never the
// cause: that goes only to Worker logs.
const ErrorTypeCredentialMissing = "CredentialMissing"

// Values are credentials by name, such as {"user_token": token}.
type Values map[string]string

// ValueContext is what [Get], [Lookup], and [Require] read from. Both
// context.Context and workflow.Context satisfy it.
type ValueContext interface {
	Value(key any) any
}

type (
	attachKey    struct{}
	selectionKey struct{}
	callKey      struct{}
	runKey       struct{}
)

type attachment struct {
	values Values
	key    string
}

// Attach returns a context whose client calls carry values: starting or
// signaling a Workflow, signal-with-start, Updates, update-with-start, and
// starting a standalone Activity or Nexus operation. Queries never carry
// credentials. The binding layer seals them to the namespace's default key.
//
// A call carries the values from the innermost Attach on its context. They
// don't merge with outer ones.
func Attach(ctx context.Context, values Values) context.Context {
	return context.WithValue(ctx, attachKey{}, attachment{values: maps.Clone(values)})
}

// AttachWithKey is [Attach], sealing values to the named key of the keyset
// instead of the default.
func AttachWithKey(ctx context.Context, key string, values Values) context.Context {
	return context.WithValue(ctx, attachKey{}, attachment{values: maps.Clone(values), key: key})
}

type selection struct {
	names []string
	all   bool
	key   string
}

// Propagate returns a Workflow context whose calls forward exactly the named
// credentials: Activities, Local Activities, child Workflows, continue-as-new,
// Signals to other Workflows, and Nexus operations. Calls made with ctx itself
// forward nothing.
//
// With no names, Propagate forwards nothing, so an empty list never forwards
// everything by accident. Use [PropagateAll] for that.
//
// In the Workflow function, the values come from what the Workflow holds. In
// an Update handler, the Update's own credentials come first. A name that
// isn't there is logged and skipped, and the receiver fails closed.
func Propagate(ctx workflow.Context, names ...string) workflow.Context {
	return workflow.WithValue(ctx, selectionKey{}, selection{names: slices.Clone(names)})
}

// PropagateAll returns a Workflow context whose calls forward every
// credential available to them. See [Propagate].
func PropagateAll(ctx workflow.Context) workflow.Context {
	return workflow.WithValue(ctx, selectionKey{}, selection{all: true})
}

// PropagateWithKey is [Propagate], sealing to the named key of the keyset
// instead of the default or a Nexus endpoint override.
func PropagateWithKey(ctx workflow.Context, key string, names ...string) workflow.Context {
	return workflow.WithValue(ctx, selectionKey{}, selection{names: slices.Clone(names), key: key})
}

// callKind is the kind of call a context belongs to.
type callKind int

const (
	callStart callKind = iota
	callUpdate
	callSignal
	callActivity
	callNexus
)

// call is the credentials one call carried, as the binding layer opened them.
type call struct {
	kind  callKind
	creds map[string]Credential
	// reason is set when the header failed to open.
	reason string
}

func newCall(kind callKind, values map[string]string) *call {
	c := &call{kind: kind, creds: make(map[string]Credential, len(values))}
	for name, value := range values {
		c.creds[name] = Credential{name: name, value: value}
	}
	return c
}

func callFromOpened(kind callKind, o binding.Opened, ok bool) *call {
	if !ok {
		return newCall(kind, nil)
	}
	c := newCall(kind, o.Credentials)
	if !o.OK {
		c.reason = o.Reason
		if c.reason == "" {
			c.reason = binding.ReasonMalformed
		}
	}
	return c
}

// runState is what one Workflow run holds.
type runState struct {
	held map[string]Credential
	// delivered counts the Signals that reached each channel, and signals
	// records the credentials each one carried, by number.
	delivered map[string]int
	signals   map[string]map[int]map[string]Credential
	// channels are the Signal channels RetainFromSignal has seen, by name.
	channels map[string]workflow.ReceiveChannel
}

// prune drops the credentials recorded for Signals numbered up to n.
func (r *runState) prune(name string, n int) {
	for _, i := range slices.Collect(maps.Keys(r.signals[name])) {
		if i <= n {
			delete(r.signals[name], i)
		}
	}
}

func newRunState() *runState {
	return &runState{
		held:      map[string]Credential{},
		delivered: map[string]int{},
		signals:   map[string]map[int]map[string]Credential{},
		channels:  map[string]workflow.ReceiveChannel{},
	}
}

func callOf(ctx ValueContext) *call {
	c, _ := ctx.Value(callKey{}).(*call)
	return c
}

func runOf(ctx ValueContext) *runState {
	r, _ := ctx.Value(runKey{}).(*runState)
	return r
}

// lookup finds name in the current call's credentials, then in what the
// Workflow holds. In the Workflow function itself, only what it holds counts,
// so Discard takes effect there.
func lookup(ctx ValueContext, name string) (Credential, bool) {
	c := callOf(ctx)
	if c != nil && c.kind != callStart {
		if cred, ok := c.creds[name]; ok {
			return cred, true
		}
	}
	if run := runOf(ctx); run != nil {
		if cred, ok := run.held[name]; ok {
			return cred, true
		}
	}
	return Credential{}, false
}

func missing(ctx ValueContext, name string) error {
	msg := fmt.Sprintf("credentials: missing credential %q", name)
	if c := callOf(ctx); c != nil && c.kind == callNexus {
		return &nexus.HandlerError{
			Type:          nexus.HandlerErrorTypeUnauthenticated,
			Message:       msg,
			RetryBehavior: nexus.HandlerErrorRetryBehaviorNonRetryable,
		}
	}
	return temporal.NewNonRetryableApplicationError(msg, ErrorTypeCredentialMissing, nil)
}

// Get returns the named credential: from the credentials the current call
// carried, then from what the Workflow holds. In the Workflow function, it
// reads only what the Workflow holds.
//
// When it's missing, Get returns a non-retryable error naming it: an
// ApplicationError of type [ErrorTypeCredentialMissing] in a Workflow or an
// Activity, and a non-retryable *nexus.HandlerError in a Nexus handler.
func Get(ctx ValueContext, name string) (Credential, error) {
	if cred, ok := lookup(ctx, name); ok {
		return cred, nil
	}
	return Credential{}, missing(ctx, name)
}

// Lookup is [Get] without the error.
func Lookup(ctx ValueContext, name string) (Credential, bool) {
	return lookup(ctx, name)
}

// Require checks that the current call carried each named credential: the
// start in a Workflow function, the Update in its validator and handler, or
// the task in an Activity or Nexus handler. It returns the same error as
// [Get] for the first one missing.
//
// In a Workflow, call it first thing, so a start without the credential fails
// the Workflow on its first Workflow task. In an Update, call it in the
// validator, so the Update is rejected before it's accepted, and again first
// thing in the handler.
func Require(ctx ValueContext, names ...string) error {
	c := callOf(ctx)
	for _, name := range names {
		if c == nil {
			return missing(ctx, name)
		}
		if _, ok := c.creds[name]; !ok {
			return missing(ctx, name)
		}
	}
	return nil
}

// errNotRetained is what RetainFromSignal returns; the cause is logged.
var errNotRetained = errors.New("credentials: credential not retained")

// check decides whether the run keeps offered under name. It logs why not.
func check(ctx workflow.Context, run *runState, name string, offered Credential, found bool, policy RetainPolicy) bool {
	if run == nil {
		workflow.GetLogger(ctx).Warn("credentials: no credentials state on this context; is the credentials plugin installed?", "credential", name)
		return false
	}
	if !found {
		workflow.GetLogger(ctx).Warn("credentials: the call didn't carry the credential", "credential", name)
		return false
	}
	held, holds := run.held[name]
	if ok, reason := policy.Keep(held.value, holds, offered.value); !ok {
		workflow.GetLogger(ctx).Warn("credentials: retain policy declined the credential", "credential", name, "reason", reason)
		return false
	}
	return true
}

func updateCredential(ctx workflow.Context, name string) (Credential, bool) {
	c := callOf(ctx)
	if c == nil || c.kind != callUpdate {
		return Credential{}, false
	}
	cred, ok := c.creds[name]
	return cred, ok
}

// CheckRetain checks, in an Update validator, that the Update carried the
// named credential and that policy would keep it. It changes nothing. On
// failure it returns the same error as [Get], so the Update is rejected.
func CheckRetain(ctx workflow.Context, name string, policy RetainPolicy) error {
	cred, found := updateCredential(ctx, name)
	if !check(ctx, runOf(ctx), name, cred, found, policy) {
		return missing(ctx, name)
	}
	return nil
}

// Retain is [CheckRetain] followed by keeping the credential for the rest of
// the run, so any coroutine can forward it. Call it first thing in the
// Update handler, before anything that yields: validators don't run on
// replay, so this is the check that catches an Update acceptance fabricated
// in history. On failure, nothing is kept.
func Retain(ctx workflow.Context, name string, policy RetainPolicy) error {
	cred, found := updateCredential(ctx, name)
	run := runOf(ctx)
	if !check(ctx, run, name, cred, found, policy) {
		return missing(ctx, name)
	}
	run.held[name] = cred
	return nil
}

// RetainFromSignal keeps a credential that the Signal just received from c
// carried, when policy allows. Call it right after receiving, before anything
// that can block, so no other coroutine receives from c in between. It
// returns an error, and logs why, when c isn't a Signal channel, nothing was
// received, the Signal didn't carry the credential, or policy declined it:
// the Workflow should then skip the Signal.
func RetainFromSignal(ctx workflow.Context, c workflow.ReceiveChannel, name string, policy RetainPolicy) error {
	run := runOf(ctx)
	if run == nil {
		check(ctx, nil, name, Credential{}, false, policy)
		return errNotRetained
	}
	signal := c.Name()
	if c != workflow.GetSignalChannel(ctx, signal) {
		workflow.GetLogger(ctx).Warn("credentials: RetainFromSignal needs a Signal channel", "channel", signal)
		return errNotRetained
	}
	run.channels[signal] = c
	// Values handed out so far; number n is the one just received. Earlier
	// ones have been received and checked.
	n := run.delivered[signal] - c.Len()
	run.prune(signal, n-1)
	if n <= 0 {
		workflow.GetLogger(ctx).Warn("credentials: no Signal received from the channel yet", "signal", signal)
		return errNotRetained
	}
	cred, found := run.signals[signal][n][name]
	if !check(ctx, run, name, cred, found, policy) {
		return errNotRetained
	}
	run.held[name] = cred
	return nil
}

// Discard drops the named credentials from what the Workflow holds.
func Discard(ctx workflow.Context, names ...string) {
	run := runOf(ctx)
	if run == nil {
		return
	}
	for _, name := range names {
		delete(run.held, name)
	}
}
