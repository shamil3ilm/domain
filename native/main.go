// privatedns — single-binary private DNS + management API.
//
// One process. SQLite for storage. Serves:
//   * DNS on :53 (authoritative for configured zones, forwarder for others)
//   * HTTP API + dashboard on :8080 (or HTTPS on :8443 with a self-signed cert)
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
	"syscall"
	"time"

	"github.com/privatedns/native/internal/bootstrap"
	"github.com/privatedns/native/internal/config"
	"github.com/privatedns/native/internal/dnssrv"
	"github.com/privatedns/native/internal/httpapi"
	"github.com/privatedns/native/internal/store"
)

const version = "0.1.0"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Println("privatedns", version)
			return
		case "help", "--help", "-h":
			usage()
			return
		}
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`privatedns ` + version + `
Usage: privatedns [command]

Commands:
  (default)   Run the server.
  version     Print version.
  help        This message.

Configuration via environment (all optional):
  PRIVATEDNS_DATA_DIR         Where to keep the SQLite DB + secrets (default: ./data)
  PRIVATEDNS_PRIVATE_TLD      Private namespace (default: myworld)
  PRIVATEDNS_DNS_ADDR         DNS listen address (default: :53)
  PRIVATEDNS_API_ADDR         HTTP API listen address (default: :8080)
  PRIVATEDNS_UPSTREAMS        Upstream resolvers (default: 1.1.1.1:53,9.9.9.9:53)
  PRIVATEDNS_ADMIN_EMAIL      Bootstrap admin email (default: admin@local)
  PRIVATEDNS_ADMIN_PASSWORD   Bootstrap admin password (default: randomly generated, printed once)
  PRIVATEDNS_ALLOW_FROM       CIDRs allowed to send DNS queries (default: all)
  PRIVATEDNS_LOG_LEVEL        debug|info|warn|error (default: info)
`)
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("mkdir data dir: %w", err)
	}

	// Configure structured logging.
	logLevel := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

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
	if err := bootstrap.Run(context.Background(), st, cfg); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start DNS server.
	dnsServer := dnssrv.New(st, cfg)
	go func() {
		if err := dnsServer.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("dns server", "err", err)
			cancel()
		}
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
	go func() {
		slog.Info("http.listen", "addr", cfg.APIAddr, "url", displayURL(cfg.APIAddr))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	slog.Info("shutdown")

	shutdownCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	_ = httpSrv.Shutdown(shutdownCtx)
	dnsServer.Shutdown()
	return nil
}

// displayURL renders a friendly URL for a listen address like ":8080" or
// "127.0.0.1:8080". Purely for the startup log.
func displayURL(addr string) string {
	host, port, err := splitHostPort(addr)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + host + ":" + port + "/"
}

func splitHostPort(addr string) (host, port string, err error) {
	// net.SplitHostPort exists but pulls in the net pkg here; do it inline.
	i := len(addr) - 1
	for i >= 0 && addr[i] != ':' {
		i--
	}
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
