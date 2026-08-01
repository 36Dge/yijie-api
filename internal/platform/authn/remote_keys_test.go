package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

func TestRemoteKeySetFetchesAndRefreshesHTTPSJWKS(t *testing.T) {
	now := time.Date(2026, time.August, 1, 2, 0, 0, 0, time.UTC)
	keyOne := remoteTestKey(t, "key-one")
	keyTwo := remoteTestKey(t, "key-two")
	state := &jwksState{contents: publicSetJSON(t, keyOne)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		state.mu.RLock()
		defer state.mu.RUnlock()
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write(state.contents)
	}))
	defer server.Close()

	provider, err := NewRemoteKeySet(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatalf("create remote key set: %v", err)
	}
	defer func() {
		if err := provider.Close(context.Background()); err != nil {
			t.Errorf("close remote key set: %v", err)
		}
	}()
	verifier, err := NewVerifier(testIssuer, provider, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	if _, err := verifier.Verify(context.Background(), mustSign(t, keyOne, claims(now, nil))); err != nil {
		t.Fatalf("verify token with initially cached key: %v", err)
	}

	state.mu.Lock()
	state.contents = publicSetJSON(t, keyTwo)
	state.mu.Unlock()
	if _, err := verifier.Verify(context.Background(), mustSign(t, keyTwo, claims(now, nil))); err != nil {
		t.Fatalf("verify token after unknown-kid JWKS refresh: %v", err)
	}
}

func TestRemoteKeySetRejectsUntrustedURLForms(t *testing.T) {
	for _, raw := range []string{
		"http://identity.example/.well-known/jwks.json",
		"https://user@identity.example/.well-known/jwks.json",
		"https://identity.example/.well-known/jwks.json#fragment",
	} {
		if _, err := NewRemoteKeySet(context.Background(), raw, nil); err != ErrInvalidConfiguration {
			t.Fatalf("URL %q: expected invalid configuration, got %v", raw, err)
		}
	}
}

func TestRemoteKeySetRejectsRedirectedJWKS(t *testing.T) {
	destination := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"keys":[]}`))
	}))
	defer destination.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := NewRemoteKeySet(ctx, redirect.URL, redirect.Client()); err == nil {
		t.Fatal("expected redirected JWKS endpoint to be rejected")
	}
}

type jwksState struct {
	mu       sync.RWMutex
	contents []byte
}

func remoteTestKey(t *testing.T, keyID string) jwk.Key {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return mustJWK(t, privateKey, keyID, jwa.RS256())
}

func publicSetJSON(t *testing.T, privateKey jwk.Key) []byte {
	t.Helper()
	publicKey, err := privateKey.PublicKey()
	if err != nil {
		t.Fatalf("derive public JWK: %v", err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(publicKey); err != nil {
		t.Fatalf("add public JWK: %v", err)
	}
	contents, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal public JWKS: %v", err)
	}
	return contents
}
