# Secure credential propagation

`go.temporal.io/sdk/contrib/credentials` carries credentials, such as a user's token, through Temporal calls: from a client into a Workflow, and from the Workflow to the Activities, child Workflows, and Nexus operations that act on the user's behalf.

Each credential travels in a header sealed to the receiver's public key and bound to the exact call it travels with: the namespace, the Workflow, Update, Activity, or Nexus operation it addresses, and a digest of the call's payload bytes. Temporal never sees a credential, and a header moved to another call, or paired with other input, doesn't open. Nothing is forwarded unless the Workflow asks for it.

> [!NOTE]
> This module is experimental. Its API and wire format can change.

## Using it

### Configure the client

```go
keyset, err := binding.LoadKeyset("keyset.json")
if err != nil {
	return err
}
plugin, err := credentials.NewPlugin(credentials.PluginOptions{Keyset: keyset})
if err != nil {
	return err
}
c, err := client.Dial(client.Options{Plugins: []client.Plugin{plugin}})
if err != nil {
	return err
}
w := worker.New(c, "agents", worker.Options{}) // inherits the plugin from the client
```

Set the plugin on the client, not only on a Worker. `worker.New` panics if it finds the plugin on a Worker whose client doesn't have it, because that Worker would have no binding layer.

### Attach credentials to a call

```go
ctx := credentials.Attach(ctx, credentials.Values{"user_token": token})
run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: agentID, TaskQueue: "agents"}, Agent, input)
```

`Attach` works for starting and signaling Workflows, signal-with-start, Updates, update-with-start, and starting standalone Activities and Nexus operations. Queries never carry credentials. A call carries the values from the innermost `Attach` on its context; they don't merge with outer ones. `AttachWithKey` seals them to a named key instead of the default.

### Forward credentials from a Workflow

```go
func Agent(ctx workflow.Context, input Input) error {
	// A start without the token fails the Workflow on its first Workflow task.
	if err := credentials.Require(ctx, "user_token"); err != nil {
		return err
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
	return workflow.ExecuteActivity(credentials.Propagate(ctx, "user_token"), CallTool, input).Get(ctx, nil)
}
```

- `Propagate(ctx, names...)` returns a context whose calls forward exactly the named credentials. Calls made with `ctx` itself forward nothing, and `Propagate` with no names forwards nothing.
- `PropagateAll(ctx)` forwards everything available. `PropagateWithKey(ctx, key, names...)` seals to a named key.
- They work for Activities, Local Activities, child Workflows, continue-as-new, Signals to other Workflows, and Nexus operations.

### Use a credential in an Activity or Nexus handler

```go
func CallTool(ctx context.Context, input Input) error {
	token, err := credentials.Get(ctx, "user_token")
	if err != nil {
		return err // non-retryable: the call didn't carry it
	}
	return callService(ctx, input, token.Reveal())
}
```

A `credentials.Credential` prints as `[REDACTED]` under fmt, JSON, text marshaling, and slog. In a Nexus handler, `Get` returns a non-retryable `*nexus.HandlerError` instead of an `ApplicationError`.

### Keep credentials in a Workflow

| Call | Where | What it checks |
| --- | --- | --- |
| `Require(ctx, names...)` | Workflow function, Update validator and handler, Activity, Nexus handler | the current call carried each name |
| `Get`, `Lookup` | anywhere | the current call, then what the Workflow holds (in the Workflow function, only what it holds) |
| `CheckRetain(ctx, name, policy)` | Update validator | the Update carried it and `policy` would keep it; changes nothing |
| `Retain(ctx, name, policy)` | first thing in the Update handler | the same check, then keeps it for the run |
| `RetainFromSignal(ctx, c, name, policy)` | right after receiving from Signal channel `c` | that Signal carried it and `policy` keeps it |
| `Discard(ctx, names...)` | Workflow code | drops them from what the Workflow holds |

Retain policies are `credentials.IfAbsent` and `jwt.SameSubject`, which replaces a held JWT with one for the same `iss` and `sub`, without verifying either token.

Run every check in both the Update validator and the handler: validators don't run on replay, so the handler's check is the one that catches an acceptance fabricated in history. Errors name the credential, never the cause; the cause goes to Worker logs.

With signal-with-start, the start carries no credentials: they're bound to the Signal. Keep them with `RetainFromSignal`.

## Running the binding layer in Temporal Proxy

```go
plugin, err := credentials.NewPlugin(credentials.PluginOptions{Proxy: true})
```

In proxy mode, Temporal Proxy seals and opens credentials for the application. The application sends them in plaintext, but only to a server that identifies itself as a credential-binding proxy with the `credentials-binding: v1` response header. A call that carries credentials fails with `FailedPrecondition` otherwise. Set `Strict` in the proxy's configuration, not in the plugin. A `Keyset` is optional in proxy mode: it's used only to reopen histories the `WorkflowReplayer` reads from files.

## Keysets

```json
{
  "keys": [
    {"name": "agents", "id": "<10 hex digits>", "public": "<base64url X25519>", "private": "<base64url X25519>"},
    {"name": "payments", "id": "<10 hex digits>", "public": "<base64url X25519>"}
  ],
  "default": "agents",
  "nexusEndpoints": {"payments-endpoint": "payments"}
}
```

`binding.GenerateKey(name)` creates a key pair. A key without `private` can seal but not open. Senders pick a key, first match wins: the key named at the call site, the Nexus endpoint's override, then the default. `PluginOptions.Strict` disables the default, so a call that selects no key fails at the sender.

- **Keep private keys while Workflows can still replay.** Workflow, Update, and Signal headers are reopened on every replay, so a running Workflow pins its keys until it closes or continues as new. Activity and Nexus headers must stay openable through retries.
- **Eager Workflow start and eager Activities** deliver tasks to the starting process, so it needs the receiver's private key.

## What stays plaintext inside an application

The link between application code and its binding layer is plaintext, in the plugin and in the proxy. Inside an application, credentials sit in request and response protos:

- SDK interceptors, and gRPC interceptors that come before the plugin's in `ConnectionOptions.DialOptions` (all of them in proxy mode), see them in Temporal headers. Don't log those headers, or whole request and response messages. Credentials never travel in gRPC metadata.
- History read through a binding layer carries opened credentials, including `client.GetWorkflowHistory` in a process with the plugin, a CLI pointed at the proxy, and fixtures saved from either. Treat those histories as secrets.

Nothing in plaintext leaves the application: the binding layer seals the positions it knows and removes every other credentials field, in both directions.

## Things to know

- **Reserved headers.** `credentials`, `credentials-key`, and `credentials-opened` belong to this module. Values under these names that the module didn't write are removed.
- **gRPC-level payload codecs**, such as one built with `converter.NewPayloadCodecGRPCClientInterceptor`, visit header payloads. They must pass through payloads for which `binding.IsCredentialsPayload` is true, and come earlier in `ConnectionOptions.DialOptions` than the plugin's interceptor, which the plugin appends. A codec that encodes credentials payloads makes calls fail with an error saying so. Credentials headers can't be offloaded to external storage either. DataConverter codecs never touch headers.
- **`client.NewClientFromExisting`** shares the existing client's connection, so the existing client must have been created with the plugin. The plugin refuses to build one otherwise.
- **Plugin ordering.** The plugin puts its propagation interceptor first, so other interceptors can call `Get`, and its Signal counter last among Worker interceptors, so a Signal that another interceptor drops isn't counted. List it after plugins that add Worker interceptors.
- **`WorkflowReplayer`.** Set `PluginOptions.ReplayNamespace` to the namespace of histories replayed from files: their headers are bound to it, and the replayer doesn't know it. The plugin never trusts an opened result read from a file: it removes them and reopens with the keyset.

## Testing

`credentialstest` helps with `TestWorkflowEnvironment` and `TestActivityEnvironment`, which have no binding layer:

```go
plugin := credentialstest.NewPlugin()
env := s.NewTestWorkflowEnvironment()
env.SetWorkerOptions(worker.Options{Plugins: []worker.Plugin{plugin}})
env.SetHeader(credentialstest.StartHeader(credentials.Values{"user_token": token}))
plugin.QueueSignal("approve", credentials.Values{"approver_token": approver})
plugin.SetUpdate("update-1", credentials.Values{"approver_token": approver})
env.ExecuteWorkflow(Agent, input)
```

Credentials a Workflow forwards arrive opened at the Activity, child Workflow, or Nexus operation, so tests can assert what each receives.
