package internal

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nexus-rpc/sdk-go/nexus"
	"github.com/stretchr/testify/require"
	failurepb "go.temporal.io/api/failure/v1"
	nexuspb "go.temporal.io/api/nexus/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type nexusErrorConversionTestCase struct {
	name                   string
	encodeCommonAttributes bool
}

func (tc nexusErrorConversionTestCase) requireFailureMessage(t *testing.T, nexusFailure *nexuspb.Failure, expected string) {
	t.Helper()

	if tc.encodeCommonAttributes {
		require.Equal(t, "Encoded failure", nexusFailure.Message)
	} else {
		require.Equal(t, expected, nexusFailure.Message)
	}
}

func (tc nexusErrorConversionTestCase) requireErrorMessage(t *testing.T, expected string, actual string) {
	t.Helper()

	if tc.encodeCommonAttributes {
		require.Equal(t, expected, actual)
	} else {
		require.Empty(t, actual)
	}
}

func (tc nexusErrorConversionTestCase) requireEncodedAttributes(t *testing.T, attrs []byte) {
	t.Helper()
	if tc.encodeCommonAttributes {
		require.NotEmpty(t, attrs)
	} else {
		require.Empty(t, attrs)
	}
}

var nexusErrorConversionTestCases = []nexusErrorConversionTestCase{
	{
		name:                   "EncodeCommonAttributes",
		encodeCommonAttributes: true,
	},
	{
		name:                   "DoNotEncodeCommonAttributes",
		encodeCommonAttributes: false,
	},
}

func TestErrorToFailure(t *testing.T) {
	for _, tc := range nexusErrorConversionTestCases {
		t.Run(tc.name, func(t *testing.T) {
			h := &nexusTaskHandler{
				failureConverter: NewDefaultFailureConverter(DefaultFailureConverterOptions{
					EncodeCommonAttributes: tc.encodeCommonAttributes,
				}),
			}

			t.Run("SimpleError is serialized as ApplicationFailure", func(t *testing.T) {
				err := errors.New("my simple error")
				nexusFailure, convErr := h.errorToFailure(err)
				require.NoError(t, convErr)
				require.NotNil(t, nexusFailure)

				tc.requireFailureMessage(t, nexusFailure, "my simple error")
				require.Equal(t, nexusTemporalFailureMetadata, nexusFailure.Metadata)

				var temporalFailure failurepb.Failure
				unmarshalErr := protojson.Unmarshal(nexusFailure.Details, &temporalFailure)
				require.NoError(t, unmarshalErr)

				loadedErr := h.failureConverter.FailureToError(&temporalFailure)
				var appErr *ApplicationError
				require.ErrorAs(t, loadedErr, &appErr)
				tc.requireErrorMessage(t, "my simple error", appErr.Message())
				require.Nil(t, appErr.Unwrap())
				require.Empty(t, appErr.Type())
				require.False(t, appErr.NonRetryable())
			})

			t.Run("ApplicationError is serialized as ApplicationFailure", func(t *testing.T) {
				cause := errors.New("root cause")
				err := NewApplicationError("app error message", "CustomType", true, cause, "detail1", 123)
				nexusFailure, convErr := h.errorToFailure(err)
				require.NoError(t, convErr)
				require.NotNil(t, nexusFailure)

				tc.requireFailureMessage(t, nexusFailure, "app error message")
				require.Equal(t, nexusTemporalFailureMetadata, nexusFailure.Metadata)

				var temporalFailure failurepb.Failure
				unmarshalErr := protojson.Unmarshal(nexusFailure.Details, &temporalFailure)
				require.NoError(t, unmarshalErr)

				loadedErr := h.failureConverter.FailureToError(&temporalFailure)
				var appErr *ApplicationError
				require.ErrorAs(t, loadedErr, &appErr)
				tc.requireErrorMessage(t, "app error message", appErr.Message())
				require.Equal(t, cause.Error(), appErr.Unwrap().Error())
				require.Equal(t, "CustomType", appErr.Type())
				require.True(t, appErr.NonRetryable())

				var errDetail1 string
				var errDetail2 int
				require.NoError(t, appErr.Details(&errDetail1, &errDetail2))
				require.Equal(t, "detail1", errDetail1)
				require.Equal(t, 123, errDetail2)
			})

			t.Run("NilError", func(t *testing.T) {
				nexusFailure, convErr := h.errorToFailure(nil)
				require.NoError(t, convErr)
				require.Nil(t, nexusFailure)
			})
		})
	}
}

func TestOperationErrorToFailure(t *testing.T) {
	for _, tc := range nexusErrorConversionTestCases {
		t.Run(tc.name, func(t *testing.T) {
			h := &nexusTaskHandler{
				failureConverter: NewDefaultFailureConverter(DefaultFailureConverterOptions{
					EncodeCommonAttributes: tc.encodeCommonAttributes,
				}),
			}

			t.Run("SimpleOperationError", func(t *testing.T) {
				err := nexus.NewOperationFailedError("op failed")
				nexusFailure, convErr := h.errorToFailure(err)
				require.NoError(t, convErr)
				require.NotNil(t, nexusFailure)

				tc.requireFailureMessage(t, nexusFailure, "op failed")
				require.Equal(t, nexusOperationFailureMetadata, nexusFailure.Metadata)
				require.Nil(t, nexusFailure.Cause)

				var opErr serializableNexusOperationError
				unmarshalErr := json.Unmarshal(nexusFailure.Details, &opErr)
				require.NoError(t, unmarshalErr)
				require.Equal(t, "failed", opErr.State)
				tc.requireEncodedAttributes(t, opErr.EncodedAttributes)
			})

			t.Run("OperationErrorWithCause", func(t *testing.T) {
				cause := errors.New("root cause")
				err := nexus.NewOperationCanceledError("op canceled")
				err.Cause = cause
				nexusFailure, convErr := h.operationErrorToFailure(err)
				require.NoError(t, convErr)
				require.NotNil(t, nexusFailure)

				tc.requireFailureMessage(t, nexusFailure, "op canceled")
				require.NotNil(t, nexusFailure.Cause)

				var opErr serializableNexusOperationError
				unmarshalErr := json.Unmarshal(nexusFailure.Details, &opErr)
				require.NoError(t, unmarshalErr)
				require.Equal(t, "canceled", opErr.State)
				tc.requireEncodedAttributes(t, opErr.EncodedAttributes)

				var temporalFailure failurepb.Failure
				unmarshalErr = protojson.Unmarshal(nexusFailure.Cause.Details, &temporalFailure)
				require.NoError(t, unmarshalErr)

				loadedErr := h.failureConverter.FailureToError(&temporalFailure)
				var appErr *ApplicationError
				require.ErrorAs(t, loadedErr, &appErr)
				tc.requireErrorMessage(t, "root cause", appErr.Message())
				require.Nil(t, appErr.Unwrap())
				require.Empty(t, appErr.Type())
				require.False(t, appErr.NonRetryable())
			})
		})
	}
}

func TestHandlerErrorToFailure(t *testing.T) {
	for _, tc := range nexusErrorConversionTestCases {
		t.Run(tc.name, func(t *testing.T) {
			h := &nexusTaskHandler{
				failureConverter: NewDefaultFailureConverter(DefaultFailureConverterOptions{
					EncodeCommonAttributes: tc.encodeCommonAttributes,
				}),
			}

			t.Run("SimpleHandlerError", func(t *testing.T) {
				err := &nexus.HandlerError{
					Type:          nexus.HandlerErrorTypeInternal,
					Message:       "internal error",
					RetryBehavior: nexus.HandlerErrorRetryBehaviorNonRetryable,
				}
				nexusFailure, convErr := h.handlerErrorToFailure(err)
				require.NoError(t, convErr)
				require.NotNil(t, nexusFailure)

				tc.requireFailureMessage(t, nexusFailure, "internal error")
				require.Equal(t, nexusHandlerFailureMetadata, nexusFailure.Metadata)

				var handlerErr serializableNexusHandlerError
				unmarshalErr := json.Unmarshal(nexusFailure.Details, &handlerErr)
				require.NoError(t, unmarshalErr)
				require.Equal(t, "false", handlerErr.Retryable)
				require.Equal(t, string(nexus.HandlerErrorTypeInternal), handlerErr.Type)
				tc.requireEncodedAttributes(t, handlerErr.EncodedAttributes)
			})

			t.Run("HandlerErrorWithCause", func(t *testing.T) {
				cause := errors.New("root cause")
				err := &nexus.HandlerError{
					Type:    nexus.HandlerErrorTypeNotFound,
					Message: "not found",
					Cause:   cause,
				}
				nexusFailure, convErr := h.handlerErrorToFailure(err)
				require.NoError(t, convErr)
				require.NotNil(t, nexusFailure)

				tc.requireFailureMessage(t, nexusFailure, "not found")
				require.NotNil(t, nexusFailure.Cause)

				var handlerErr serializableNexusHandlerError
				unmarshalErr := json.Unmarshal(nexusFailure.Details, &handlerErr)
				require.NoError(t, unmarshalErr)
				require.Empty(t, handlerErr.Retryable)
				require.Equal(t, string(nexus.HandlerErrorTypeNotFound), handlerErr.Type)
				tc.requireEncodedAttributes(t, handlerErr.EncodedAttributes)

				var temporalFailure failurepb.Failure
				unmarshalErr = protojson.Unmarshal(nexusFailure.Cause.Details, &temporalFailure)
				require.NoError(t, unmarshalErr)

				loadedErr := h.failureConverter.FailureToError(&temporalFailure)
				var appErr *ApplicationError
				require.ErrorAs(t, loadedErr, &appErr)
				tc.requireErrorMessage(t, "root cause", appErr.Message())
				require.Nil(t, appErr.Unwrap())
				require.Empty(t, appErr.Type())
				require.False(t, appErr.NonRetryable())
			})
		})
	}
}
