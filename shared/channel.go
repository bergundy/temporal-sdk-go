package shared

import "time"

// SendChannel is a write only view of the Channel
type SendChannel interface {
	// Send blocks until the data is sent.
	Send(ctx Context, v interface{})

	// SendAsync try to send without blocking. It returns true if the data was sent, otherwise it returns false.
	SendAsync(v interface{}) (ok bool)

	// Close close the Channel, and prohibit subsequent sends.
	Close()
}

// ReceiveChannel is a read only view of the Channel
type ReceiveChannel interface {
	// Receive blocks until it receives a value, and then assigns the received value to the provided pointer.
	// Returns false when Channel is closed.
	// Parameter valuePtr is a pointer to the expected data structure to be received. For example:
	//  var v string
	//  c.Receive(ctx, &v)
	//
	// Note, values should not be reused for extraction here because merging on
	// top of existing values may result in unexpected behavior similar to
	// json.Unmarshal.
	Receive(ctx Context, valuePtr interface{}) (more bool)

	// ReceiveWithTimeout blocks up to timeout until it receives a value, and then assigns the received value to the
	// provided pointer.
	// Returns more value of false when Channel is closed.
	// Returns ok value of false when no value was found in the channel for the duration of timeout or
	// the ctx was canceled.
	// The valuePtr is not modified if ok is false.
	// Parameter valuePtr is a pointer to the expected data structure to be received. For example:
	//  var v string
	//  c.ReceiveWithTimeout(ctx, time.Minute, &v)
	//
	// Note, values should not be reused for extraction here because merging on
	// top of existing values may result in unexpected behavior similar to
	// json.Unmarshal.
	ReceiveWithTimeout(ctx Context, timeout time.Duration, valuePtr interface{}) (ok, more bool)

	// ReceiveAsync try to receive from Channel without blocking. If there is data available from the Channel, it
	// assign the data to valuePtr and returns true. Otherwise, it returns false immediately.
	//
	// Note, values should not be reused for extraction here because merging on
	// top of existing values may result in unexpected behavior similar to
	// json.Unmarshal.
	ReceiveAsync(valuePtr interface{}) (ok bool)

	// ReceiveAsyncWithMoreFlag is same as ReceiveAsync with extra return value more to indicate if there could be
	// more value from the Channel. The more is false when Channel is closed.
	//
	// Note, values should not be reused for extraction here because merging on
	// top of existing values may result in unexpected behavior similar to
	// json.Unmarshal.
	ReceiveAsyncWithMoreFlag(valuePtr interface{}) (ok bool, more bool)

	// Len returns the number of buffered messages plus the number of blocked Send calls.
	Len() int
}

// Channel must be used instead of native go channel by workflow code.
// Use workflow.NewChannel(ctx) method to create Channel instance.
type Channel interface {
	SendChannel
	ReceiveChannel
}
