// Package tlscerts owns the on-disk lifecycle of a self-signed X.509
// certificate for the management HTTP API. First boot generates a keypair
// and persists it under DataDir/tls; subsequent boots reuse it. Operators
// can rotate by deleting the two files.
//
// The cert is deliberately long-lived (10 years) and self-signed. Trust is
// established out-of-band: the SHA-256 fingerprint is logged at startup so
// operators can pin it in curl (`--pinnedpubkey`), a browser trust store,
// or a client's TLS config.
//
// This package intentionally does not attempt ACME. Anyone who wants a
// public-CA-signed cert should point PRIVATEDNS_API_TLS_CERT and
// _KEY at their own materials.
package tlscerts

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FilePair is where the cert + key live on disk. Constant relative to the
// caller-supplied data dir so ops docs can point at exact paths.
type FilePair struct {
	CertPath string
	KeyPath  string
}

// PairIn returns the standard on-disk locations under dataDir.
func PairIn(dataDir string) FilePair {
	tls := filepath.Join(dataDir, "tls")
	return FilePair{
		CertPath: filepath.Join(tls, "cert.pem"),
		KeyPath:  filepath.Join(tls, "key.pem"),
	}
}

// EnsureSelfSigned generates a fresh self-signed cert+key under dataDir if
// neither exists. If both exist it returns them untouched. Returns the paths
// so the HTTP server can hand them to ListenAndServeTLS, plus the SHA-256
// fingerprint of the (existing or new) leaf cert for the startup log.
//
// hosts contains the SANs to encode. Hostnames go into DNS SANs; parseable
// addresses go into IP SANs. An empty list means "localhost" + loopback.
func EnsureSelfSigned(dataDir string, hosts []string) (FilePair, string, error) {
	fp := PairIn(dataDir)
	if err := os.MkdirAll(filepath.Dir(fp.CertPath), 0o700); err != nil {
		return fp, "", fmt.Errorf("mkdir tls dir: %w", err)
	}

	// Reuse an existing pair if both files are present. We do NOT try to
	// validate expiry — the cert is 10y long and operators rotate by
	// deleting the files. Reading the leaf gives us the fingerprint.
	if fileExists(fp.CertPath) && fileExists(fp.KeyPath) {
		fpr, err := fingerprintCert(fp.CertPath)
		if err != nil {
			return fp, "", fmt.Errorf("read existing cert: %w", err)
		}
		return fp, fpr, nil
	}

	// Generate.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fp, "", fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fp, "", fmt.Errorf("serial: %w", err)
	}

	if len(hosts) == 0 {
		hosts = []string{"localhost"}
	}

	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "privatedns"},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  false,
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
			continue
		}
		tpl.DNSNames = append(tpl.DNSNames, h)
	}
	// Always self-sign loopback so `curl -k https://127.0.0.1:port/` works
	// out of the box for the operator that just did the install.
	tpl.IPAddresses = append(tpl.IPAddresses,
		net.ParseIP("127.0.0.1"),
		net.ParseIP("::1"),
	)

	derCert, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		return fp, "", fmt.Errorf("sign cert: %w", err)
	}
	derKey, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fp, "", fmt.Errorf("marshal key: %w", err)
	}

	if err := writePEM(fp.CertPath, "CERTIFICATE", derCert, 0o644); err != nil {
		return fp, "", fmt.Errorf("write cert: %w", err)
	}
	if err := writePEM(fp.KeyPath, "PRIVATE KEY", derKey, 0o600); err != nil {
		return fp, "", fmt.Errorf("write key: %w", err)
	}

	sum := sha256.Sum256(derCert)
	return fp, hex.EncodeToString(sum[:]), nil
}

// Fingerprint returns the SHA-256 hex of the leaf certificate at path. Used
// by tests + reload paths that don't need to regenerate.
func Fingerprint(certPath string) (string, error) {
	return fingerprintCert(certPath)
}

// --- internal helpers ------------------------------------------------------

func fingerprintCert(path string) (string, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(buf)
	if block == nil {
		return "", fmt.Errorf("no PEM block in %s", path)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: typ, Bytes: der})
}
