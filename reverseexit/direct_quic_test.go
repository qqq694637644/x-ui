package reverseexit

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"math/big"
	"testing"
	"time"
)

func validDirectConfig() DirectConfig {
	return DirectConfig{
		Hostname:   "rsx3.example.test",
		Port:       8080,
		CertSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Agent: Config{Protocol: "rsx3", Token: "shared-token-16characters",
			TargetSOCKSAddr: "127.0.0.1:40001", RetryInterval: time.Second},
	}
}

func TestDirectQUICIsIndependentOfWARPAndFRPC(t *testing.T) {
	cfg := validDirectConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("direct config rejected: %v", err)
	}
	cfg.Hostname = "240e:36f::1"
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted an IPv6 literal instead of DDNS")
	}
	cfg = validDirectConfig()
	cfg.Agent.TargetSOCKSAddr = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted implicit WARP/freedom fallback")
	}
	cfg.Agent.DirectTarget = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit native egress incorrectly requires WARP: %v", err)
	}
	cfg.Agent.TargetSOCKSAddr = "127.0.0.1:40001"
	if err := cfg.Validate(); err == nil {
		t.Fatal("native egress silently accepted conflicting SOCKS5 configuration")
	}
	cfg = validDirectConfig()
	cfg.Agent.DiagnosticsEnabled = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted unsupported direct diagnostics")
	}
}

func TestDirectCertificateFingerprintAndSAN(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	templ := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour),
		NotAfter: now.Add(time.Hour), DNSNames: []string{"rsx3.example.test"},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	raw, err := x509.CreateCertificate(rand.Reader, templ, templ, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	cfg := validDirectConfig()
	cfg.CertSHA256 = hex.EncodeToString(sum[:])
	if err := directTLS(cfg).VerifyPeerCertificate([][]byte{raw}, nil); err != nil {
		t.Fatalf("valid pinned certificate rejected: %v", err)
	}
	cfg.CertSHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if err := directTLS(cfg).VerifyPeerCertificate([][]byte{raw}, nil); err == nil {
		t.Fatal("accepted wrong certificate pin")
	}
	cfg.CertSHA256 = hex.EncodeToString(sum[:])
	cfg.Hostname = "another.example.test"
	if err := directTLS(cfg).VerifyPeerCertificate([][]byte{raw}, nil); err == nil {
		t.Fatal("accepted certificate for wrong DDNS hostname")
	}
}
