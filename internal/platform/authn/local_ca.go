package authn

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maximumLocalCAPEMBytes int64 = 64 * 1024

// NewLocalLabPinnedCAHTTPClient returns an isolated HTTPS client whose only
// trust anchor is the single, pinned public CA certificate loaded from path.
// Profile and endpoint restrictions are enforced by app.LoadConfig before this
// local-lab-only helper is called.
func NewLocalLabPinnedCAHTTPClient(path, expectedSHA256 string) (*http.Client, error) {
	certificate, err := loadPinnedLocalCACertificate(path, expectedSHA256)
	if err != nil {
		return nil, err
	}

	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(certificate)
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, invalidLocalCA("default HTTP transport is unavailable")
	}
	transport := baseTransport.Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    rootCAs,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   maximumJWKSRequestDuration,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func loadPinnedLocalCACertificate(path, expectedSHA256 string) (*x509.Certificate, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return nil, invalidLocalCA("path must be absolute")
	}
	expectedDigest, err := parseLowerHexSHA256(expectedSHA256)
	if err != nil {
		return nil, invalidLocalCA("SHA-256 pin is invalid")
	}

	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, invalidLocalCA("path is not a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, invalidLocalCA("file cannot be opened")
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil || !fileInfo.Mode().IsRegular() || !os.SameFile(pathInfo, fileInfo) {
		return nil, invalidLocalCA("opened file does not match the validated path")
	}
	permissions := fileInfo.Mode().Perm()
	if permissions != 0o400 && permissions != 0o600 {
		return nil, invalidLocalCA("file permissions must be 0400 or 0600")
	}
	if fileInfo.Size() <= 0 || fileInfo.Size() > maximumLocalCAPEMBytes {
		return nil, invalidLocalCA("file size is invalid")
	}

	contents, err := io.ReadAll(io.LimitReader(file, maximumLocalCAPEMBytes+1))
	if err != nil || int64(len(contents)) != fileInfo.Size() || int64(len(contents)) > maximumLocalCAPEMBytes {
		return nil, invalidLocalCA("file contents changed or could not be read")
	}
	actualDigest := sha256.Sum256(contents)
	if subtle.ConstantTimeCompare(actualDigest[:], expectedDigest) != 1 {
		return nil, invalidLocalCA("SHA-256 pin does not match")
	}

	trimmed := bytes.TrimSpace(contents)
	if !bytes.HasPrefix(trimmed, []byte("-----BEGIN CERTIFICATE-----")) ||
		bytes.Count(trimmed, []byte("-----BEGIN CERTIFICATE-----")) != 1 ||
		bytes.Count(trimmed, []byte("-----END CERTIFICATE-----")) != 1 ||
		bytes.Contains(trimmed, []byte("PRIVATE KEY")) {
		return nil, invalidLocalCA("file must contain exactly one public certificate PEM")
	}
	block, remaining := pem.Decode(trimmed)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(remaining)) != 0 {
		return nil, invalidLocalCA("file must contain exactly one public certificate PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.BasicConstraintsValid || !certificate.IsCA || certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, invalidLocalCA("certificate is not a signing CA")
	}
	return certificate, nil
}

func parseLowerHexSHA256(value string) ([]byte, error) {
	if len(value) != sha256.Size*2 {
		return nil, ErrInvalidConfiguration
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return nil, ErrInvalidConfiguration
		}
	}
	digest, err := hex.DecodeString(value)
	if err != nil || len(digest) != sha256.Size {
		return nil, ErrInvalidConfiguration
	}
	return digest, nil
}

func invalidLocalCA(reason string) error {
	return fmt.Errorf("local CA %s: %w", reason, ErrInvalidConfiguration)
}
