<!--
Release notes for go.temporal.io/sdk/contrib/credentials.
Loosely based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Add user-facing changes below under the appropriate heading (create the heading
if it does not yet exist): Added, Changed, Deprecated, Breaking Changes, Fixed,
or Security.
-->

# Changelog

## [Unreleased]

### Added

- Secure credential propagation: a client and Worker plugin that seals credentials to the receiver's public key, binds them to the call they travel with, and forwards them from Workflows only when asked (`Attach`, `Propagate`, `Get`, `Require`, `Retain`, `RetainFromSignal`, `Discard`).
- `binding`, the binding layer shared with Temporal Proxy, with test vectors for its wire format.
- `jwt.SameSubject`, a retain policy for refreshing JWTs.
- `credentialstest`, helpers for the SDK's test environments.
