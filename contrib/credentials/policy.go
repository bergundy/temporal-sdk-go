package credentials

import "go.temporal.io/sdk/contrib/credentials/internal/policy"

// RetainPolicy decides whether a Workflow keeps a credential a call carried in
// place of the one it holds under the same name. Policies are opaque values:
// v1 has only [IfAbsent] and jwt.SameSubject, with no unconditional or custom
// policies until credentials can be validated.
type RetainPolicy interface {
	policy.Policy
}

type ifAbsent struct{ policy.Base }

func (ifAbsent) Keep(_ string, holds bool, _ string) (bool, string) {
	if holds {
		return false, "the Workflow already holds this credential"
	}
	return true, ""
}

// IfAbsent keeps a credential only when the Workflow holds none under that
// name. The first one to arrive is kept until the Workflow discards it.
var IfAbsent RetainPolicy = ifAbsent{}
