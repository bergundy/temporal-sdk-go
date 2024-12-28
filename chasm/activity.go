package chasm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/api/chasm/activity/v1"
	"go.temporal.io/api/chasm/activityservice/v1"
	"go.temporal.io/api/chasm/activityservice/v1/activityservicenexus"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/internal"
	"google.golang.org/protobuf/types/known/durationpb"
)

type ClientOptions struct {
	Address       string
	Namespace     string
	Identity      string
	DataConverter converter.DataConverter
}

type Client struct {
	options                ClientOptions
	memoizedActivityClient func() *ActivityClient
}

func NewClient(options ClientOptions) (*Client, error) {
	if options.Identity == "" {
		options.Identity = "TODO"
	}
	if options.DataConverter == nil {
		options.DataConverter = converter.GetDefaultDataConverter()
	}
	// TODO: check address?
	c := &Client{
		options: options,
	}
	c.memoizedActivityClient = sync.OnceValue(c.newActivityClient)
	return c, nil
}

func (c *Client) Activity() *ActivityClient {
	return c.memoizedActivityClient()
}

func (c *Client) newActivityClient() *ActivityClient {
	// TODO: https
	nexusClient, err := activityservicenexus.NewActivityServiceNexusHTTPClient(nexus.HTTPClientOptions{
		BaseURL: fmt.Sprintf("http://%s/nexus/endpoints/system/services", c.options.Address),
	})
	if err != nil {
		panic(err)
	}
	return &ActivityClient{
		nexusClient: nexusClient,
		options:     c.options,
	}
}

type ActivityClient struct {
	options     ClientOptions
	nexusClient *activityservicenexus.ActivityServiceNexusHTTPClient
}

type SearchAttributes = internal.SearchAttributes
type RetryPolicy = internal.RetryPolicy

type StartActivityOptions struct {
	Type                   string
	TaskQueue              string
	TypedSearchAttributes  SearchAttributes
	ScheduleToCloseTimeout time.Duration
	ScheduleToStartTimeout time.Duration
	StartToCloseTimeout    time.Duration
	HeartbeatTimeout       time.Duration
	RetryPolicy            *RetryPolicy
}

type ActivityResult struct {
	value internal.EncodedValue
}

func (r *ActivityResult) Output(v any) error {
	return r.value.Get(v)
}

func (c *ActivityClient) Start(ctx context.Context, id string, options StartActivityOptions) (*ActivityHandle, error) {
	searchAttributes, err := internal.SerializeTypedSearchAttributes(options.TypedSearchAttributes.GetUntypedValues())
	if err != nil {
		return nil, err
	}
	res, err := c.nexusClient.ExecuteActivityAsync(ctx, &activityservice.ExecuteActivityRequest{
		Identity:  c.options.Identity,
		Namespace: c.options.Namespace,
		RequestId: uuid.NewString(),
		EntityId:  id,
		Options: &activity.ActivityOptions{
			Type:      options.Type,
			TaskQueue: options.TaskQueue,
			// TODO: header from interceptor
			SearchAttributes:       searchAttributes,
			ScheduleToCloseTimeout: durationpb.New(options.ScheduleToCloseTimeout),
			ScheduleToStartTimeout: durationpb.New(options.ScheduleToStartTimeout),
			HeartbeatTimeout:       durationpb.New(options.HeartbeatTimeout),
			RetryPolicy:            internal.ConvertToPBRetryPolicy(options.RetryPolicy),
			// TODO: expose completion callbacks?
			// TODO: links from context?
			// TODO: support eager execution
		},
	}, nexus.StartOperationOptions{})
	if err != nil {
		// TODO: wrap error?
		return nil, err
	}
	return &ActivityHandle{
		ID:          id,
		RunID:       res.StartResult.RunId,
		nexusHandle: res.Pending,
		client:      c,
	}, nil
}

func (c *ActivityClient) Handle(id, runID string) *ActivityHandle {
	return &ActivityHandle{
		ID:     id,
		RunID:  runID,
		client: c,
	}
}

type ActivityHandle struct {
	ID          string
	RunID       string
	client      *ActivityClient
	nexusHandle *nexus.OperationHandle[*activityservice.ExecuteActivityResponse]
}

func (h *ActivityHandle) Result(ctx context.Context) (*ActivityResult, error) {
	if h.nexusHandle == nil {
		var err error
		h.nexusHandle, err = h.client.nexusClient.NewExecuteActivityHandle(h.ID)
		if err != nil {
			return nil, err
		}
	}
	res, err := h.nexusHandle.GetResult(ctx, nexus.GetOperationResultOptions{})
	if err != nil {
		return nil, err
	}
	return &ActivityResult{
		value: internal.NewEncodedValue(&common.Payloads{Payloads: []*common.Payload{res.Result}}, h.client.options.DataConverter),
	}, nil
}
