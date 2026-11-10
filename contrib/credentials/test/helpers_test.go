package credentials_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nexus-rpc/sdk-go/nexus"
	"github.com/stretchr/testify/require"
	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/credentials"
	"go.temporal.io/sdk/contrib/credentials/binding"
	"go.temporal.io/sdk/contrib/credentials/jwt"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const missing = "missing"

var activityOptions = workflow.ActivityOptions{StartToCloseTimeout: time.Minute}

// keys are the key pairs the tests seal to: the namespace default and a
// named key the payments service holds.
type keys struct {
	agents, payments binding.KeyJSON
}

func newKeys(t *testing.T) keys {
	t.Helper()
	agents, err := binding.GenerateKey("agents")
	require.NoError(t, err)
	payments, err := binding.GenerateKey("payments")
	require.NoError(t, err)
	return keys{agents: agents, payments: payments}
}

// keyset builds a keyset holding the private halves of the keys named in
// private, the public halves of the rest, and Nexus endpoint overrides.
func (k keys) keyset(t *testing.T, overrides map[string]string, private ...string) *binding.Keyset {
	t.Helper()
	f := binding.KeysetJSON{Default: "agents", NexusEndpoints: overrides}
	for _, key := range []binding.KeyJSON{k.agents, k.payments} {
		held := false
		for _, name := range private {
			held = held || name == key.Name
		}
		if !held {
			key = key.PublicOnly()
		}
		f.Keys = append(f.Keys, key)
	}
	ks, err := binding.NewKeyset(f)
	require.NoError(t, err)
	return ks
}

func (k keys) full(t *testing.T) *binding.Keyset {
	return k.keyset(t, nil, "agents", "payments")
}

func newPlugin(t *testing.T, opts credentials.PluginOptions) *credentials.Plugin {
	t.Helper()
	p, err := credentials.NewPlugin(opts)
	require.NoError(t, err)
	return p
}

func newClient(t *testing.T, opts client.Options) client.Client {
	t.Helper()
	opts.HostPort = hostPort
	opts.Namespace = namespace
	c, err := client.Dial(opts)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

func pluginClient(t *testing.T, ks *binding.Keyset) client.Client {
	return newClient(t, client.Options{Plugins: []client.Plugin{newPlugin(t, credentials.PluginOptions{Keyset: ks})}})
}

// startWorker runs a Worker with the test's Workflows, Activities, and Nexus
// service until the test ends.
func startWorker(t *testing.T, c client.Client, taskQueue string, opts worker.Options) worker.Worker {
	t.Helper()
	w := worker.New(c, taskQueue, opts)
	for _, wf := range []any{Agent, Chain, ChildToken, Approval, SignalKeeper, UpdateWithStart, Waiter} {
		w.RegisterWorkflow(wf)
	}
	w.RegisterActivity(TokenOf)
	w.RegisterNexusService(toolsService())
	require.NoError(t, w.Start())
	t.Cleanup(w.Stop)
	return w
}

func newID(prefix string) string {
	return prefix + "-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
}

// createEndpoint creates a Nexus endpoint targeting taskQueue.
func createEndpoint(t *testing.T, c client.Client, taskQueue string) string {
	t.Helper()
	name := newID("ep")
	_, err := c.OperatorService().CreateNexusEndpoint(context.Background(), &operatorservice.CreateNexusEndpointRequest{
		Spec: &nexuspb.EndpointSpec{
			Name: name,
			Target: &nexuspb.EndpointTarget{Variant: &nexuspb.EndpointTarget_Worker_{
				Worker: &nexuspb.EndpointTarget_Worker{Namespace: namespace, TaskQueue: taskQueue},
			}},
		},
	})
	require.NoError(t, err)
	return name
}

func user(token string) context.Context {
	return credentials.Attach(context.Background(), credentials.Values{"user_token": token})
}

func jwtFor(sub, nonce string) string {
	b64 := base64.RawURLEncoding.EncodeToString
	return b64([]byte(`{"alg":"none"}`)) + "." + b64([]byte(`{"iss":"idp","sub":"`+sub+`","n":"`+nonce+`"}`)) + ".sig"
}

func isMissing(err error) bool {
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		return appErr.Type() == credentials.ErrorTypeCredentialMissing && appErr.NonRetryable()
	}
	var handlerErr *nexus.HandlerError
	return errors.As(err, &handlerErr) && handlerErr.Type == nexus.HandlerErrorTypeUnauthenticated && !handlerErr.Retryable()
}

func requireMissing(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.True(t, isMissing(err), "want a missing-credential error, got %v", err)
}

// TokenOf returns the named credential the Activity received, or "missing".
func TokenOf(ctx context.Context, name string) (string, error) {
	cred, err := credentials.Get(ctx, name)
	if isMissing(err) {
		return missing, nil
	}
	return cred.Reveal(), err
}

var tokenOp = nexus.NewSyncOperation("token", func(ctx context.Context, name string, _ nexus.StartOperationOptions) (string, error) {
	cred, err := credentials.Get(ctx, name)
	if isMissing(err) {
		return missing, nil
	}
	return cred.Reveal(), err
})

func toolsService() *nexus.Service {
	s := nexus.NewService("tools")
	if err := s.Register(tokenOp); err != nil {
		panic(err)
	}
	return s
}

// Agent requires the user's token, forwards it to an Activity and, given
// endpoints, to Nexus operations, and reports what each received. It also
// makes each call once without Propagate.
func Agent(ctx workflow.Context, endpoints []string) (map[string]string, error) {
	if err := credentials.Require(ctx, "user_token"); err != nil {
		return nil, err
	}
	ctx = workflow.WithActivityOptions(ctx, activityOptions)
	out := map[string]string{}
	// Calls are made in a fixed order: replay matches commands without
	// looking at headers.
	for _, call := range []struct {
		name string
		ctx  workflow.Context
	}{{"activity", credentials.Propagate(ctx, "user_token")}, {"activity-plain", ctx}} {
		var s string
		if err := workflow.ExecuteActivity(call.ctx, TokenOf, "user_token").Get(ctx, &s); err != nil {
			return nil, err
		}
		out[call.name] = s
	}
	for _, ep := range endpoints {
		c := workflow.NewNexusClient(ep, "tools")
		for _, call := range []struct {
			name string
			ctx  workflow.Context
		}{{"nexus:" + ep, credentials.Propagate(ctx, "user_token")}, {"nexus-plain:" + ep, ctx}} {
			var s string
			if err := c.ExecuteOperation(call.ctx, tokenOp, "user_token", workflow.NexusOperationOptions{}).Get(ctx, &s); err != nil {
				return nil, err
			}
			out[call.name] = s
		}
	}
	return out, nil
}

// Chain continues as new once, forwarding everything, then forwards the
// user's token to a child Workflow.
func Chain(ctx workflow.Context, iteration int) (string, error) {
	if err := credentials.Require(ctx, "user_token"); err != nil {
		return "", err
	}
	if iteration == 0 {
		return "", workflow.NewContinueAsNewError(credentials.PropagateAll(ctx), Chain, 1)
	}
	var s string
	err := workflow.ExecuteChildWorkflow(credentials.Propagate(ctx, "user_token"), ChildToken).Get(ctx, &s)
	return s, err
}

func ChildToken(ctx workflow.Context) (string, error) {
	if err := credentials.Require(ctx, "user_token"); err != nil {
		return "", err
	}
	cred, err := credentials.Get(ctx, "user_token")
	return cred.Reveal(), err
}

// Approval keeps an approver's credential from the "approve" Update and
// refreshes the user's token with "refresh". "logout" discards the user's
// token. It returns what an Activity receives for each once "done" arrives.
func Approval(ctx workflow.Context) (map[string]string, error) {
	if err := credentials.Require(ctx, "user_token"); err != nil {
		return nil, err
	}
	ctx = workflow.WithActivityOptions(ctx, activityOptions)
	if err := workflow.SetUpdateHandlerWithOptions(ctx, "approve",
		func(ctx workflow.Context) error { return credentials.Retain(ctx, "approver_token", credentials.IfAbsent) },
		workflow.UpdateHandlerOptions{Validator: func(ctx workflow.Context) error {
			return credentials.CheckRetain(ctx, "approver_token", credentials.IfAbsent)
		}},
	); err != nil {
		return nil, err
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, "refresh",
		func(ctx workflow.Context) error { return credentials.Retain(ctx, "user_token", jwt.SameSubject) },
		workflow.UpdateHandlerOptions{Validator: func(ctx workflow.Context) error {
			return credentials.CheckRetain(ctx, "user_token", jwt.SameSubject)
		}},
	); err != nil {
		return nil, err
	}
	if err := workflow.SetQueryHandler(ctx, "holds", func(name string) (bool, error) {
		_, ok := credentials.Lookup(ctx, name)
		return ok, nil
	}); err != nil {
		return nil, err
	}
	logout := workflow.GetSignalChannel(ctx, "logout")
	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			logout.Receive(ctx, nil)
			credentials.Discard(ctx, "user_token")
		}
	})
	workflow.GetSignalChannel(ctx, "done").Receive(ctx, nil)
	out := map[string]string{}
	for _, name := range []string{"user_token", "approver_token"} {
		var s string
		if err := workflow.ExecuteActivity(credentials.Propagate(ctx, name), TokenOf, name).Get(ctx, &s); err != nil {
			return nil, err
		}
		out[name] = s
	}
	return out, nil
}

// SignalKeeper keeps the approver's credential from each "approve" Signal and
// reports what an Activity receives, or "skipped" for a Signal it drops.
func SignalKeeper(ctx workflow.Context, count int) ([]string, error) {
	ctx = workflow.WithActivityOptions(ctx, activityOptions)
	out := []string{}
	if err := credentials.Require(ctx, "user_token"); err != nil {
		out = append(out, "start-without-credentials")
	}
	c := workflow.GetSignalChannel(ctx, "approve")
	for range count {
		c.Receive(ctx, nil)
		if err := credentials.RetainFromSignal(ctx, c, "user_token", credentials.IfAbsent); err != nil {
			out = append(out, "skipped")
			continue
		}
		var s string
		if err := workflow.ExecuteActivity(credentials.Propagate(ctx, "user_token"), TokenOf, "user_token").Get(ctx, &s); err != nil {
			return nil, err
		}
		out = append(out, s)
		credentials.Discard(ctx, "user_token")
	}
	return out, nil
}

// UpdateWithStart requires the start's token and returns the Update's own.
func UpdateWithStart(ctx workflow.Context) (string, error) {
	start := credentials.Require(ctx, "user_token")
	handled := false
	if err := workflow.SetUpdateHandlerWithOptions(ctx, "token",
		func(ctx workflow.Context) (string, error) {
			handled = true
			if err := credentials.Require(ctx, "user_token"); err != nil {
				return "", err
			}
			cred, err := credentials.Get(ctx, "user_token")
			return cred.Reveal(), err
		},
		workflow.UpdateHandlerOptions{Validator: func(ctx workflow.Context) error { return credentials.Require(ctx, "user_token") }},
	); err != nil {
		return "", err
	}
	if err := workflow.Await(ctx, func() bool { return handled }); err != nil {
		return "", err
	}
	if start != nil {
		return "", start
	}
	return "start-ok", nil
}

// Waiter requires the token, waits for "go", then forwards it to an Activity.
func Waiter(ctx workflow.Context) (string, error) {
	if err := credentials.Require(ctx, "user_token"); err != nil {
		return "", err
	}
	workflow.GetSignalChannel(ctx, "go").Receive(ctx, nil)
	ctx = workflow.WithActivityOptions(ctx, activityOptions)
	var s string
	err := workflow.ExecuteActivity(credentials.Propagate(ctx, "user_token"), TokenOf, "user_token").Get(ctx, &s)
	return s, err
}
