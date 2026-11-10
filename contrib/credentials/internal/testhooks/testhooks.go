// Package testhooks lets credentialstest install the propagation interceptors
// without a client, as test environments have none.
package testhooks

import "go.temporal.io/sdk/interceptor"

// Interceptors returns the interceptor that goes first in a Worker's list and
// the one that goes last. The credentials package sets it.
var Interceptors func() (first, last interceptor.WorkerInterceptor)
