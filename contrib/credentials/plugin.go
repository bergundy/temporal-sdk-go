package credentials

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"

	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/credentials/binding"
	"go.temporal.io/sdk/contrib/credentials/internal/testhooks"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// PluginName is the name the plugin reports, including in Worker heartbeats.
const PluginName = "credentials"

const pluginVersion = "0.1.0"

// replayNamespace is the placeholder namespace the WorkflowReplayer gives
// histories replayed from files.
const replayNamespace = "ReplayNamespace"

func init() {
	testhooks.Interceptors = func() (first, last interceptor.WorkerInterceptor) {
		return &outer{}, &signalCounter{}
	}
}

// PluginOptions configures [NewPlugin].
type PluginOptions struct {
	// Keyset seals the credentials this application sends and opens the ones
	// it receives, with the binding layer running in the application. It
	// holds the namespace's default key, named keys, and Nexus endpoint
	// overrides. A sender needs only public keys; a receiver needs the private
	// keys of headers sealed to it, for as long as its Workflows can replay.
	// Required unless Proxy is set.
	Keyset *binding.Keyset

	// Strict ignores the keyset's default key, so a call that names no key and
	// matches no Nexus endpoint override fails instead of being sealed to a key
	// the receiver may not hold. With Proxy, set it in the proxy instead.
	Strict bool

	// Proxy says the binding layer runs in Temporal Proxy, which seals and
	// opens credentials on the application's behalf. The application then
	// sends credentials in plaintext, and only to a server that identifies
	// itself as a credential-binding proxy. A Keyset is optional: it's used
	// only to reopen histories the WorkflowReplayer reads from files.
	Proxy bool

	// ReplayNamespace is the namespace of Workflows the WorkflowReplayer
	// replays from files. Their headers are bound to it, and the replayer
	// doesn't know it.
	ReplayNamespace string

	// Logger receives headers that fail to open and credentials dropped from
	// calls with no context to bind them to. Never their values. Defaults to
	// slog.Default().
	Logger *slog.Logger
}

// Plugin propagates credentials through Temporal calls. Set it on the client;
// Workers made from the client inherit it.
//
// It installs the binding layer, or in proxy mode a guard, as a gRPC
// interceptor on the client's connection, closest to the wire. It installs
// propagation as the first client and Worker interceptor, so other
// interceptors can call [Get], and a Signal counter as the last Worker
// interceptor. List it after plugins that add Worker interceptors.
type Plugin struct {
	//lint:ignore U1000 embedded to implement client.Plugin
	clientPluginBase
	workerPluginBase
	opts       PluginOptions
	configured atomic.Bool
}

// The aliases name the embedded fields, which would otherwise both be
// PluginBase.
type (
	//lint:ignore U1000 embedded to implement client.Plugin
	clientPluginBase = client.PluginBase
	workerPluginBase = worker.PluginBase
)

var (
	_ client.Plugin = (*Plugin)(nil)
	_ worker.Plugin = (*Plugin)(nil)
)

// NewPlugin returns a credentials plugin.
func NewPlugin(opts PluginOptions) (*Plugin, error) {
	switch {
	case !opts.Proxy && opts.Keyset == nil:
		return nil, errors.New("credentials: PluginOptions.Keyset is required unless PluginOptions.Proxy is set")
	case opts.Proxy && opts.Strict:
		return nil, errors.New("credentials: PluginOptions.Strict applies to the binding layer; in proxy mode, set it in the proxy's configuration")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Plugin{opts: opts}, nil
}

// Name implements client.Plugin and worker.Plugin.
func (p *Plugin) Name() string { return PluginName }

// Version is reported in Worker heartbeats.
func (p *Plugin) Version() string { return pluginVersion }

// ConfigureClient installs propagation first among the client's interceptors,
// and the binding layer or the proxy guard last on its connection.
func (p *Plugin) ConfigureClient(_ context.Context, opts client.PluginConfigureClientOptions) error {
	co := opts.ClientOptions
	co.Interceptors = append([]interceptor.ClientInterceptor{&outer{}}, co.Interceptors...)
	var ic grpc.UnaryClientInterceptor
	if p.opts.Proxy {
		ic = (&proxyGuard{}).unary
	} else {
		ic = binding.NewInterceptor(binding.Options{Keyset: p.opts.Keyset, Strict: p.opts.Strict, Logger: p.opts.Logger})
	}
	co.ConnectionOptions.DialOptions = append(co.ConnectionOptions.DialOptions, grpc.WithChainUnaryInterceptor(ic))
	p.configured.Store(true)
	return nil
}

// NewClient refuses to build a client from an existing one whose connection
// has no credentials interceptor: a client made from an existing one shares
// its connection, so the interceptor this plugin adds would never run.
func (p *Plugin) NewClient(
	ctx context.Context,
	options client.PluginNewClientOptions,
	next func(context.Context, client.PluginNewClientOptions) error,
) error {
	if options.FromExisting != nil {
		probeCtx, answered := binding.WithProbe(ctx)
		_, _ = options.FromExisting.WorkflowService().GetSystemInfo(probeCtx, &workflowservice.GetSystemInfoRequest{})
		if !answered() {
			return errors.New("credentials: the existing client's connection has no credentials interceptor; create it with the credentials plugin")
		}
	}
	return next(ctx, options)
}

// ConfigureWorker adds the Signal counter last among the Worker's
// interceptors. It fails, which makes worker.New panic, when this plugin was
// set on the Worker but not on its client: the Worker would have no binding
// layer.
func (p *Plugin) ConfigureWorker(_ context.Context, opts worker.PluginConfigureWorkerOptions) error {
	if !p.configured.Load() {
		return errors.New("credentials: set the credentials plugin on the client (client.Options.Plugins), not only on the Worker; Workers inherit it from the client")
	}
	opts.WorkerOptions.Interceptors = append(opts.WorkerOptions.Interceptors, &signalCounter{})
	return nil
}

// ConfigureWorkflowReplayer installs the propagation interceptors.
func (p *Plugin) ConfigureWorkflowReplayer(_ context.Context, opts worker.PluginConfigureWorkflowReplayerOptions) error {
	ro := opts.WorkflowReplayerOptions
	ro.Interceptors = append([]interceptor.WorkerInterceptor{&outer{}}, ro.Interceptors...)
	ro.Interceptors = append(ro.Interceptors, &signalCounter{})
	return nil
}

// ReplayWorkflow never trusts an opened result read from storage. For a
// history replayed from a file, and with in-process binding for a fetched one
// too, it removes every opened result and, when it has a keyset, reopens the
// sealed headers. In proxy mode, a fetched history arrives opened by the proxy
// and is used as is.
func (p *Plugin) ReplayWorkflow(
	ctx context.Context,
	options worker.PluginReplayWorkflowOptions,
	next func(context.Context, worker.PluginReplayWorkflowOptions) error,
) error {
	fromFile := options.Namespace == replayNamespace
	if p.opts.Proxy && !fromFile {
		return next(ctx, options)
	}
	keys, ns := p.opts.Keyset, options.Namespace
	if fromFile {
		ns = p.opts.ReplayNamespace
		if keys != nil && ns == "" {
			if options.Logger != nil {
				options.Logger.Warn("credentials: set PluginOptions.ReplayNamespace to reopen credentials in histories replayed from files; treating them as missing")
			}
			keys = nil
		}
	}
	history := proto.Clone(options.History).(*historypb.History)
	wfID := options.OriginalExecution.ID
	if wfID == "" && len(history.GetEvents()) > 0 {
		wfID = history.GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetWorkflowId()
	}
	binding.OpenHistory(binding.Options{Keyset: keys, Logger: p.opts.Logger}, ns, wfID, history)
	options.History = history
	return next(ctx, options)
}
