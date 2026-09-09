//go:build windows

package svcmgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// IsWindowsService returns true when the current process was started by the
// Service Control Manager. Used at boot to switch into service-run mode.
func IsWindowsService() (bool, error) { return svc.IsWindowsService() }

// Handle dispatches a `service <action>` subcommand.
//
// Run is the exception: it is called by main's own dispatch when the process
// is running under the SCM. It ties svc.Run to the caller-supplied runFn.
func Handle(action Action, opts Options, runFn func(context.Context) error) error {
	switch action {
	case ActionInstall:
		return install(opts)
	case ActionUninstall:
		return uninstall(opts.ServiceName)
	case ActionStart:
		return controlStart(opts.ServiceName)
	case ActionStop:
		return controlStop(opts.ServiceName)
	case ActionStatus:
		return status(opts.ServiceName)
	case ActionRun:
		return runService(opts.ServiceName, runFn)
	default:
		return fmt.Errorf("unknown service action %q", action)
	}
}

// install registers the current executable with the SCM, writes an env file
// beside the binary, and sets the service to auto-start.
func install(opts Options) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	exe, _ = filepath.Abs(exe)

	// Write env file so the service inherits current shell config on start.
	envDir := filepath.Join(os.Getenv("ProgramData"), "privatedns")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", envDir, err)
	}
	envFile := filepath.Join(envDir, "service.env")
	if err := writeEnvFile(envFile, opts.Env); err != nil {
		return fmt.Errorf("write env file: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM (run elevated?): %w", err)
	}
	defer m.Disconnect()

	if existing, err := m.OpenService(opts.ServiceName); err == nil {
		existing.Close()
		return fmt.Errorf("service %q already installed; run 'service uninstall' first", opts.ServiceName)
	}

	cfg := mgr.Config{
		ServiceType:  0x10, // SERVICE_WIN32_OWN_PROCESS
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		DisplayName:  opts.DisplayName,
		Description:  opts.Description,
	}
	s, err := m.CreateService(opts.ServiceName, exe, cfg, "service", "run", "--env-file", envFile)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	if err := eventlog.InstallAsEventCreate(opts.ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		// Non-fatal: EventLog registration can fail if the source exists.
		if !strings.Contains(err.Error(), "already exists") {
			fmt.Fprintln(os.Stderr, "warn: eventlog registration:", err)
		}
	}

	fmt.Printf("service %q installed.\n", opts.ServiceName)
	fmt.Printf("  binary:   %s\n", exe)
	fmt.Printf("  env file: %s\n", envFile)
	fmt.Printf("  start with:   privatedns service start\n")
	fmt.Printf("  or Services.msc\n")
	return nil
}

func uninstall(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM (run elevated?): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("open service %q: %w", name, err)
	}
	defer s.Close()

	// Try to stop first (ignore failure — may already be stopped).
	_, _ = s.Control(svc.Stop)

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	_ = eventlog.Remove(name)
	fmt.Printf("service %q uninstalled.\n", name)
	return nil
}

func controlStart(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	if err := s.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	fmt.Printf("service %q starting…\n", name)
	return nil
}

func controlStop(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	// Wait briefly for the service to transition to stopped.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			break
		}
		if st.State == svc.Stopped {
			fmt.Printf("service %q stopped.\n", name)
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	fmt.Printf("service %q stop requested (may still be running).\n", name)
	return nil
}

func status(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return err
	}
	fmt.Printf("service %q: state=%s\n", name, stateName(st.State))
	return nil
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start-pending"
	case svc.StopPending:
		return "stop-pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue-pending"
	case svc.PausePending:
		return "pause-pending"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("state(%d)", s)
	}
}

// runService bridges Windows SCM lifecycle to a normal Go run function.
type wrappedHandler struct {
	runFn func(context.Context) error
}

func (h *wrappedHandler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- h.runFn(ctx) }()

	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-errCh
				status <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-errCh:
			if err != nil {
				status <- svc.Status{State: svc.Stopped, ServiceSpecificExitCode: 1}
				return false, 1
			}
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func runService(name string, runFn func(context.Context) error) error {
	return svc.Run(name, &wrappedHandler{runFn: runFn})
}

// writeEnvFile serializes a map to KEY=value lines. Keys are sorted for a
// deterministic file — makes diffing config easier.
func writeEnvFile(path string, env map[string]string) error {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("# generated by 'privatedns service install' — edit and restart the service to change.\n")
	for _, k := range keys {
		v := env[k]
		// A leaked newline would corrupt the file. Reject.
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("%s contains newline; refusing to write", k)
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// LoadEnvFile reads a KEY=value file written by install and sets each
// variable in the current process. Missing file → no-op (running without
// the service manager).
func LoadEnvFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		// Only set if unset in the current environment — env at run time wins.
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
	return nil
}
