// Package svcmgr wraps OS-level service management. On Windows it drives
// the Service Control Manager. On Linux/macOS the CLI still exposes the
// subcommand for consistency but prints native install instructions
// instead — those platforms use systemd/launchd, whose unit files ship in
// deploy/.
package svcmgr

// Action is the operation the user asked for.
type Action string

const (
	ActionInstall   Action = "install"
	ActionUninstall Action = "uninstall"
	ActionStart     Action = "start"
	ActionStop      Action = "stop"
	ActionStatus    Action = "status"
	ActionRun       Action = "run"
)

// Options is passed from main to Handle. Only Install uses the env-var set.
type Options struct {
	ServiceName string            // e.g. "privatedns"
	DisplayName string            // e.g. "privatedns DNS server"
	Description string            // free-text description
	Env         map[string]string // captured environment when calling `install`
}
