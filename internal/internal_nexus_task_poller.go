package internal

import (
	"context"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/internal/common/metrics"
	"go.temporal.io/sdk/log"
)

type nexusTaskPoller struct {
	basePoller
	namespace       string
	taskQueueName   string
	identity        string
	service         workflowservice.WorkflowServiceClient
	taskHandler     *nexusTaskHandler
	logger          log.Logger
	numPollerMetric *numPollerMetric
}

var _ taskPoller = &nexusTaskPoller{}

func newNexusTaskPoller(taskHandler *nexusTaskHandler, service workflowservice.WorkflowServiceClient, params workerExecutionParameters) *nexusTaskPoller {
	return &nexusTaskPoller{
		basePoller: basePoller{
			metricsHandler:       params.MetricsHandler,
			stopC:                params.WorkerStopChannel,
			workerBuildID:        params.getBuildID(),
			useBuildIDVersioning: params.UseBuildIDForVersioning,
			capabilities:         params.capabilities,
		},
		taskHandler:     taskHandler,
		service:         service,
		namespace:       params.Namespace,
		taskQueueName:   params.TaskQueue,
		identity:        params.Identity,
		logger:          params.Logger,
		numPollerMetric: newNumPollerMetric(params.MetricsHandler, metrics.PollerTypeNexusTask),
	}
}

// Poll the nexus task queue and update the num_poller metric
func (ntp *nexusTaskPoller) pollNexusTaskQueue(ctx context.Context, request *workflowservice.PollNexusTaskQueueRequest) (*workflowservice.PollNexusTaskQueueResponse, error) {
	ntp.numPollerMetric.increment()
	defer ntp.numPollerMetric.decrement()

	return ntp.service.PollNexusTaskQueue(ctx, request)
}

func (ntp *nexusTaskPoller) poll(ctx context.Context) (interface{}, error) {
	traceLog(func() {
		ntp.logger.Debug("nexusTaskPoller::Poll")
	})
	request := &workflowservice.PollNexusTaskQueueRequest{
		Namespace: ntp.namespace,
		TaskQueue: &taskqueuepb.TaskQueue{Name: ntp.taskQueueName, Kind: enumspb.TASK_QUEUE_KIND_NORMAL},
		Identity:  ntp.identity,
		// TODO
		// WorkerVersionCapabilities: &commonpb.WorkerVersionCapabilities{
		// 	BuildId:       ntp.workerBuildID,
		// 	UseVersioning: ntp.useBuildIDVersioning,
		// },
	}

	response, err := ntp.pollNexusTaskQueue(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || len(response.TaskToken) == 0 {
		// No operation info is available on empty poll. Emit using base scope.
		ntp.metricsHandler.Counter(metrics.NexusPollNoTaskCounter).Inc(1)
		return nil, nil
	}

	// TODO: schedule to start
	// metricsHandler := ntp.metricsHandler.WithTags(metrics.NexusTags("TODO", ntp.taskQueueName))
	// scheduleToStartLatency := common.TimeValue(response.GetStartedTime()).Sub(common.TimeValue(response.GetCurrentAttemptScheduledTime()))
	// metricsHandler.Timer(metrics.ActivityScheduleToStartLatency).Record(scheduleToStartLatency)

	return response, nil
}

// PollTask polls a new task
func (ntp *nexusTaskPoller) PollTask() (any, error) {
	return ntp.doPoll(ntp.poll)
}

// ProcessTask processes a new task
func (ntp *nexusTaskPoller) ProcessTask(task interface{}) error {
	if ntp.stopping() {
		return errStop
	}

	if task == nil {
		// We didn't have task, poll might have timeout.
		traceLog(func() {
			ntp.logger.Debug("Nexus task unavailable")
		})
		return nil
	}

	response := task.(*workflowservice.PollNexusTaskQueueResponse)

	metricsHandler := ntp.metricsHandler.WithTags(metrics.NexusTags("TODO", ntp.taskQueueName))

	executionStartTime := time.Now()
	// Process the nexus task.
	// TODO: figure out the metrics here
	completion, err := ntp.taskHandler.execute(ntp.taskQueueName, response)
	// err is returned in case of internal failure, such as unable to propagate context or context timeout.
	if err != nil {
		metricsHandler.Counter(metrics.NexusExecutionFailedCounter).Inc(1)
		return err
	}
	// if request.Response.StatusCode >= 400 {
	// 	metricsHandler.Counter(metrics.NexusExecutionFailedCounter).Inc(1)
	// }
	metricsHandler.Timer(metrics.NexusExecutionLatency).Record(time.Since(executionStartTime))

	// TODO
	// rpcMetricsHandler := atp.metricsHandler.WithTags(metrics.RPCTags(workflowType, activityType, metrics.NoneTagValue))
	// reportErr := reportActivityComplete(context.Background(), atp.service, request, rpcMetricsHandler)
	// if reportErr != nil {
	// 	traceLog(func() {
	// 		atp.logger.Debug("reportActivityComplete failed", tagError, reportErr)
	// 	})
	// 	return reportErr
	// }

	// metricsHandler.
	// 	Timer(metrics.NexusSucceedEndToEndLatency).
	// 	Record(time.Since(common.TimeValue(response.GetScheduledTime())))
	// TODO: rewrite this
	_, err = ntp.taskHandler.client.WorkflowService().RespondNexusTaskCompleted(context.TODO(), completion)
	return err
}
