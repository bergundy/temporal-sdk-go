package credentials

import (
	"fmt"
	"log/slog"
	"strconv"
)

const redacted = "[REDACTED]"

// Credential is one credential a call carried, such as a user's token. Its
// value is hidden wherever it's printed or serialized: fmt, JSON, text
// marshaling, and slog all show "[REDACTED]". Call [Credential.Reveal] only to
// hand the value to the service that verifies it.
type Credential struct {
	name  string
	value string
}

// Name returns the credential's name, such as "user_token".
func (c Credential) Name() string { return c.name }

// Reveal returns the credential's value.
func (c Credential) Reveal() string { return c.value }

// String implements [fmt.Stringer] and returns "[REDACTED]".
func (c Credential) String() string { return redacted }

// GoString implements [fmt.GoStringer] without the value.
func (c Credential) GoString() string {
	return fmt.Sprintf("credentials.Credential{name: %q, value: %s}", c.name, redacted)
}

// Format implements [fmt.Formatter], so every verb prints "[REDACTED]".
func (c Credential) Format(f fmt.State, verb rune) {
	switch {
	case verb == 'q':
		_, _ = f.Write([]byte(strconv.Quote(redacted)))
	case verb == 'v' && f.Flag('#'):
		_, _ = f.Write([]byte(c.GoString()))
	default:
		_, _ = f.Write([]byte(redacted))
	}
}

// MarshalJSON implements [json.Marshaler] and returns "[REDACTED]", so a
// credential passed by mistake as an Activity argument doesn't reach history.
func (c Credential) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(redacted)), nil
}

// MarshalText implements [encoding.TextMarshaler] and returns "[REDACTED]".
func (c Credential) MarshalText() ([]byte, error) {
	return []byte(redacted), nil
}

// LogValue implements [slog.LogValuer] and returns "[REDACTED]".
func (c Credential) LogValue() slog.Value {
	return slog.StringValue(redacted)
}
