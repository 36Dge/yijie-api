package authn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

const (
	minimumJWKSRefreshInterval = 5 * time.Minute
	maximumJWKSResponseBytes   = 1024 * 1024
	maximumJWKSRequestDuration = 5 * time.Second
)

type RemoteKeySet struct {
	cache *jwk.Cache
	url   string
}

func NewRemoteKeySet(ctx context.Context, jwksURL string, client *http.Client) (*RemoteKeySet, error) {
	if ctx == nil {
		return nil, ErrInvalidConfiguration
	}
	jwksURL = strings.TrimSpace(jwksURL)
	if err := validateJWKSURL(jwksURL); err != nil {
		return nil, err
	}
	cache, err := jwk.NewCache(ctx, httprc.NewClient())
	if err != nil {
		return nil, fmt.Errorf("create JWKS cache: %w", err)
	}
	options := []jwk.RegisterOption{
		jwk.WithFetchWhitelist(jwk.NewMapWhitelist().Add(jwksURL)),
		jwk.WithMaxFetchBodySize(maximumJWKSResponseBytes),
		jwk.WithMinInterval(minimumJWKSRefreshInterval),
		jwk.WithHTTPClient(restrictedHTTPClient(client)),
	}
	registerContext, cancelRegister := context.WithTimeout(ctx, maximumJWKSRequestDuration)
	defer cancelRegister()
	if err := cache.Register(registerContext, jwksURL, options...); err != nil {
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
		defer cancelShutdown()
		_ = cache.Shutdown(shutdownContext)
		return nil, fmt.Errorf("register JWKS endpoint: %w", err)
	}
	return &RemoteKeySet{cache: cache, url: jwksURL}, nil
}

func restrictedHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	restricted := *client
	if restricted.Timeout == 0 || restricted.Timeout > maximumJWKSRequestDuration {
		restricted.Timeout = maximumJWKSRequestDuration
	}
	restricted.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &restricted
}

func (provider *RemoteKeySet) KeySet(ctx context.Context, keyID string) (jwk.Set, error) {
	if provider == nil || provider.cache == nil || strings.TrimSpace(keyID) == "" {
		return nil, ErrKeyUnavailable
	}
	set, err := provider.cache.Lookup(ctx, provider.url)
	if err != nil || set == nil {
		return nil, fmt.Errorf("lookup cached JWKS: %w", ErrKeyUnavailable)
	}
	if _, ok := set.LookupKeyID(keyID); ok {
		return set, nil
	}
	set, err = provider.cache.Refresh(ctx, provider.url)
	if err != nil || set == nil {
		return nil, fmt.Errorf("refresh JWKS: %w", ErrKeyUnavailable)
	}
	return set, nil
}

func (provider *RemoteKeySet) Close(ctx context.Context) error {
	if provider == nil || provider.cache == nil {
		return nil
	}
	if err := provider.cache.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("shutdown JWKS cache: %w", err)
	}
	return nil
}

func validateJWKSURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return ErrInvalidConfiguration
	}
	return nil
}
