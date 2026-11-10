package credentials

import (
	"context"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/contrib/credentials/binding"
	"go.temporal.io/sdk/interceptor"
)

func (o *outer) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	i := &activityInbound{}
	i.Next = next
	return i
}

type activityInbound struct {
	interceptor.ActivityInboundInterceptorBase
}

// ExecuteActivity reads the credentials the Activity task carried: the
// binding layer's opened result, or for a Local Activity, which never reaches
// the wire, the local form. Neither is accepted in place of the other.
func (a *activityInbound) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (any, error) {
	header := interceptor.Header(ctx)
	var c *call
	if activity.GetInfo(ctx).IsLocalActivity {
		values, _ := binding.LocalFromHeader(header)
		c = newCall(callActivity, values)
	} else {
		o, ok := binding.OpenedFromHeader(header)
		if c = callFromOpened(callActivity, o, ok); c.reason != "" {
			activity.GetLogger(ctx).Warn("credentials: the Activity's credentials header didn't open; treating its credentials as missing",
				"reason", c.reason)
		}
	}
	return a.Next.ExecuteActivity(context.WithValue(ctx, callKey{}, c), in)
}
