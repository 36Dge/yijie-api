package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

const testIssuer = "https://identity.test.example/"

type staticKeySet struct {
	set jwk.Set
	err error
}

func (provider staticKeySet) KeySet(context.Context, string) (jwk.Set, error) {
	return provider.set, provider.err
}

func TestVerifierAcceptsOnlyBoundedRS256AccessJWT(t *testing.T) {
	now := time.Date(2026, time.August, 1, 2, 0, 0, 0, time.UTC)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	key := mustJWK(t, privateKey, "test-key", jwa.RS256())
	set := jwk.NewSet()
	publicKey, err := key.PublicKey()
	if err != nil {
		t.Fatalf("derive public JWK: %v", err)
	}
	if err := set.AddKey(publicKey); err != nil {
		t.Fatalf("add public JWK: %v", err)
	}
	verifier, err := NewVerifier(testIssuer, staticKeySet{set: set}, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}

	valid := mustSign(t, key, map[string]any{
		"iss": testIssuer,
		"sub": "synthetic-subject",
		"aud": []string{AccessTokenAudience},
		"iat": now.Add(-time.Minute),
		"nbf": now.Add(-time.Minute),
		"exp": now.Add(9 * time.Minute),
	})
	identity, err := verifier.Verify(context.Background(), valid)
	if err != nil {
		t.Fatalf("verify valid token: %v", err)
	}
	if identity.Issuer != testIssuer || identity.Subject != "synthetic-subject" {
		t.Fatalf("unexpected verified identity: %#v", identity)
	}

	tests := []struct {
		name   string
		claims map[string]any
	}{
		{name: "wrong audience", claims: claims(now, map[string]any{"aud": []string{"https://wrong.example"}})},
		{name: "multiple audiences", claims: claims(now, map[string]any{"aud": []string{AccessTokenAudience, "https://other.example"}})},
		{name: "wrong issuer", claims: claims(now, map[string]any{"iss": "https://wrong.example/"})},
		{name: "expired", claims: claims(now, map[string]any{"exp": now.Add(-2 * time.Minute)})},
		{name: "future not before", claims: claims(now, map[string]any{"nbf": now.Add(2 * time.Minute)})},
		{name: "future issued at", claims: claims(now, map[string]any{"iat": now.Add(2 * time.Minute), "exp": now.Add(9 * time.Minute)})},
		{name: "lifetime over ten minutes", claims: claims(now, map[string]any{"iat": now.Add(-time.Minute), "exp": now.Add(10*time.Minute + time.Second)})},
		{name: "empty subject", claims: claims(now, map[string]any{"sub": ""})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := mustSign(t, key, test.claims)
			if _, err := verifier.Verify(context.Background(), raw); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("expected ErrUnauthorized, got %v", err)
			}
		})
	}
}

func TestVerifierRejectsNonRS256BeforeKeyLookup(t *testing.T) {
	now := time.Date(2026, time.August, 1, 2, 0, 0, 0, time.UTC)
	key := mustJWK(t, []byte("test-only-hmac-key-with-enough-entropy"), "hmac-key", jwa.HS256())
	raw := mustSign(t, key, claims(now, nil))
	verifier, err := NewVerifier(testIssuer, staticKeySet{err: errors.New("must not be reached")}, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	if _, err := verifier.Verify(context.Background(), raw); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestVerifierDistinguishesUnavailableKeys(t *testing.T) {
	now := time.Date(2026, time.August, 1, 2, 0, 0, 0, time.UTC)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	key := mustJWK(t, privateKey, "unavailable-key", jwa.RS256())
	verifier, err := NewVerifier(testIssuer, staticKeySet{err: errors.New("jwks unavailable")}, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	if _, err := verifier.Verify(context.Background(), mustSign(t, key, claims(now, nil))); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("expected ErrKeyUnavailable, got %v", err)
	}
	verifier, err = NewVerifier(testIssuer, staticKeySet{}, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("create nil-key-set verifier: %v", err)
	}
	if _, err := verifier.Verify(context.Background(), mustSign(t, key, claims(now, nil))); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("expected nil key set to fail unavailable, got %v", err)
	}
}

func TestBearerTokenRequiresExactSchemeAndBoundedValue(t *testing.T) {
	if token, err := BearerToken("Bearer abc.def.ghi"); err != nil || token != "abc.def.ghi" {
		t.Fatalf("unexpected bearer parse: token=%q err=%v", token, err)
	}
	for _, header := range []string{"", "Basic abc", "Bearer", "Bearer a b", "bearer abc", "Bearer " + string(make([]byte, MaxEncodedTokenBytes+1))} {
		if _, err := BearerToken(header); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("header %q: expected ErrUnauthorized, got %v", header, err)
		}
	}
}

func claims(now time.Time, overrides map[string]any) map[string]any {
	values := map[string]any{
		"iss": testIssuer,
		"sub": "synthetic-subject",
		"aud": []string{AccessTokenAudience},
		"iat": now.Add(-time.Minute),
		"nbf": now.Add(-time.Minute),
		"exp": now.Add(9 * time.Minute),
	}
	for key, value := range overrides {
		values[key] = value
	}
	return values
}

func mustJWK(t *testing.T, raw any, keyID string, algorithm jwa.KeyAlgorithm) jwk.Key {
	t.Helper()
	key, err := jwk.Import(raw)
	if err != nil {
		t.Fatalf("import JWK: %v", err)
	}
	if err := key.Set(jwk.KeyIDKey, keyID); err != nil {
		t.Fatalf("set kid: %v", err)
	}
	if err := key.Set(jwk.AlgorithmKey, algorithm); err != nil {
		t.Fatalf("set alg: %v", err)
	}
	return key
}

func mustSign(t *testing.T, key jwk.Key, claims map[string]any) string {
	t.Helper()
	token := jwt.New()
	for name, value := range claims {
		if err := token.Set(name, value); err != nil {
			t.Fatalf("set claim %s: %v", name, err)
		}
	}
	algorithm, ok := key.Algorithm()
	if !ok {
		t.Fatal("JWK algorithm is missing")
	}
	signed, err := jwt.Sign(token, jwt.WithKey(algorithm, key))
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return string(signed)
}
