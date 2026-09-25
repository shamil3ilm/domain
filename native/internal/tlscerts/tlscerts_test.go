package tlscerts

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"
	"time"
)

func TestEnsureSelfSigned_GeneratesThenReuses(t *testing.T) {
	dir := t.TempDir()

	fp1, fpr1, err := EnsureSelfSigned(dir, []string{"privatedns.local"})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if fpr1 == "" || len(fpr1) != 64 {
		t.Fatalf("fingerprint should be 64 hex chars, got %q", fpr1)
	}

	// Files exist and have restrictive perms on the key.
	if _, err := os.Stat(fp1.CertPath); err != nil {
		t.Fatalf("cert missing: %v", err)
	}
	info, err := os.Stat(fp1.KeyPath)
	if err != nil {
		t.Fatalf("key missing: %v", err)
	}
	if runtimeAllowsPermCheck() {
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("key perms %o allow group/other read", info.Mode().Perm())
		}
	}

	// Second call must reuse (same fingerprint, no regeneration).
	fp2, fpr2, err := EnsureSelfSigned(dir, []string{"different.host"})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if fp2 != fp1 {
		t.Errorf("paths changed on reuse: %+v vs %+v", fp2, fp1)
	}
	if fpr2 != fpr1 {
		t.Errorf("fingerprint changed on reuse: was %s, now %s", fpr1, fpr2)
	}
}

func TestEnsureSelfSigned_CertParsesAndHasSANs(t *testing.T) {
	dir := t.TempDir()
	fp, _, err := EnsureSelfSigned(dir, []string{"api.example.myworld", "10.10.0.5"})
	if err != nil {
		t.Fatal(err)
	}
	pemBytes, err := os.ReadFile(fp.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	// The hostname SAN we asked for is present.
	found := false
	for _, d := range cert.DNSNames {
		if d == "api.example.myworld" {
			found = true
		}
	}
	if !found {
		t.Errorf("DNS SAN missing: got %v", cert.DNSNames)
	}
	// IP SAN we asked for is present, plus loopback which we always add.
	haveIP := map[string]bool{}
	for _, ip := range cert.IPAddresses {
		haveIP[ip.String()] = true
	}
	if !haveIP["10.10.0.5"] {
		t.Errorf("IP SAN 10.10.0.5 missing: got %v", cert.IPAddresses)
	}
	if !haveIP["127.0.0.1"] {
		t.Errorf("loopback IP SAN missing: got %v", cert.IPAddresses)
	}

	// Not-after is far enough out that a rotation isn't imminent.
	if time.Until(cert.NotAfter) < 365*24*time.Hour {
		t.Errorf("cert expires too soon: %v", cert.NotAfter)
	}

	// Loads with the standard library's TLS keypair loader — that's what
	// http.Server.ListenAndServeTLS does under the covers.
	if _, err := tls.LoadX509KeyPair(fp.CertPath, fp.KeyPath); err != nil {
		t.Errorf("tls.LoadX509KeyPair: %v", err)
	}
}

// Windows perms don't map cleanly to POSIX modes. Skip the perm check there.
func runtimeAllowsPermCheck() bool {
	return os.PathSeparator == '/'
}
