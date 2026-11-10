// Package credentials propagates credentials, such as a user's token, through
// Temporal calls: from a client into a Workflow, and from the Workflow to the
// Activities, child Workflows, and Nexus operations that act on the user's
// behalf.
//
// Each credential travels in a header sealed to the receiver's public key and
// bound to the exact call it travels with, so Temporal never sees it and a
// header moved to another call doesn't open. Nothing is forwarded unless the
// Workflow asks for it with [Propagate].
//
// Set the [Plugin] on the client; Workers made from it inherit it:
//
//	plugin, err := credentials.NewPlugin(credentials.PluginOptions{Keyset: keyset})
//	c, err := client.Dial(client.Options{Plugins: []client.Plugin{plugin}})
//
// Attach credentials to a client call with [Attach], forward them from a
// Workflow with [Propagate], and read them with [Get] or [Require]. See the
// README for the full guide.
package credentials
