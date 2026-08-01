package authn

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

const (
	AccessTokenAudience    = "https://api.yijie.ai"
	MaxAccessTokenLifetime = 10 * time.Minute
	AllowedClockSkew       = 60 * time.Second
	MaxEncodedTokenBytes   = 16 * 1024
)

var (
	ErrUnauthorized         = errors.New("access token is unauthorized")
	ErrKeyUnavailable       = errors.New("access-token verification key is unavailable")
	ErrInvalidConfiguration = errors.New("access-token verifier configuration is invalid")
)

type Identity struct {
	Issuer  string
	Subject string
}

type KeySetProvider interface {
	KeySet(ctx context.Context, keyID string) (jwk.Set, error)
}

type Option func(*Verifier)

func WithClock(clock func() time.Time) Option {
	return func(verifier *Verifier) {
		if clock != nil {
			verifier.clock = clock
		}
	}
}

type Verifier struct {
	issuer string
	keys   KeySetProvider
	clock  func() time.Time
}

func NewVerifier(issuer string, keys KeySetProvider, options ...Option) (*Verifier, error) {
	issuer = strings.TrimSpace(issuer)
	if err := validateIssuer(issuer); err != nil || keys == nil {
		return nil, ErrInvalidConfiguration
	}
	verifier := &Verifier{issuer: issuer, keys: keys, clock: time.Now}
	for _, option := range options {
		if option != nil {
			option(verifier)
		}
	}
	return verifier, nil
}

func (verifier *Verifier) Verify(ctx context.Context, encoded string) (Identity, error) {
	if verifier == nil || verifier.keys == nil || len(encoded) == 0 || len(encoded) > MaxEncodedTokenBytes {
		return Identity{}, ErrUnauthorized
	}

	keyID, err := protectedKeyID([]byte(encoded))
	if err != nil {
		return Identity{}, ErrUnauthorized
	}
	set, err := verifier.keys.KeySet(ctx, keyID)
	if err != nil || set == nil {
		return Identity{}, fmt.Errorf("load verification key: %w", ErrKeyUnavailable)
	}
	key, ok := set.LookupKeyID(keyID)
	if !ok || key.KeyType() != jwa.RSA() {
		return Identity{}, ErrUnauthorized
	}
	algorithm, ok := key.Algorithm()
	if !ok || algorithm.String() != jwa.RS256().String() {
		return Identity{}, ErrUnauthorized
	}

	token, err := jwt.Parse(
		[]byte(encoded),
		jwt.WithKey(jwa.RS256(), key),
		jwt.WithIssuer(verifier.issuer),
		jwt.WithAudience(AccessTokenAudience),
		jwt.WithRequiredClaim(jwt.IssuerKey),
		jwt.WithRequiredClaim(jwt.SubjectKey),
		jwt.WithRequiredClaim(jwt.AudienceKey),
		jwt.WithRequiredClaim(jwt.IssuedAtKey),
		jwt.WithRequiredClaim(jwt.NotBeforeKey),
		jwt.WithRequiredClaim(jwt.ExpirationKey),
		jwt.WithAcceptableSkew(AllowedClockSkew),
		jwt.WithMaxDelta(MaxAccessTokenLifetime, jwt.ExpirationKey, jwt.IssuedAtKey),
		jwt.WithClock(jwt.ClockFunc(verifier.clock)),
	)
	if err != nil {
		return Identity{}, ErrUnauthorized
	}
	issuer, issuerOK := token.Issuer()
	subject, subjectOK := token.Subject()
	if !issuerOK || !subjectOK || issuer != verifier.issuer || strings.TrimSpace(subject) == "" {
		return Identity{}, ErrUnauthorized
	}
	return Identity{Issuer: issuer, Subject: subject}, nil
}

func BearerToken(header string) (string, error) {
	if len(header) > MaxEncodedTokenBytes+len("Bearer ") {
		return "", ErrUnauthorized
	}
	parts := strings.Split(header, " ")
	if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" || len(parts[1]) > MaxEncodedTokenBytes {
		return "", ErrUnauthorized
	}
	return parts[1], nil
}

func protectedKeyID(encoded []byte) (string, error) {
	message, err := jws.Parse(encoded)
	if err != nil {
		return "", err
	}
	signatures := message.Signatures()
	if len(signatures) != 1 {
		return "", errors.New("access JWT must have one signature")
	}
	headers := signatures[0].ProtectedHeaders()
	if headers == nil {
		return "", errors.New("access JWT protected headers are missing")
	}
	algorithm, ok := headers.Algorithm()
	if !ok || algorithm != jwa.RS256() {
		return "", errors.New("access JWT algorithm is not RS256")
	}
	keyID, ok := headers.KeyID()
	if !ok || strings.TrimSpace(keyID) == "" || len(keyID) > 200 {
		return "", errors.New("access JWT kid is invalid")
	}
	return keyID, nil
}

func validateIssuer(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ErrInvalidConfiguration
	}
	return nil
}
