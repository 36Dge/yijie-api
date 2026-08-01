package authn

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewLocalLabPinnedCAHTTPClient(t *testing.T) {
	path, digest, _ := writeLocalLabTestCA(t)

	client, err := NewLocalLabPinnedCAHTTPClient(path, digest)
	if err != nil {
		t.Fatalf("create pinned CA client: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.TLSClientConfig == nil {
		t.Fatalf("client must use an isolated proxy-free TLS transport: %#v", client.Transport)
	}
	if transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.RootCAs == nil {
		t.Fatal("client must use the pinned root pool without disabling TLS verification")
	}
	if client.Timeout != maximumJWKSRequestDuration {
		t.Fatalf("unexpected client timeout: %s", client.Timeout)
	}
	client.CloseIdleConnections()
}

func TestNewLocalLabPinnedCAHTTPClientRejectsUnsafeInputs(t *testing.T) {
	path, digest, contents := writeLocalLabTestCA(t)

	t.Run("pin mismatch", func(t *testing.T) {
		_, err := NewLocalLabPinnedCAHTTPClient(path, strings.Repeat("0", sha256.Size*2))
		if !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("expected invalid configuration, got %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "root.pem")
		if err := os.Symlink(path, link); err != nil {
			t.Fatalf("create CA symlink: %v", err)
		}
		_, err := NewLocalLabPinnedCAHTTPClient(link, digest)
		if !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("expected symlink rejection, got %v", err)
		}
	})

	t.Run("non owner-only permissions", func(t *testing.T) {
		unsafePath := filepath.Join(t.TempDir(), "root.pem")
		if err := os.WriteFile(unsafePath, contents, 0o644); err != nil {
			t.Fatalf("write unsafe CA: %v", err)
		}
		_, err := NewLocalLabPinnedCAHTTPClient(unsafePath, digest)
		if !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("expected permission rejection, got %v", err)
		}
	})

	t.Run("certificate bundle", func(t *testing.T) {
		bundle := append(append([]byte{}, contents...), contents...)
		bundlePath := filepath.Join(t.TempDir(), "bundle.pem")
		if err := os.WriteFile(bundlePath, bundle, 0o600); err != nil {
			t.Fatalf("write CA bundle: %v", err)
		}
		bundleDigest := sha256.Sum256(bundle)
		_, err := NewLocalLabPinnedCAHTTPClient(bundlePath, hex.EncodeToString(bundleDigest[:]))
		if !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("expected bundle rejection, got %v", err)
		}
	})
}

func writeLocalLabTestCA(t *testing.T) (string, string, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "FEAT-125 synthetic test CA"},
		NotBefore:             time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2027, time.August, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	contents := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	path := filepath.Join(t.TempDir(), "root.pem")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write CA certificate: %v", err)
	}
	digest := sha256.Sum256(contents)
	return path, hex.EncodeToString(digest[:]), contents
}
