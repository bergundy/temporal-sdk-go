package binding

import (
	"bytes"
	"crypto/hpke"
	"encoding/base64"
	"errors"
	"maps"
	"slices"
	"strings"
)

// SealedPrefix starts every sealed header.
const SealedPrefix = "v1.start."

// encSize is the length of an X25519 encapsulated key.
const encSize = 32

// tagSize is the length of an AES-GCM tag.
const tagSize = 16

var (
	// ErrMalformed is returned for a header that isn't one this package wrote.
	ErrMalformed = errors.New("malformed header")

	// ErrKeyNotHeld is returned when a header is sealed to a key whose private
	// half the keyset doesn't hold.
	ErrKeyNotHeld = errors.New("header sealed to a key not held")

	// ErrOpen is returned when a header fails to open: it was edited, sealed
	// under a different context, or sealed to a different key with the same ID.
	// The cause is deliberately not distinguished.
	ErrOpen = errors.New("header does not open under this context")

	plaintextLabel = [][]byte{[]byte("credentials"), []byte("v1")}
)

func suite() (hpke.KDF, hpke.AEAD) { return hpke.HKDFSHA256(), hpke.AES256GCM() }

// Seal seals creds to key, bound to info, and returns the header value:
//
//	"v1.start." + base64url(keyID (5) | enc (32) | AES-256-GCM ciphertext | tag (16))
//
// The sealed plaintext is [Frame]("credentials", "v1", name₁, value₁, ...),
// with names sorted.
func Seal(key *Key, info []byte, creds map[string]string) (string, error) {
	fields := slices.Clone(plaintextLabel)
	for _, name := range slices.Sorted(maps.Keys(creds)) {
		fields = append(fields, []byte(name), []byte(creds[name]))
	}
	kdf, aead := suite()
	ct, err := hpke.Seal(key.public, kdf, aead, info, Frame(fields...))
	if err != nil {
		return "", err
	}
	return SealedPrefix + base64.RawURLEncoding.EncodeToString(append(key.ID[:], ct...)), nil
}

// Open opens header with the private key it names, under info, and returns
// the credentials it carries.
func (ks *Keyset) Open(info []byte, header string) (map[string]string, error) {
	raw, err := decodeSealed(header)
	if err != nil {
		return nil, err
	}
	key := ks.keys[KeyID(raw[:KeyIDSize])]
	if key == nil || key.private == nil {
		return nil, ErrKeyNotHeld
	}
	kdf, aead := suite()
	pt, err := hpke.Open(key.private, kdf, aead, info, raw[KeyIDSize:])
	if err != nil {
		return nil, ErrOpen
	}
	fields, err := unframe(pt)
	if err != nil || len(fields) < 2 || len(fields)%2 != 0 ||
		!bytes.Equal(fields[0], plaintextLabel[0]) || !bytes.Equal(fields[1], plaintextLabel[1]) {
		return nil, ErrMalformed
	}
	creds := make(map[string]string, len(fields)/2-1)
	for i := 2; i < len(fields); i += 2 {
		creds[string(fields[i])] = string(fields[i+1])
	}
	return creds, nil
}

// SealedKeyID returns the key ID a sealed header names, for logs.
func SealedKeyID(header string) (KeyID, bool) {
	raw, err := decodeSealed(header)
	if err != nil {
		return KeyID{}, false
	}
	return KeyID(raw[:KeyIDSize]), true
}

func decodeSealed(header string) ([]byte, error) {
	rest, ok := strings.CutPrefix(header, SealedPrefix)
	if !ok {
		return nil, ErrMalformed
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(raw) < KeyIDSize+encSize+tagSize {
		return nil, ErrMalformed
	}
	return raw, nil
}
