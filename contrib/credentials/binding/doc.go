// Package binding implements the binding layer of secure credential
// propagation. It seals named credentials into a header that only the
// receiving party can open, and binds that header to the exact call it
// travels with.
//
// A header is sealed with HPKE (RFC 9180, base mode) using DHKEM(X25519,
// HKDF-SHA256), HKDF-SHA256, and AES-256-GCM, to the receiver's public key.
// The call's context, such as the namespace, Workflow ID, and a digest of the
// exact payload bytes, is passed as the HPKE info. It's never transmitted: the
// receiver rebuilds it from what it received, so a header moved to another
// call, or paired with other payload bytes, fails to open.
//
// The package is shared by the Go SDK's credentials plugin, which installs
// [NewInterceptor] on the client connection, and Temporal Proxy, which
// installs it on its upstream connection. It imports only go.temporal.io/api,
// gRPC, protobuf, and the standard library.
//
// # Wire format
//
// A sealed header is
//
//	"v1.start." + base64url(keyID (5) | enc (32) | AES-256-GCM ciphertext | tag (16))
//
// and its plaintext is [Frame]("credentials", "v1", name₁, value₁, ...). See
// [WorkflowContext] and its siblings for the contexts, [Digest] for D(), and
// the constants in link.go for the plaintext link between an application and
// its binding layer.
package binding
