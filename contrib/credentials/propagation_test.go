package credentials_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/contrib/credentials"
	"go.temporal.io/sdk/contrib/credentials/credentialstest"
	"go.temporal.io/sdk/contrib/credentials/jwt"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

var activityOptions = workflow.ActivityOptions{StartToCloseTimeout: time.Minute}

func newEnv(t *testing.T, start credentials.Values, extra ...interceptor.WorkerInterceptor) (*testsuite.TestWorkflowEnvironment, *credentialstest.Plugin) {
	t.Helper()
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	plugin := credentialstest.NewPlugin()
	env.SetWorkerOptions(worker.Options{Plugins: []worker.Plugin{plugin}, Interceptors: extra})
	if start != nil {
		env.SetHeader(credentialstest.StartHeader(start))
	}
	return env, plugin
}

// tokenOf is an Activity that returns the named credential, or "missing".
func tokenOf(ctx context.Context, name string) (string, error) {
	cred, err := credentials.Get(ctx, name)
	if err != nil {
		var appErr *temporal.ApplicationError
		if errors.As(err, &appErr) && appErr.Type() == credentials.ErrorTypeCredentialMissing && appErr.NonRetryable() {
			return "missing", nil
		}
		return "", err
	}
	return cred.Reveal(), nil
}

func requireMissing(t *testing.T, err error, name string) {
	t.Helper()
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, credentials.ErrorTypeCredentialMissing, appErr.Type())
	require.True(t, appErr.NonRetryable())
	require.Contains(t, appErr.Error(), name)
}

func TestRequireOnStart(t *testing.T) {
	wf := func(ctx workflow.Context) error {
		return credentials.Require(ctx, "user_token")
	}

	env, _ := newEnv(t, credentials.Values{"user_token": "eyJ-user"})
	env.ExecuteWorkflow(wf)
	require.NoError(t, env.GetWorkflowError())

	env, _ = newEnv(t, nil)
	env.ExecuteWorkflow(wf)
	requireMissing(t, env.GetWorkflowError(), "user_token")

	// A failure marker counts as missing.
	env, _ = newEnv(t, nil)
	env.SetHeader(credentialstest.StartHeader(nil))
	env.ExecuteWorkflow(wf)
	requireMissing(t, env.GetWorkflowError(), "user_token")
}

func TestPropagateToActivities(t *testing.T) {
	wf := func(ctx workflow.Context) (map[string]string, error) {
		ctx = workflow.WithActivityOptions(ctx, activityOptions)
		lctx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{StartToCloseTimeout: time.Minute})
		results := map[string]string{}
		run := func(name string, f workflow.Future) error {
			var s string
			if err := f.Get(ctx, &s); err != nil {
				return err
			}
			results[name] = s
			return nil
		}
		for name, f := range map[string]workflow.Future{
			"plain":            workflow.ExecuteActivity(ctx, tokenOf, "user_token"),
			"propagate":        workflow.ExecuteActivity(credentials.Propagate(ctx, "user_token"), tokenOf, "user_token"),
			"propagate-none":   workflow.ExecuteActivity(credentials.Propagate(ctx), tokenOf, "user_token"),
			"propagate-other":  workflow.ExecuteActivity(credentials.Propagate(ctx, "approver_token"), tokenOf, "user_token"),
			"propagate-all":    workflow.ExecuteActivity(credentials.PropagateAll(ctx), tokenOf, "admin_token"),
			"with-key":         workflow.ExecuteActivity(credentials.PropagateWithKey(ctx, "payments", "user_token"), tokenOf, "user_token"),
			"local":            workflow.ExecuteLocalActivity(credentials.Propagate(lctx, "user_token"), tokenOf, "user_token"),
			"local-plain":      workflow.ExecuteLocalActivity(lctx, tokenOf, "user_token"),
			"propagate-absent": workflow.ExecuteActivity(credentials.Propagate(ctx, "nope"), tokenOf, "nope"),
		} {
			if err := run(name, f); err != nil {
				return nil, err
			}
		}
		return results, nil
	}

	env, _ := newEnv(t, credentials.Values{"user_token": "eyJ-user", "admin_token": "eyJ-admin"})
	env.RegisterActivity(tokenOf)
	env.ExecuteWorkflow(wf)
	require.NoError(t, env.GetWorkflowError())
	var got map[string]string
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, map[string]string{
		"plain":            "missing",
		"propagate":        "eyJ-user",
		"propagate-none":   "missing",
		"propagate-other":  "missing",
		"propagate-all":    "eyJ-admin",
		"with-key":         "eyJ-user",
		"local":            "eyJ-user",
		"local-plain":      "missing",
		"propagate-absent": "missing",
	}, got)
}

func TestDiscard(t *testing.T) {
	wf := func(ctx workflow.Context) (string, error) {
		ctx = workflow.WithActivityOptions(ctx, activityOptions)
		credentials.Discard(ctx, "user_token")
		if _, ok := credentials.Lookup(ctx, "user_token"); ok {
			return "", errors.New("still held")
		}
		var s string
		err := workflow.ExecuteActivity(credentials.Propagate(ctx, "user_token"), tokenOf, "user_token").Get(ctx, &s)
		return s, err
	}
	env, _ := newEnv(t, credentials.Values{"user_token": "eyJ-user"})
	env.RegisterActivity(tokenOf)
	env.ExecuteWorkflow(wf)
	var got string
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, "missing", got)
}

func TestPropagateToChildWorkflow(t *testing.T) {
	child := func(ctx workflow.Context) (string, error) {
		if err := credentials.Require(ctx, "user_token"); err != nil {
			return "", err
		}
		cred, err := credentials.Get(ctx, "user_token")
		return cred.Reveal(), err
	}
	parent := func(ctx workflow.Context) (string, error) {
		var s string
		err := workflow.ExecuteChildWorkflow(credentials.Propagate(ctx, "user_token"), child).Get(ctx, &s)
		if err != nil {
			return "", err
		}
		// Without Propagate, the child fails its Require.
		err = workflow.ExecuteChildWorkflow(ctx, child).Get(ctx, nil)
		var appErr *temporal.ApplicationError
		if !errors.As(err, &appErr) || appErr.Type() != credentials.ErrorTypeCredentialMissing {
			return "", errors.New("the unpropagated child didn't fail its Require")
		}
		return s, nil
	}
	env, _ := newEnv(t, credentials.Values{"user_token": "eyJ-user"})
	env.RegisterWorkflow(child)
	env.ExecuteWorkflow(parent)
	var got string
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, "eyJ-user", got)
}

func TestPropagateToNexus(t *testing.T) {
	op := nexus.NewSyncOperation("token", func(ctx context.Context, name string, _ nexus.StartOperationOptions) (string, error) {
		cred, err := credentials.Get(ctx, name)
		if err != nil {
			var handlerErr *nexus.HandlerError
			if errors.As(err, &handlerErr) && !handlerErr.Retryable() && handlerErr.Type == nexus.HandlerErrorTypeUnauthenticated {
				return "missing", nil
			}
			return "", err
		}
		return cred.Reveal(), nil
	})
	service := nexus.NewService("tools")
	require.NoError(t, service.Register(op))

	wf := func(ctx workflow.Context) ([]string, error) {
		c := workflow.NewNexusClient("endpoint", "tools")
		var out []string
		for _, ctx := range []workflow.Context{credentials.Propagate(ctx, "user_token"), ctx} {
			var s string
			if err := c.ExecuteOperation(ctx, op, "user_token", workflow.NexusOperationOptions{}).Get(ctx, &s); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	}
	env, _ := newEnv(t, credentials.Values{"user_token": "eyJ-user"})
	env.RegisterNexusService(service)
	env.ExecuteWorkflow(wf)
	var got []string
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, []string{"eyJ-user", "missing"}, got)
}

func jwtFor(sub string) string {
	return "eyJhbGciOiJub25lIn0." + b64(`{"iss":"idp","sub":"`+sub+`"}`) + ".sig"
}

// approvalWorkflow follows the design's example: an approver's credential is
// kept with IfAbsent, the user's refreshed with SameSubject.
func approvalWorkflow(ctx workflow.Context) (map[string]string, error) {
	if err := credentials.Require(ctx, "user_token"); err != nil {
		return nil, err
	}
	ctx = workflow.WithActivityOptions(ctx, activityOptions)
	approved, done := false, false
	err := workflow.SetUpdateHandlerWithOptions(ctx, "approve",
		func(ctx workflow.Context) error {
			if err := credentials.Retain(ctx, "approver_token", credentials.IfAbsent); err != nil {
				return err
			}
			approved = true
			return nil
		},
		workflow.UpdateHandlerOptions{Validator: func(ctx workflow.Context) error {
			return credentials.CheckRetain(ctx, "approver_token", credentials.IfAbsent)
		}},
	)
	if err != nil {
		return nil, err
	}
	err = workflow.SetUpdateHandlerWithOptions(ctx, "refresh",
		func(ctx workflow.Context) error { return credentials.Retain(ctx, "user_token", jwt.SameSubject) },
		workflow.UpdateHandlerOptions{Validator: func(ctx workflow.Context) error {
			return credentials.CheckRetain(ctx, "user_token", jwt.SameSubject)
		}},
	)
	if err != nil {
		return nil, err
	}
	// Forwards the Update's own credential from inside its handler, without
	// keeping it.
	var forwarded string
	err = workflow.SetUpdateHandler(ctx, "forward", func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, activityOptions)
		return workflow.ExecuteActivity(credentials.Propagate(ctx, "approver_token"), tokenOf, "approver_token").Get(ctx, &forwarded)
	})
	if err != nil {
		return nil, err
	}
	if err := workflow.SetUpdateHandler(ctx, "done", func(workflow.Context) error { done = true; return nil }); err != nil {
		return nil, err
	}
	if err := workflow.Await(ctx, func() bool { return approved && done && forwarded != "" }); err != nil {
		return nil, err
	}
	out := map[string]string{"forwarded": forwarded}
	for _, name := range []string{"user_token", "approver_token"} {
		var s string
		if err := workflow.ExecuteActivity(credentials.Propagate(ctx, name), tokenOf, name).Get(ctx, &s); err != nil {
			return nil, err
		}
		out[name] = s
	}
	return out, nil
}

type updateResult struct {
	rejected error
	err      error
}

func sendUpdate(env *testsuite.TestWorkflowEnvironment, name, id string, results map[string]*updateResult) {
	r := &updateResult{}
	results[id] = r
	env.UpdateWorkflow(name, id, &testsuite.TestUpdateCallback{
		OnAccept:   func() {},
		OnReject:   func(err error) { r.rejected = err },
		OnComplete: func(_ any, err error) { r.err = err },
	})
}

func TestUpdatesRetainAndRefresh(t *testing.T) {
	env, plugin := newEnv(t, credentials.Values{"user_token": jwtFor("alice")})
	env.RegisterActivity(tokenOf)
	results := map[string]*updateResult{}

	plugin.SetUpdate("refresh-other", credentials.Values{"user_token": jwtFor("mallory")})
	plugin.SetUpdate("refresh-malformed", credentials.Values{"user_token": "not-a-jwt"})
	plugin.SetUpdate("refresh-ok", credentials.Values{"user_token": jwtFor("alice") + "fresh"})
	plugin.SetUpdate("approve-none", nil)
	plugin.SetUpdate("approve-1", credentials.Values{"approver_token": "eyJ-approver"})
	plugin.SetUpdate("approve-2", credentials.Values{"approver_token": "eyJ-second"})
	plugin.SetUpdate("forward-1", credentials.Values{"approver_token": "eyJ-forwarded"})
	env.RegisterDelayedCallback(func() {
		sendUpdate(env, "refresh", "refresh-other", results)
		sendUpdate(env, "refresh", "refresh-malformed", results)
		sendUpdate(env, "refresh", "refresh-ok", results)
		sendUpdate(env, "approve", "approve-none", results)
		sendUpdate(env, "approve", "approve-1", results)
		sendUpdate(env, "approve", "approve-2", results)
		sendUpdate(env, "forward", "forward-1", results)
		sendUpdate(env, "done", "done-1", results)
	}, time.Second)

	env.ExecuteWorkflow(approvalWorkflow)
	require.NoError(t, env.GetWorkflowError())

	requireMissing(t, results["refresh-other"].rejected, "user_token")
	requireMissing(t, results["refresh-malformed"].rejected, "user_token")
	require.NoError(t, results["refresh-ok"].rejected)
	require.NoError(t, results["refresh-ok"].err)
	requireMissing(t, results["approve-none"].rejected, "approver_token")
	require.NoError(t, results["approve-1"].rejected)
	requireMissing(t, results["approve-2"].rejected, "approver_token")
	require.NoError(t, results["forward-1"].err)

	var got map[string]string
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, map[string]string{
		"user_token":     jwtFor("alice") + "fresh",
		"approver_token": "eyJ-approver",
		"forwarded":      "eyJ-forwarded",
	}, got)
}

// signalWorkflow keeps an approver's credential from "approve" Signals,
// receiving them the way mode says, and returns the approvers it kept.
func signalWorkflow(ctx workflow.Context, mode string, count int) ([]string, error) {
	ctx = workflow.WithActivityOptions(ctx, activityOptions)
	c := workflow.GetSignalChannel(ctx, "approve")
	var kept []string
	keep := func(c workflow.ReceiveChannel) error {
		if err := credentials.RetainFromSignal(ctx, c, "approver_token", credentials.IfAbsent); err != nil {
			kept = append(kept, "skipped")
			return nil
		}
		var s string
		if err := workflow.ExecuteActivity(credentials.Propagate(ctx, "approver_token"), tokenOf, "approver_token").Get(ctx, &s); err != nil {
			return err
		}
		kept = append(kept, s)
		credentials.Discard(ctx, "approver_token")
		return nil
	}
	for range count {
		switch mode {
		case "receive":
			c.Receive(ctx, nil)
		case "selector":
			s := workflow.NewSelector(ctx)
			s.AddReceive(c, func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, nil) })
			s.Select(ctx)
		case "async":
			if err := workflow.Await(ctx, func() bool { return c.Len() > 0 }); err != nil {
				return nil, err
			}
			c.ReceiveAsync(nil)
		case "timeout":
			if ok, _ := c.ReceiveWithTimeout(ctx, time.Hour, nil); !ok {
				return nil, errors.New("timed out")
			}
		}
		if err := keep(c); err != nil {
			return nil, err
		}
	}
	return kept, nil
}

func TestRetainFromSignal(t *testing.T) {
	for _, mode := range []string{"receive", "selector", "async", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			env, plugin := newEnv(t, credentials.Values{"user_token": "eyJ-user"})
			env.RegisterActivity(tokenOf)
			signal := func(values credentials.Values) {
				plugin.QueueSignal("approve", values)
				env.SignalWorkflow("approve", nil)
			}
			// First-task Signals, buffered before the Workflow receives any.
			env.RegisterDelayedCallback(func() {
				signal(credentials.Values{"approver_token": "eyJ-first"})
				signal(nil)
			}, 0)
			env.RegisterDelayedCallback(func() {
				signal(credentials.Values{"approver_token": "eyJ-third"})
			}, time.Minute)
			env.ExecuteWorkflow(signalWorkflow, mode, 3)
			require.NoError(t, env.GetWorkflowError())
			var got []string
			require.NoError(t, env.GetWorkflowResult(&got))
			require.Equal(t, []string{"eyJ-first", "skipped", "eyJ-third"}, got)
		})
	}
}

// dropFirst drops the first Signal of each name before it reaches its
// channel.
type dropFirst struct {
	interceptor.WorkerInterceptorBase
}

func (d *dropFirst) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	i := &dropFirstInbound{seen: map[string]bool{}}
	i.Next = next
	return i
}

type dropFirstInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	seen map[string]bool
}

func (d *dropFirstInbound) HandleSignal(ctx workflow.Context, in *interceptor.HandleSignalInput) error {
	if !d.seen[in.SignalName] {
		d.seen[in.SignalName] = true
		return nil
	}
	return d.Next.HandleSignal(ctx, in)
}

func TestRetainFromSignalCountsOnlyDeliveredSignals(t *testing.T) {
	env, plugin := newEnv(t, credentials.Values{"user_token": "eyJ-user"}, &dropFirst{})
	env.RegisterActivity(tokenOf)
	env.RegisterDelayedCallback(func() {
		plugin.QueueSignal("approve", credentials.Values{"approver_token": "eyJ-dropped"})
		env.SignalWorkflow("approve", nil)
		plugin.QueueSignal("approve", credentials.Values{"approver_token": "eyJ-delivered"})
		env.SignalWorkflow("approve", nil)
	}, time.Second)
	env.ExecuteWorkflow(signalWorkflow, "receive", 1)
	var got []string
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, []string{"eyJ-delivered"}, got)
}

func TestRetainFromSignalRejectsOtherChannels(t *testing.T) {
	wf := func(ctx workflow.Context) error {
		c := workflow.NewChannel(ctx)
		workflow.Go(ctx, func(ctx workflow.Context) { c.Send(ctx, 1) })
		c.Receive(ctx, nil)
		if err := credentials.RetainFromSignal(ctx, c, "approver_token", credentials.IfAbsent); err == nil {
			return errors.New("retained from a channel that isn't a Signal's")
		}
		if err := credentials.RetainFromSignal(ctx, workflow.GetSignalChannel(ctx, "never"), "x", credentials.IfAbsent); err == nil {
			return errors.New("retained before anything was received")
		}
		return nil
	}
	env, _ := newEnv(t, nil)
	env.ExecuteWorkflow(wf)
	require.NoError(t, env.GetWorkflowError())
}

func TestActivityRequire(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	act := func(ctx context.Context) (string, error) {
		if err := credentials.Require(ctx, "user_token"); err != nil {
			return "", err
		}
		cred, err := credentials.Get(ctx, "user_token")
		return cred.Reveal(), err
	}

	env := s.NewTestActivityEnvironment()
	env.SetWorkerOptions(worker.Options{Plugins: []worker.Plugin{credentialstest.NewPlugin()}})
	env.SetHeader(credentialstest.ActivityHeader(credentials.Values{"user_token": "eyJ-user"}))
	env.RegisterActivity(act)
	v, err := env.ExecuteActivity(act)
	require.NoError(t, err)
	var got string
	require.NoError(t, v.Get(&got))
	require.Equal(t, "eyJ-user", got)

	env = s.NewTestActivityEnvironment()
	env.SetWorkerOptions(worker.Options{Plugins: []worker.Plugin{credentialstest.NewPlugin()}})
	env.RegisterActivity(act)
	_, err = env.ExecuteActivity(act)
	requireMissing(t, err, "user_token")
}
