package credentials_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/contrib/credentials"
	"go.temporal.io/sdk/contrib/credentials/jwt"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"
)

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// credential returns a Credential as the propagation layer delivers it, via
// Lookup on a context carrying an Activity call.
func credential(t *testing.T, name, value string) credentials.Credential {
	t.Helper()
	var got credentials.Credential
	env, _ := newEnv(t, credentials.Values{name: value})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		cred, err := credentials.Get(ctx, name)
		got = cred
		return err
	})
	require.NoError(t, env.GetWorkflowError())
	return got
}

func TestCredentialRedacts(t *testing.T) {
	cred := credential(t, "user_token", "eyJ-secret")
	require.Equal(t, "user_token", cred.Name())
	require.Equal(t, "eyJ-secret", cred.Reveal())

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"} {
		require.NotContains(t, fmt.Sprintf(verb, cred), "eyJ", verb)
		require.NotContains(t, fmt.Sprintf(verb, &cred), "eyJ", verb)
		require.NotContains(t, fmt.Sprintf(verb, []credentials.Credential{cred}), "eyJ", verb)
	}
	require.Contains(t, fmt.Sprintf("%#v", cred), "user_token")

	b, err := json.Marshal(map[string]any{"token": cred})
	require.NoError(t, err)
	require.JSONEq(t, `{"token":"[REDACTED]"}`, string(b))

	text, err := cred.MarshalText()
	require.NoError(t, err)
	require.Equal(t, "[REDACTED]", string(text))

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("calling", "token", cred)
	require.NotContains(t, buf.String(), "eyJ")
	require.Contains(t, buf.String(), "[REDACTED]")

	// Passed by mistake as an Activity argument.
	p, err := converter.GetDefaultDataConverter().ToPayload(cred)
	require.NoError(t, err)
	require.Equal(t, `"[REDACTED]"`, string(p.GetData()))
}

func TestIfAbsent(t *testing.T) {
	ok, _ := credentials.IfAbsent.Keep("", false, "new")
	require.True(t, ok)
	ok, reason := credentials.IfAbsent.Keep("old", true, "new")
	require.False(t, ok)
	require.NotEmpty(t, reason)
}

func TestSameSubject(t *testing.T) {
	token := func(claims string) string { return b64(`{"alg":"none"}`) + "." + b64(claims) + ".sig" }
	alice := token(`{"iss":"idp","sub":"alice","exp":1}`)
	aliceFresh := token(`{"iss":"idp","sub":"alice","exp":2}`)

	for name, tc := range map[string]struct {
		held    string
		holds   bool
		offered string
		want    bool
	}{
		"same subject":       {alice, true, aliceFresh, true},
		"nothing held":       {"", false, aliceFresh, false},
		"other subject":      {alice, true, token(`{"iss":"idp","sub":"mallory"}`), false},
		"other issuer":       {alice, true, token(`{"iss":"evil","sub":"alice"}`), false},
		"no subject":         {token(`{"iss":"idp"}`), true, token(`{"iss":"idp"}`), false},
		"not a JWT":          {alice, true, "opaque-token", false},
		"bad base64":         {alice, true, "a.!!!.c", false},
		"bad JSON":           {alice, true, "a." + b64("{") + ".c", false},
		"held malformed":     {"opaque", true, aliceFresh, false},
		"padded base64 body": {alice, true, b64(`{}`) + "." + base64.URLEncoding.EncodeToString([]byte(`{"iss":"idp","sub":"alice"}`)) + ".sig", true},
	} {
		ok, reason := jwt.SameSubject.Keep(tc.held, tc.holds, tc.offered)
		require.Equal(t, tc.want, ok, name)
		if !ok {
			require.NotEmpty(t, reason, name)
			require.NotContains(t, reason, "alice", name)
		}
	}
}
