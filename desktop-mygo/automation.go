//go:build e2e

package main

// A test build exposes the native accessibility tree over MYGO_AUTOMATION.
// Production builds do not include this plugin or its control socket.
import _ "github.com/egoist/mygo/plugins/automation"
