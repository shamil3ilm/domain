// privatedns — single-binary private DNS + management API.
//
// One process. SQLite for storage. Serves:
//   - DNS on :53 (authoritative for configured zones, forwarder for others)
//   - HTTP API + dashboard on :8080 (or HTTPS on :8443 with a self-signed cert)
//
// No Docker required.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/privatedns/native/internal/bootstrap"
	"github.com/privatedns/native/internal/config"
	"github.com/privatedns/native/internal/dnssrv"
	"github.com/privatedns/native/internal/httpapi"
	"github.com/privatedns/native/internal/metrics"
	"github.com/privatedns/native/internal/store"
	"github.com/privatedns/native/internal/svcmgr"
	"github.com/privatedns/native/internal/tlscerts"
)

const (
	version     = "0.3.0"
	serviceName = "privatedns"
	displayName = "privatedns"
	description = "Private DNS + management API"
)

func main() {
	// If the OS started us as a Windows service, dispatch straight to svc.Run
	// so lifecycle messages reach the SCM. This does NOT match the case where
	// the user typed `privatedns service run` — that goes through the CLI
	// dispatcher below.
	if isSvc, _ := svcmgr.IsWindowsService(); isSvc {
		_ = svcmgr.Handle(svcmgr.ActionRun, svcmgr.Options{ServiceName: serviceName}, run)
		return
	}

	// Command dispatch.
	args := os.Args[1:]
	if len(args) == 0 {
		mustRun(run)
		return
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Println("privatedns", version)
	case "help", "--help", "-h":
		usage()
	case "service":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: privatedns service <install|uninstall|start|stop|status|run> [--env-file <path>]")
			os.Exit(2)
		}
		handleService(args[1], args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
		os.Exit(2)
	}
}

func mustRun(fn func(context.Context) error) {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := fn(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func handleService(action string, rest []string) {
	// Optional --env-file: load KEY=value pairs before doing anything else.
	// This is how Windows Service persists its "install-time" configuration.
	envFile := ""
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--env-file" && i+1 < len(rest) {
			envFile = rest[i+1]
			i++
		}
	}
	if envFile != "" {
		if err := svcmgr.LoadEnvFile(envFile); err != nil {
			fmt.Fprintln(os.Stderr, "load env file:", err)
			os.Exit(1)
		}
	}

	opts := svcmgr.Options{
		ServiceName: serviceName,
		DisplayName: displayName,
		Description: description,
	}

	act := svcmgr.Action(action)
	switch act {
	case svcmgr.ActionInstall:
		// Capture every PRIVATEDNS_* env var currently set — that's what we
		// want to bake into the service so `install` under a shell that had
		// the desired env exported does the right thing.
		opts.Env = captureEnv("PRIVATEDNS_")
	case svcmgr.ActionRun:
		// Under `service run` invoked by the SCM (or manually for testing),
		// we want to actually run the server. Don't shell out.
		mustRun(run)
		return
	}

	if err := svcmgr.Handle(act, opts, run); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func captureEnv(prefix string) map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		if strings.HasPrefix(k, prefix) {
			out[k] = kv[i+1:]
		}
	}
	return out
}

func usage() {
	fmt.Print(`privatedns ` + version + `
Usage:
  privatedns                        Run the server in the foreground.
  privatedns version                Print version.
  privatedns help                   This message.
  privatedns service install        Install as a Windows Service (elevated).
  privatedns service uninstall      Remove the Windows Service.
  privatedns service start          Start the Windows Service.
  privatedns service stop           Stop the Windows Service.
  privatedns service status         Show current service state.

Configuration via environment (all optional):
  PRIVATEDNS_DATA_DIR                Where to keep the SQLite DB + secrets (default: ./data)
  PRIVATEDNS_PRIVATE_TLD             Private namespace (default: myworld)
  PRIVATEDNS_DNS_ADDR                DNS listen address (default: :53)
  PRIVATEDNS_API_ADDR                HTTP API listen address (default: :8080)
  PRIVATEDNS_UPSTREAMS               Upstream resolvers (default: 1.1.1.1:53,9.9.9.9:53)
  PRIVATEDNS_ADMIN_EMAIL             Bootstrap admin email (default: admin@local)
  PRIVATEDNS_ADMIN_PASSWORD          Bootstrap admin password (default: randomly generated, printed once)
  PRIVATEDNS_ALLOW_QUERY_FROM        CIDRs allowed to send DNS queries (default: all)
  PRIVATEDNS_ALLOW_RECURSION_FROM    CIDRs allowed to recurse (default: loopback only)
  PRIVATEDNS_DNS_RATE_LIMIT_PER_SEC  Per-source-IP QPS (default: 20; 0 disables)
  PRIVATEDNS_DNS_RATE_LIMIT_BURST    Per-source-IP burst (default: 40)
  PRIVATEDNS_DNS_RATE_LIMIT_EXEMPT_CIDR   CIDRs that bypass rate limit (default: loopback)
  PRIVATEDNS_LOGIN_MAX_ATTEMPTS      Failed logins before lockout per (email,IP) (default: 5; 0 disables)
  PRIVATEDNS_LOGIN_LOCKOUT_WINDOW    Sliding window for the lockout (default: 15m)
  PRIVATEDNS_API_TLS                 off|auto|cert (default: off)
  PRIVATEDNS_API_TLS_CERT            Cert path (mode=cert)
  PRIVATEDNS_API_TLS_KEY             Key path (mode=cert)
  PRIVATEDNS_API_TLS_HOSTS           Extra SANs for the auto-generated cert (comma-separated)
  PRIVATEDNS_AXFR_ALLOW_FROM         CIDRs allowed to AXFR zones (default: none — transfers disabled)
  PRIVATEDNS_LOG_LEVEL               debug|info|warn|error (default: info)
`)
}

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("mkdir data dir: %w", err)
	}

	// Configure structured logging. If stderr is unavailable (Windows service
	// with no console), fall back to a log file in DataDir/logs/.
	logHandler := newLogHandler(cfg)
	slog.SetDefault(slog.New(logHandler))

	// Publish build info so Grafana can group by version.
	metrics.SetBuildInfo(version)

	// Load or generate the JWT secret.
	jwtSecret, err := loadOrCreateSecret(filepath.Join(cfg.DataDir, "jwt.key"))
	if err != nil {
		return fmt.Errorf("jwt secret: %w", err)
	}
	cfg.JWTSecret = jwtSecret

	// Open the SQLite database.
	st, err := store.Open(filepath.Join(cfg.DataDir, "privatedns.db"))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// Bootstrap: create admin, ensure private root zone exists.
	if err := bootstrap.Run(ctx, st, cfg); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	// Periodic housekeeping: drop expired JWT revocation rows so the table
	// doesn't grow forever. Once a token's natural expiry has passed the
	// parser rejects it anyway; the revocation row is just noise. Runs
	// hourly, plus once at boot so a long-idle process cleans up on start.
	go func() {
		gc := func() {
			n, err := st.GCExpiredRevokedTokens(ctx)
			if err != nil {
				slog.Warn("revoked_tokens.gc", "err", err)
				return
			}
			if n > 0 {
				slog.Info("revoked_tokens.gc", "removed", n)
			}
		}
		gc() // first pass at boot
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				gc()
			}
		}
	}()

	// Start DNS server.
	dnsServer := dnssrv.New(st, cfg)
	dnsErr := make(chan error, 1)
	go func() {
		if err := dnsServer.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("dns server", "err", err)
			dnsErr <- err
			return
		}
		dnsErr <- nil
	}()

	// Start HTTP API + dashboard.
	apiHandler := httpapi.New(st, cfg)
	httpSrv := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           apiHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// TLS setup. In "off" mode the API is plain HTTP. In "auto" mode we
	// materialize a self-signed cert into DataDir/tls (fresh on first boot,
	// reused after). "cert" uses operator-supplied paths verbatim.
	tlsCert, tlsKey := "", ""
	switch cfg.APITLSMode {
	case "auto":
		pair, fpr, err := tlscerts.EnsureSelfSigned(cfg.DataDir, cfg.APITLSHosts)
		if err != nil {
			return fmt.Errorf("tls auto: %w", err)
		}
		tlsCert, tlsKey = pair.CertPath, pair.KeyPath
		slog.Info("tls.self_signed", "cert", pair.CertPath, "fingerprint_sha256", fpr)
	case "cert":
		tlsCert, tlsKey = cfg.APITLSCert, cfg.APITLSKey
	}

	httpErr := make(chan error, 1)
	go func() {
		scheme := "http"
		if tlsCert != "" {
			scheme = "https"
		}
		slog.Info("http.listen", "addr", cfg.APIAddr, "scheme", scheme,
			"url", schemeURL(scheme, cfg.APIAddr))
		var err error
		if tlsCert != "" {
			err = httpSrv.ListenAndServeTLS(tlsCert, tlsKey)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
			httpErr <- err
			return
		}
		httpErr <- nil
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown")
	case err := <-dnsErr:
		slog.Info("dns.exit", "err", err)
	case err := <-httpErr:
		slog.Info("http.exit", "err", err)
	}

	shutdownCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	_ = httpSrv.Shutdown(shutdownCtx)
	dnsServer.Shutdown()
	return nil
}

// newLogHandler picks a log destination. If we're running as a Windows
// Service, stderr is /dev/null equivalent — write to a file inside DataDir.
// Otherwise (foreground / systemd / launchd), write to stderr and let the
// init system capture it.
func newLogHandler(cfg *config.Config) slog.Handler {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}

	if isSvc, _ := svcmgr.IsWindowsService(); isSvc {
		logDir := filepath.Join(cfg.DataDir, "logs")
		_ = os.MkdirAll(logDir, 0o700)
		f, err := os.OpenFile(filepath.Join(logDir, "privatedns.log"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			return slog.NewJSONHandler(f, opts)
		}
	}
	return slog.NewTextHandler(os.Stderr, opts)
}

// displayURL renders a friendly URL for a listen address like ":8080" or
// "127.0.0.1:8080". Purely for the startup log.
func displayURL(addr string) string {
	return schemeURL("http", addr)
}

// schemeURL is displayURL with the scheme picked by the caller (used when
// TLS is enabled). Same "friendliest hostname wins" logic.
func schemeURL(scheme, addr string) string {
	host, port, err := splitHostPort(addr)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return scheme + "://" + host + ":" + port + "/"
}

func splitHostPort(addr string) (host, port string, err error) {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return "", "", fmt.Errorf("bad addr")
	}
	return addr[:i], addr[i+1:], nil
}

// loadOrCreateSecret reads a hex-encoded secret from disk, or generates one.
func loadOrCreateSecret(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 64 {
		return string(b), nil
	}
	buf := make([]byte, 64)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	s := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		return "", err
	}
	return s, nil
}
