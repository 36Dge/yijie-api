package webwire

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuiltinWebManagedServiceIdentityHandshake(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ordinary local FEAT-175 CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	certificate := func(name, identity string, serial int64) (string, string) {
		t.Helper()
		pub, private, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		uri, _ := url.Parse(identity)
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, URIs: []*url.URL{uri}}
		encoded, e := x509.CreateCertificate(rand.Reader, leaf, ca, pub, key)
		if e != nil {
			t.Fatal(e)
		}
		secret, e := x509.MarshalPKCS8PrivateKey(private)
		if e != nil {
			t.Fatal(e)
		}
		certPath, keyPath := filepath.Join(root, name+".pem"), filepath.Join(root, name+".key")
		if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded}), 0600); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: secret}), 0600); e != nil {
			t.Fatal(e)
		}
		return certPath, keyPath
	}
	apiCert, apiKey := certificate("api", APIIdentity, 2)
	executorCert, executorKey := certificate("executor", ExecutorIdentity, 3)
	serverTLS, err := ManagedIdentity(executorCert, executorKey, caPath, APIIdentity, true)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := ManagedIdentity(apiCert, apiKey, caPath, ExecutorIdentity, false)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || r.TLS.PeerCertificates[0].URIs[0].String() != APIIdentity {
			t.Error("managed caller identity missing")
		}
		w.WriteHeader(204)
	}))
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()
	client := ManagedClient(clientTLS)
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal("local mTLS handshake failed", err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal(response.StatusCode)
	}
}
