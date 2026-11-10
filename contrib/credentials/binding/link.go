package binding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	commonpb "go.temporal.io/api/common/v1"
)

// The link format between an application and its binding layer. It's
// plaintext: inside an application, credentials sit in request and response
// protos. The binding layer seals or strips every one of these fields before a
// request leaves the application.
//
// In a Temporal header (map<string, Payload>):
//
//   - Pending, app to binding: "credentials" is {encoding: json/credentials-pending}
//     with a JSON object of strings. "credentials-key" optionally names a key:
//     {encoding: json/credentials-key} with a JSON string.
//   - Sealed, on the wire: "credentials" is {encoding: binary/credentials-sealed}
//     with data "v1.start.…".
//   - Opened, binding to app: the sealed payload unchanged, plus metadata
//     "credentials-opened" holding an [Opened] as JSON.
//   - Local, Local Activities only, never on the wire: "credentials" is
//     {encoding: json/credentials-local} with a JSON object of strings.
//
// In a Nexus header (map<string, string>), the same values are the header
// strings themselves, and the opened result is a "credentials-opened" header.
const (
	// HeaderCredentials carries credentials in every form.
	HeaderCredentials = "credentials"
	// HeaderKey names the key a pending value is sealed to.
	HeaderKey = "credentials-key"
	// HeaderOpened carries the [Opened] result: as payload metadata on a
	// Temporal header's sealed payload, or as a Nexus header.
	HeaderOpened = "credentials-opened"

	// EncodingSealed marks a sealed header payload.
	EncodingSealed = "binary/credentials-sealed"
	// EncodingPending marks a pending header payload, for the binding layer to seal.
	EncodingPending = "json/credentials-pending"
	// EncodingKey marks a credentials-key payload.
	EncodingKey = "json/credentials-key"
	// EncodingLocal marks credentials passed to a Local Activity.
	EncodingLocal = "json/credentials-local"

	metadataEncoding = "encoding"
)

// Reasons an [Opened] result fails. They're for Worker logs only: callers never
// see them.
const (
	ReasonMalformed   = "malformed"
	ReasonKeyNotHeld  = "key-not-held"
	ReasonDoesNotOpen = "does-not-open"
	ReasonNoEndpoint  = "no-endpoint"
)

// Opened is what the binding layer attaches to a header it tried to open: the
// credentials, or a failure marker.
type Opened struct {
	OK          bool              `json:"ok"`
	Reason      string            `json:"reason,omitempty"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

func (o Opened) marshal() []byte {
	b, _ := json.Marshal(o)
	return b
}

// ParseOpened decodes an [Opened] result.
func ParseOpened(data []byte) (Opened, error) {
	var o Opened
	if err := json.Unmarshal(data, &o); err != nil {
		return Opened{}, fmt.Errorf("credentials: invalid %s value: %w", HeaderOpened, err)
	}
	if !o.OK {
		o.Credentials = nil
	}
	return o, nil
}

// IsCredentialsPayload reports whether p is a credentials payload in any form.
// gRPC-level payload codecs, such as one built with
// converter.NewPayloadCodecGRPCClientInterceptor, visit header payloads, so
// they must pass these through unchanged.
func IsCredentialsPayload(p *commonpb.Payload) bool {
	switch payloadEncoding(p) {
	case EncodingSealed, EncodingPending, EncodingKey, EncodingLocal:
		return true
	}
	return false
}

func payloadEncoding(p *commonpb.Payload) string {
	return string(p.GetMetadata()[metadataEncoding])
}

func isSealedPayload(p *commonpb.Payload) bool {
	return payloadEncoding(p) == EncodingSealed && bytes.HasPrefix(p.GetData(), []byte(SealedPrefix))
}

func newPayload(encoding string, data []byte) *commonpb.Payload {
	return &commonpb.Payload{Metadata: map[string][]byte{metadataEncoding: []byte(encoding)}, Data: data}
}

// PendingPayload returns the pending form of values, for the binding layer to
// seal.
func PendingPayload(values map[string]string) *commonpb.Payload {
	b, _ := json.Marshal(values)
	return newPayload(EncodingPending, b)
}

// KeyPayload returns the credentials-key payload naming key.
func KeyPayload(key string) *commonpb.Payload {
	b, _ := json.Marshal(key)
	return newPayload(EncodingKey, b)
}

// LocalPayload returns the Local Activity form of values.
func LocalPayload(values map[string]string) *commonpb.Payload {
	b, _ := json.Marshal(values)
	return newPayload(EncodingLocal, b)
}

// PendingNexus returns the pending form of values for a Nexus header.
func PendingNexus(values map[string]string) string {
	b, _ := json.Marshal(values)
	return string(b)
}

// KeyNexus returns the credentials-key Nexus header value naming key.
func KeyNexus(key string) string {
	b, _ := json.Marshal(key)
	return string(b)
}

// OpenedFromHeader reads the [Opened] result attached to a Temporal header.
// It reports false when the header carries no credentials or the binding
// layer didn't attach a result.
func OpenedFromHeader(fields map[string]*commonpb.Payload) (Opened, bool) {
	p := fields[HeaderCredentials]
	if p == nil {
		return Opened{}, false
	}
	data, ok := p.GetMetadata()[HeaderOpened]
	if !ok {
		return Opened{}, false
	}
	o, err := ParseOpened(data)
	if err != nil {
		return Opened{Reason: ReasonMalformed}, true
	}
	return o, true
}

// LocalFromHeader reads Local Activity credentials from a Temporal header.
func LocalFromHeader(fields map[string]*commonpb.Payload) (map[string]string, bool) {
	p := fields[HeaderCredentials]
	if payloadEncoding(p) != EncodingLocal {
		return nil, false
	}
	values, err := parseValues(p.GetData())
	if err != nil {
		return nil, false
	}
	return values, true
}

// OpenedFromNexus reads the [Opened] result attached to a Nexus header. Keys
// are matched case-insensitively, as Nexus headers are.
func OpenedFromNexus(h map[string]string) (Opened, bool) {
	for k, v := range h {
		if strings.EqualFold(k, HeaderOpened) {
			o, err := ParseOpened([]byte(v))
			if err != nil {
				return Opened{Reason: ReasonMalformed}, true
			}
			return o, true
		}
	}
	return Opened{}, false
}

// SealedPayload returns the wire form of a sealed header.
func SealedPayload(sealed string) *commonpb.Payload {
	return newPayload(EncodingSealed, []byte(sealed))
}

// OpenedPayload returns a sealed payload with the [Opened] result attached.
// Test helpers use it to build headers in the opened form.
func OpenedPayload(sealed string, o Opened) *commonpb.Payload {
	p := SealedPayload(sealed)
	p.Metadata[HeaderOpened] = o.marshal()
	return p
}

func parseValues(data []byte) (map[string]string, error) {
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil || values == nil {
		return nil, errors.New("pending credentials must be a JSON object of strings")
	}
	return values, nil
}

func parseKeyName(data []byte) (string, error) {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return "", fmt.Errorf("the %s value must be a JSON string", HeaderKey)
	}
	return name, nil
}
