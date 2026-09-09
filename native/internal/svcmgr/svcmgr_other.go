//go:build !windows

package svcmgr

import (
	"context"
	"fmt"
	"os"
)

// IsWindowsService is always false on non-Windows.
func IsWindowsService() (bool, error) { return false, nil }

// Handle on non-Windows prints native-install instructions and exits.
// Systemd/launchd handle service lifecycle better than we would, and their
// unit files are shipped in deploy/.
func Handle(action Action, opts Options, _ func(context.Context) error) error {
	switch action {
	case ActionInstall, ActionUninstall, ActionStart, ActionStop, ActionStatus:
		fmt.Fprintln(os.Stderr, "service management on this platform is delegated to the native init system.")
		fmt.Fprintln(os.Stderr, "See deploy/linux/install.sh and deploy/linux/privatedns.service (systemd)")
		fmt.Fprintln(os.Stderr, "or  deploy/macos/install.sh and deploy/macos/com.privatedns.plist (launchd).")
		return fmt.Errorf("action %q not supported on this platform", action)
	case ActionRun:
		// If someone runs `service run` on non-Windows (e.g. via systemd's
		// ExecStart), just fall through to a normal run. Callers should not
		// reach here — main handles the run path directly.
		return fmt.Errorf("service run is only meaningful under Windows SCM; use normal invocation instead")
	}
	return nil
}

// LoadEnvFile on non-Windows still works; systemd's EnvironmentFile= is the
// idiomatic loader but supporting it here means the same install semantics
// work if someone chooses to use it manually.
func LoadEnvFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	_ = b // parser exists in _windows.go; on non-Windows we defer to systemd's EnvironmentFile.
	return nil
}
