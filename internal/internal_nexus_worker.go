package internal

import (
	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/api/workflowservice/v1"
)

type nexusWorkerOptions struct {
	executionParameters workerExecutionParameters
	client              Client
	workflowService     workflowservice.WorkflowServiceClient
	operations          []nexus.UntypedOperationHandler
}

type nexusWorker struct {
	executionParameters workerExecutionParameters
	workflowService     workflowservice.WorkflowServiceClient
	worker              *baseWorker
	stopC               chan struct{}
}

func newNexusWorker(opts nexusWorkerOptions) (*nexusWorker, error) {
	workerStopChannel := make(chan struct{})

	serviceHandler, err := nexus.NewServiceHandler(nexus.ServiceHandlerOptions{
		Operations: opts.operations,
		Codec:      nexus.DefaultCodec,
		// TODO
		// dataConverter converter.DataConverter
	})
	if err != nil {
		return nil, err
	}

	params := opts.executionParameters
	params.WorkerStopChannel = getReadOnlyChannel(workerStopChannel)
	poller := newNexusTaskPoller(&nexusTaskHandler{
		serviceHandler: serviceHandler,
		baseResponse: workflowservice.RespondNexusTaskCompletedRequest{
			Namespace: opts.executionParameters.Namespace,
			Identity:  opts.executionParameters.Identity,
		},
		client: opts.client,
	}, opts.workflowService, params)

	baseWorker := newBaseWorker(baseWorkerOptions{
		pollerCount:       params.MaxConcurrentNexusTaskQueuePollers,
		pollerRate:        defaultPollerRate,
		maxConcurrentTask: params.ConcurrentNexusTaskExecutionSize,
		maxTaskPerSecond:  defaultWorkerTaskExecutionRate,
		taskWorker:        poller,
		identity:          params.Identity,
		workerType:        "NexusWorker",
		stopTimeout:       params.WorkerStopTimeout,
		fatalErrCb:        params.WorkerFatalErrorCallback,
	},
		params.Logger,
		params.MetricsHandler,
		nil,
	)

	return &nexusWorker{
		executionParameters: opts.executionParameters,
		workflowService:     opts.workflowService,
		worker:              baseWorker,
		stopC:               workerStopChannel,
	}, nil
}

// Start the worker.
func (w *nexusWorker) Start() error {
	err := verifyNamespaceExist(w.workflowService, w.executionParameters.MetricsHandler, w.executionParameters.Namespace, w.worker.logger)
	if err != nil {
		return err
	}
	w.worker.Start()
	return nil // TODO: propagate error
}

// Stop the worker.
func (w *nexusWorker) Stop() {
	close(w.stopC)
	w.worker.Stop()
}
