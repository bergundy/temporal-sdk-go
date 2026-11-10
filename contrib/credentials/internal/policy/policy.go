// Package policy seals the credentials retain policies: only packages in this
// module can implement them.
package policy

// Policy decides whether a Workflow keeps an offered credential in place of
// what it holds. Keep returns a reason when it declines, for Worker logs only.
type Policy interface {
	Keep(held string, holds bool, offered string) (ok bool, reason string)
	policy()
}

// Base is embedded by every policy implementation.
type Base struct{}

//lint:ignore U1000 the method seals Policy: only types embedding Base implement it
func (Base) policy() {}
