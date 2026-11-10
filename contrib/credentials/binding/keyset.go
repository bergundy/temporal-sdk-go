package binding

import (
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// KeyIDSize is the length of a key ID. Every sealed header carries one, so the
// receiver can pick its private key without routing.
const KeyIDSize = 5

var (
	// ErrNoKey is returned by [Keyset.SealingKey] when nothing selects a key: no
	// key was named, no Nexus endpoint override matched, and the keyset has no
	// default or the caller is strict.
	ErrNoKey = errors.New("no key selected: name a key, add a Nexus endpoint override, or set a default key (strict mode ignores the default)")

	// ErrUnknownKeyName is returned when a call names a key the keyset doesn't
	// have.
	ErrUnknownKeyName = errors.New("unknown key name")
)

type (
	// KeyID identifies an HPKE key pair.
	KeyID [KeyIDSize]byte

	// Key is an X25519 key pair, or only its public half for a sender.
	Key struct {
		ID      KeyID
		Name    string
		public  hpke.PublicKey
		private hpke.PrivateKey
	}

	// Keyset holds the keys a binding layer seals and opens with, by ID, and
	// how senders choose among them. It's safe for concurrent use.
	Keyset struct {
		keys           map[KeyID]*Key
		names          map[string]*Key
		nexusEndpoints map[string]*Key
		def            *Key
	}

	// KeysetJSON is the JSON form of a [Keyset]. Keys are referred to by name;
	// only the key ID travels on the wire.
	KeysetJSON struct {
		Keys           []KeyJSON         `json:"keys"`
		Default        string            `json:"default,omitempty"`
		NexusEndpoints map[string]string `json:"nexusEndpoints,omitempty"`
	}

	// KeyJSON is one key of a [KeysetJSON]. A key without Private can seal but
	// not open.
	KeyJSON struct {
		Name    string `json:"name"`
		ID      string `json:"id"`
		Public  string `json:"public"`
		Private string `json:"private,omitempty"`
	}
)

// GenerateKey returns a new X25519 key pair named name, with a random key ID.
func GenerateKey(name string) (KeyJSON, error) {
	sk, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return KeyJSON{}, err
	}
	var id KeyID
	if _, err := rand.Read(id[:]); err != nil {
		return KeyJSON{}, err
	}
	return KeyJSON{
		Name:    name,
		ID:      hex.EncodeToString(id[:]),
		Public:  base64.RawURLEncoding.EncodeToString(sk.PublicKey().Bytes()),
		Private: base64.RawURLEncoding.EncodeToString(sk.Bytes()),
	}, nil
}

// PublicOnly returns a copy of k without its private half.
func (k KeyJSON) PublicOnly() KeyJSON {
	k.Private = ""
	return k
}

// LoadKeyset reads a JSON keyset file. See [ParseKeyset].
func LoadKeyset(path string) (*Keyset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("credentials: reading keyset: %w", err)
	}
	return ParseKeyset(data)
}

// ParseKeyset parses a JSON keyset:
//
//	{
//	  "keys": [{"name": "agents", "id": "<10 hex>", "public": "<b64url>", "private": "<b64url>"}],
//	  "default": "agents",
//	  "nexusEndpoints": {"payments": "payments"}
//	}
//
// A key without "private" can seal but not open.
func ParseKeyset(data []byte) (*Keyset, error) {
	var f KeysetJSON
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("credentials: invalid keyset: %w", err)
	}
	return NewKeyset(f)
}

// NewKeyset builds a [Keyset] from its JSON form.
func NewKeyset(f KeysetJSON) (*Keyset, error) {
	ks := &Keyset{
		keys:           make(map[KeyID]*Key, len(f.Keys)),
		names:          make(map[string]*Key, len(f.Keys)),
		nexusEndpoints: make(map[string]*Key, len(f.NexusEndpoints)),
	}
	for i, k := range f.Keys {
		if k.Name == "" {
			return nil, fmt.Errorf("credentials: keyset keys[%d]: a name is required", i)
		}
		key, err := parseKey(k)
		if err != nil {
			return nil, fmt.Errorf("credentials: keyset keys[%d] (%q): %w", i, k.Name, err)
		}
		if _, dup := ks.keys[key.ID]; dup {
			return nil, fmt.Errorf("credentials: keyset keys[%d]: duplicate key ID %x", i, key.ID)
		}
		if _, dup := ks.names[k.Name]; dup {
			return nil, fmt.Errorf("credentials: keyset keys[%d]: duplicate key name %q", i, k.Name)
		}
		ks.keys[key.ID] = key
		ks.names[k.Name] = key
	}
	if f.Default != "" {
		if ks.def = ks.names[f.Default]; ks.def == nil {
			return nil, fmt.Errorf("credentials: keyset default: %w %q", ErrUnknownKeyName, f.Default)
		}
	}
	for ep, name := range f.NexusEndpoints {
		k := ks.names[name]
		if k == nil {
			return nil, fmt.Errorf("credentials: keyset nexusEndpoints[%q]: %w %q", ep, ErrUnknownKeyName, name)
		}
		ks.nexusEndpoints[ep] = k
	}
	return ks, nil
}

func parseKey(k KeyJSON) (*Key, error) {
	idBytes, err := hex.DecodeString(k.ID)
	if err != nil || len(idBytes) != KeyIDSize {
		return nil, fmt.Errorf("id must be %d hex-encoded bytes", KeyIDSize)
	}
	key := &Key{ID: KeyID(idBytes), Name: k.Name}

	pubBytes, err := base64.RawURLEncoding.DecodeString(k.Public)
	if err != nil {
		return nil, fmt.Errorf("public: %w", err)
	}
	pk, err := ecdh.X25519().NewPublicKey(pubBytes)
	if err != nil {
		return nil, fmt.Errorf("public: %w", err)
	}
	if key.public, err = hpke.NewDHKEMPublicKey(pk); err != nil {
		return nil, fmt.Errorf("public: %w", err)
	}
	if k.Private == "" {
		return key, nil
	}

	privBytes, err := base64.RawURLEncoding.DecodeString(k.Private)
	if err != nil {
		return nil, fmt.Errorf("private: %w", err)
	}
	sk, err := ecdh.X25519().NewPrivateKey(privBytes)
	if err != nil {
		return nil, fmt.Errorf("private: %w", err)
	}
	if !sk.PublicKey().Equal(pk) {
		return nil, errors.New("private key does not match public key")
	}
	if key.private, err = hpke.NewDHKEMPrivateKey(sk); err != nil {
		return nil, fmt.Errorf("private: %w", err)
	}
	return key, nil
}

// SealingKey picks the key a header is sealed to, first match wins: the key
// named at the call site, the Nexus endpoint's override (when nexusEndpoint is
// set), then the default unless strict.
func (ks *Keyset) SealingKey(name, nexusEndpoint string, strict bool) (*Key, error) {
	if name != "" {
		if k := ks.names[name]; k != nil {
			return k, nil
		}
		return nil, fmt.Errorf("%w %q", ErrUnknownKeyName, name)
	}
	if nexusEndpoint != "" {
		if k := ks.nexusEndpoints[nexusEndpoint]; k != nil {
			return k, nil
		}
	}
	if ks.def == nil || strict {
		return nil, ErrNoKey
	}
	return ks.def, nil
}

// CanOpen reports whether the keyset holds at least one private key.
func (ks *Keyset) CanOpen() bool {
	for _, k := range ks.keys {
		if k.private != nil {
			return true
		}
	}
	return false
}
