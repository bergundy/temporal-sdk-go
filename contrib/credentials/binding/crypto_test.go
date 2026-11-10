package binding

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
)

func newKeyset(t *testing.T, def string, names ...string) (*Keyset, KeysetJSON) {
	t.Helper()
	f := KeysetJSON{Default: def}
	for _, name := range names {
		k, err := GenerateKey(name)
		require.NoError(t, err)
		f.Keys = append(f.Keys, k)
	}
	ks, err := NewKeyset(f)
	require.NoError(t, err)
	return ks, f
}

func payload(data string) *commonpb.Payload {
	return &commonpb.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte(data)}
}

func payloads(data ...string) []*commonpb.Payload {
	out := make([]*commonpb.Payload, len(data))
	for i, d := range data {
		out[i] = payload(d)
	}
	return out
}

// vectors freezes the wire format for other implementations. Regenerate
// testdata/vectors.json with UPDATE_VECTORS=1 only for a deliberate format
// change, which means a new version.
type vectors struct {
	Frame []struct {
		Fields []string `json:"fields"` // hex
		Out    string   `json:"out"`
	} `json:"frame"`
	Digest []struct {
		Payloads []*vectorPayload `json:"payloads"`
		Out      string           `json:"out"`
	} `json:"digest"`
	Contexts []struct {
		Kind   string           `json:"kind"`
		Fields []string         `json:"fields"`
		Input  []*vectorPayload `json:"input"`
		Out    string           `json:"out"`
	} `json:"contexts"`
	Open []struct {
		Keyset      KeysetJSON        `json:"keyset"`
		Info        string            `json:"info"` // hex
		Header      string            `json:"header"`
		Credentials map[string]string `json:"credentials"`
	} `json:"open"`
}

// vectorPayload is a payload in a vector: metadata values and data are
// base64. A null entry is a nil payload.
type vectorPayload struct {
	Metadata map[string]string `json:"metadata"`
	Data     string            `json:"data"`
}

func (v *vectorPayload) payload(t *testing.T) *commonpb.Payload {
	if v == nil {
		return nil
	}
	p := &commonpb.Payload{Metadata: map[string][]byte{}}
	for k, val := range v.Metadata {
		b, err := base64.StdEncoding.DecodeString(val)
		require.NoError(t, err)
		p.Metadata[k] = b
	}
	b, err := base64.StdEncoding.DecodeString(v.Data)
	require.NoError(t, err)
	p.Data = b
	return p
}

func vectorPayloads(t *testing.T, vs []*vectorPayload) []*commonpb.Payload {
	out := make([]*commonpb.Payload, len(vs))
	for i, v := range vs {
		out[i] = v.payload(t)
	}
	return out
}

func contextFor(t *testing.T, kind string, f []string, input []*commonpb.Payload) []byte {
	switch kind {
	case "workflow":
		return WorkflowContext(f[0], f[1], f[2], input)
	case "update":
		return UpdateContext(f[0], f[1], f[2], f[3], input)
	case "signal":
		return SignalContext(f[0], f[1], f[2], input)
	case "activity":
		return ActivityContext(f[0], f[1], f[2], f[3], input)
	case "nexus":
		require.Len(t, input, 1)
		return NexusContext(f[0], f[1], f[2], input[0])
	}
	t.Fatalf("unknown context kind %q", kind)
	return nil
}

func TestVectors(t *testing.T) {
	path := filepath.Join("testdata", "vectors.json")
	if os.Getenv("UPDATE_VECTORS") != "" {
		writeVectors(t, path)
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var v vectors
	require.NoError(t, json.Unmarshal(data, &v))
	require.NotEmpty(t, v.Frame)
	require.NotEmpty(t, v.Digest)
	require.Len(t, v.Contexts, 6)
	require.NotEmpty(t, v.Open)

	for _, f := range v.Frame {
		fields := make([][]byte, len(f.Fields))
		for i, h := range f.Fields {
			fields[i], err = hex.DecodeString(h)
			require.NoError(t, err)
		}
		require.Equal(t, f.Out, hex.EncodeToString(Frame(fields...)))
	}
	for _, d := range v.Digest {
		require.Equal(t, d.Out, hex.EncodeToString(Digest(vectorPayloads(t, d.Payloads))))
	}
	for _, c := range v.Contexts {
		require.Equal(t, c.Out, hex.EncodeToString(contextFor(t, c.Kind, c.Fields, vectorPayloads(t, c.Input))), c.Kind)
	}
	for _, o := range v.Open {
		ks, err := NewKeyset(o.Keyset)
		require.NoError(t, err)
		info, err := hex.DecodeString(o.Info)
		require.NoError(t, err)
		creds, err := ks.Open(info, o.Header)
		require.NoError(t, err)
		require.Equal(t, o.Credentials, creds)
	}
}

func writeVectors(t *testing.T, path string) {
	b64 := base64.StdEncoding.EncodeToString
	vp := func(md map[string]string, data string) *vectorPayload {
		m := map[string]string{}
		for k, v := range md {
			m[k] = b64([]byte(v))
		}
		return &vectorPayload{Metadata: m, Data: b64([]byte(data))}
	}
	var v vectors
	for _, fields := range [][]string{{}, {""}, {"ab", "c"}, {"a", "bc"}, {"credentials", "v1", "user_token", "eyJ"}} {
		hexFields := make([]string, len(fields))
		raw := make([][]byte, len(fields))
		for i, f := range fields {
			hexFields[i] = hex.EncodeToString([]byte(f))
			raw[i] = []byte(f)
		}
		v.Frame = append(v.Frame, struct {
			Fields []string `json:"fields"`
			Out    string   `json:"out"`
		}{hexFields, hex.EncodeToString(Frame(raw...))})
	}
	for _, list := range [][]*vectorPayload{
		{},
		{nil},
		{vp(nil, "")},
		{vp(map[string]string{"encoding": "json/plain"}, `"hi"`)},
		{vp(map[string]string{"encoding": "json/plain", "a": "1", "z": "2"}, `"a"`), vp(map[string]string{"encoding": "binary/null"}, "")},
	} {
		v.Digest = append(v.Digest, struct {
			Payloads []*vectorPayload `json:"payloads"`
			Out      string           `json:"out"`
		}{list, hex.EncodeToString(Digest(vectorPayloads(t, list)))})
	}
	input := []*vectorPayload{vp(map[string]string{"encoding": "json/plain"}, `{"task":"plan"}`)}
	for _, c := range []struct {
		kind   string
		fields []string
	}{
		{"workflow", []string{"default", "Agent", "agent-1"}},
		{"update", []string{"default", "agent-1", "approve", "update-1"}},
		{"signal", []string{"default", "agent-1", "logout"}},
		{"activity", []string{"default", "agent-1", "5", "CallLLM"}},
		{"activity", []string{"default", "", "standalone-1", "CallLLM"}},
		{"nexus", []string{"tools", "MyToolService", "callMyTool"}},
	} {
		v.Contexts = append(v.Contexts, struct {
			Kind   string           `json:"kind"`
			Fields []string         `json:"fields"`
			Input  []*vectorPayload `json:"input"`
			Out    string           `json:"out"`
		}{c.kind, c.fields, input, hex.EncodeToString(contextFor(t, c.kind, c.fields, vectorPayloads(t, input)))})
	}
	ks, f := newKeyset(t, "agents", "agents")
	info := WorkflowContext("default", "Agent", "agent-1", vectorPayloads(t, input))
	creds := map[string]string{"user_token": "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1In0.", "approver_token": "eyK"}
	key, err := ks.SealingKey("", "", false)
	require.NoError(t, err)
	header, err := Seal(key, info, creds)
	require.NoError(t, err)
	v.Open = append(v.Open, struct {
		Keyset      KeysetJSON        `json:"keyset"`
		Info        string            `json:"info"`
		Header      string            `json:"header"`
		Credentials map[string]string `json:"credentials"`
	}{f, hex.EncodeToString(info), header, creds})

	data, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o644))
}

func TestSealOpenRoundTrip(t *testing.T) {
	ks, _ := newKeyset(t, "agents", "agents")
	key, err := ks.SealingKey("", "", false)
	require.NoError(t, err)
	info := WorkflowContext("default", "Agent", "wf-1", payloads(`"hi"`))
	creds := map[string]string{"user_token": "eyJ...", "approver_token": "eyK..."}

	header, err := Seal(key, info, creds)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(header, SealedPrefix))
	require.NotContains(t, header, "eyJ")

	opened, err := ks.Open(info, header)
	require.NoError(t, err)
	require.Equal(t, creds, opened)

	id, ok := SealedKeyID(header)
	require.True(t, ok)
	require.Equal(t, key.ID, id)
}

// TestTampering covers the design's tampering table: each change to what a
// header travels with makes it fail to open.
func TestTampering(t *testing.T) {
	ks, _ := newKeyset(t, "agents", "agents")
	key, _ := ks.SealingKey("", "", false)
	seal := func(info []byte) string {
		h, err := Seal(key, info, map[string]string{"t": "v"})
		require.NoError(t, err)
		return h
	}
	args := payloads(`"a"`, `"b"`)
	wf := seal(WorkflowContext("ns", "Agent", "wf-1", args))
	update := seal(UpdateContext("ns", "wf-1", "approve", "u-1", args))
	nexusIn := payload(`{"amount":1}`)
	nexus := seal(NexusContext("payments", "PaymentsService", "transfer", nexusIn))

	for name, tc := range map[string]struct {
		header string
		info   []byte
	}{
		"changed input":           {wf, WorkflowContext("ns", "Agent", "wf-1", payloads(`"a"`, `"c"`))},
		"another valid payload":   {wf, WorkflowContext("ns", "Agent", "wf-1", payloads(`"b"`, `"b"`))},
		"reordered arguments":     {wf, WorkflowContext("ns", "Agent", "wf-1", []*commonpb.Payload{args[1], args[0]})},
		"duplicated argument":     {wf, WorkflowContext("ns", "Agent", "wf-1", append(args, args[1]))},
		"dropped argument":        {wf, WorkflowContext("ns", "Agent", "wf-1", args[:1])},
		"changed metadata":        {wf, WorkflowContext("ns", "Agent", "wf-1", []*commonpb.Payload{{Metadata: map[string][]byte{"encoding": []byte("binary/plain")}, Data: []byte(`"a"`)}, args[1]})},
		"copied to another ID":    {wf, WorkflowContext("ns", "Agent", "wf-2", args)},
		"copied to another type":  {wf, WorkflowContext("ns", "Other", "wf-1", args)},
		"another namespace":       {wf, WorkflowContext("other", "Agent", "wf-1", args)},
		"moved to another hop":    {wf, ActivityContext("ns", "wf-1", "Agent", "", args)},
		"moved Nexus endpoint":    {nexus, NexusContext("tools", "PaymentsService", "transfer", nexusIn)},
		"moved Nexus service":     {nexus, NexusContext("payments", "Other", "transfer", nexusIn)},
		"moved Nexus operation":   {nexus, NexusContext("payments", "PaymentsService", "refund", nexusIn)},
		"changed Nexus input":     {nexus, NexusContext("payments", "PaymentsService", "transfer", payload(`{"amount":2}`))},
		"old Update under new ID": {update, UpdateContext("ns", "wf-1", "approve", "u-2", args)},
		"Update to another name":  {update, UpdateContext("ns", "wf-1", "reject", "u-1", args)},
	} {
		_, err := ks.Open(tc.info, tc.header)
		require.ErrorIs(t, err, ErrOpen, name)
	}

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(wf, SealedPrefix))
	require.NoError(t, err)
	raw[len(raw)-20] ^= 1
	_, err = ks.Open(WorkflowContext("ns", "Agent", "wf-1", args), SealedPrefix+base64.RawURLEncoding.EncodeToString(raw))
	require.ErrorIs(t, err, ErrOpen, "edited bytes")

	_, err = ks.Open(nil, "eyJhbGciOi.plaintext.jwt")
	require.ErrorIs(t, err, ErrMalformed)
	_, err = ks.Open(nil, SealedPrefix+"AAAA")
	require.ErrorIs(t, err, ErrMalformed)
}

func TestPublicOnlyKeysetCannotOpen(t *testing.T) {
	receiver, f := newKeyset(t, "agents", "agents")
	f.Keys[0] = f.Keys[0].PublicOnly()
	sender, err := NewKeyset(f)
	require.NoError(t, err)
	require.False(t, sender.CanOpen())
	require.True(t, receiver.CanOpen())

	key, _ := sender.SealingKey("", "", false)
	info := NexusContext("ep", "svc", "op", payload("x"))
	header, err := Seal(key, info, map[string]string{"t": "v"})
	require.NoError(t, err)
	_, err = sender.Open(info, header)
	require.ErrorIs(t, err, ErrKeyNotHeld)
	_, err = receiver.Open(info, header)
	require.NoError(t, err)
}

func TestSealingKeySelection(t *testing.T) {
	_, f := newKeyset(t, "agents", "agents", "payments")
	f.NexusEndpoints = map[string]string{"payments-ep": "payments"}
	ks, err := NewKeyset(f)
	require.NoError(t, err)
	id := func(name, ep string, strict bool) string {
		k, err := ks.SealingKey(name, ep, strict)
		require.NoError(t, err)
		return hex.EncodeToString(k.ID[:])
	}
	require.Equal(t, f.Keys[0].ID, id("", "", false))
	require.Equal(t, f.Keys[1].ID, id("", "payments-ep", false))
	require.Equal(t, f.Keys[0].ID, id("", "tools-ep", false))
	require.Equal(t, f.Keys[0].ID, id("agents", "payments-ep", false))
	require.Equal(t, f.Keys[1].ID, id("", "payments-ep", true))
	require.Equal(t, f.Keys[0].ID, id("agents", "", true))

	_, err = ks.SealingKey("nope", "", false)
	require.ErrorIs(t, err, ErrUnknownKeyName)
	_, err = ks.SealingKey("", "tools-ep", true)
	require.ErrorIs(t, err, ErrNoKey)
}

func TestParseKeysetErrors(t *testing.T) {
	_, f := newKeyset(t, "", "a")
	k := f.Keys[0]
	other, err := GenerateKey("b")
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		f    KeysetJSON
		want string
	}{
		"no name":         {KeysetJSON{Keys: []KeyJSON{{ID: k.ID, Public: k.Public}}}, "a name is required"},
		"bad id":          {KeysetJSON{Keys: []KeyJSON{{Name: "a", ID: "zz", Public: k.Public}}}, "id must be 5 hex-encoded bytes"},
		"mismatched pair": {KeysetJSON{Keys: []KeyJSON{{Name: "a", ID: k.ID, Public: k.Public, Private: other.Private}}}, "does not match"},
		"duplicate id":    {KeysetJSON{Keys: []KeyJSON{k, {Name: "b", ID: k.ID, Public: other.Public}}}, "duplicate key ID"},
		"duplicate name":  {KeysetJSON{Keys: []KeyJSON{k, {Name: "a", ID: other.ID, Public: other.Public}}}, "duplicate key name"},
		"unknown default": {KeysetJSON{Keys: []KeyJSON{k}, Default: "x"}, "unknown key name"},
		"unknown nexus":   {KeysetJSON{Keys: []KeyJSON{k}, NexusEndpoints: map[string]string{"ep": "x"}}, "unknown key name"},
	} {
		_, err := NewKeyset(tc.f)
		require.ErrorContains(t, err, tc.want, name)
	}
	_, err = ParseKeyset([]byte("{"))
	require.ErrorContains(t, err, "invalid keyset")
}

func TestFrameIsUnambiguous(t *testing.T) {
	require.NotEqual(t, Frame([]byte("ab"), []byte("c")), Frame([]byte("a"), []byte("bc")))
	require.NotEqual(t, Frame([]byte("")), Frame())
	fields, err := unframe(Frame([]byte("a"), nil, []byte("bc")))
	require.NoError(t, err)
	require.Equal(t, [][]byte{[]byte("a"), {}, []byte("bc")}, fields)
	_, err = unframe(append(Frame([]byte("a")), 0))
	require.ErrorIs(t, err, ErrMalformed)
}

func TestDigestIgnoresMetadataOrder(t *testing.T) {
	// Map serialization order varies; the digest sorts metadata keys.
	a := &commonpb.Payload{Metadata: map[string][]byte{"a": []byte("1"), "b": []byte("2")}, Data: []byte("x")}
	b := &commonpb.Payload{Metadata: map[string][]byte{"b": []byte("2"), "a": []byte("1")}, Data: []byte("x")}
	require.Equal(t, Digest([]*commonpb.Payload{a}), Digest([]*commonpb.Payload{b}))
	require.NotEqual(t, Digest([]*commonpb.Payload{nil}), Digest([]*commonpb.Payload{{}}))
}
