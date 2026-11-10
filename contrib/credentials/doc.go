// Package credentials propagates credentials, such as a user's token, through
// Temporal calls, sealed so that only the receiving party can open them and
// bound to the exact call they travel with.
package credentials
