// Package jwt provides a retain policy for credentials that are JWTs.
package jwt

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"go.temporal.io/sdk/contrib/credentials"
	"go.temporal.io/sdk/contrib/credentials/internal/policy"
)

type sameSubject struct{ policy.Base }

// SameSubject keeps a JWT in place of the one the Workflow holds when both
// name the same subject from the same issuer (their "iss" and "sub" claims).
// It's meant for refreshing a session's token before it expires.
//
// It doesn't verify either token: it reads their claims as they are. The
// service each token is forwarded to verifies it. It declines a malformed
// token, and it declines when the Workflow holds nothing, so it can't install
// a first token.
var SameSubject credentials.RetainPolicy = sameSubject{}

func (sameSubject) Keep(held string, holds bool, offered string) (bool, string) {
	if !holds {
		return false, "the Workflow holds no token to compare with"
	}
	h, err := subject(held)
	if err != nil {
		return false, "the held token " + err.Error()
	}
	o, err := subject(offered)
	if err != nil {
		return false, "the offered token " + err.Error()
	}
	if h != o {
		return false, "the tokens name different subjects"
	}
	return true, ""
}

type claims struct {
	Issuer  string `json:"iss"`
	Subject string `json:"sub"`
}

func subject(token string) (claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}, errors.New("isn't a JWT")
	}
	body, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return claims{}, errors.New("has a malformed payload")
	}
	var c claims
	if err := json.Unmarshal(body, &c); err != nil {
		return claims{}, errors.New("has a malformed payload")
	}
	if c.Subject == "" {
		return claims{}, errors.New("has no subject")
	}
	return c, nil
}
