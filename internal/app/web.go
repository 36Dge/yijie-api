package app

import (
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/36Dge/yijie-api/internal/modules/webretrieval/application"
	"github.com/36Dge/yijie-api/internal/modules/webretrieval/infrastructure"
	"github.com/36Dge/yijie-api/internal/modules/webretrieval/infrastructure/postgres"
	webhttp "github.com/36Dge/yijie-api/internal/modules/webretrieval/interfaces/http"
	"github.com/36Dge/yijie-api/internal/platform/webwire"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WebConfig struct {
	Enabled                                                           bool
	DailyTenantCalls                                                  int
	ExecutorOrigin, InternalAddress, CertificatePath, KeyPath, CAPath string
}

func loadWebConfig() (WebConfig, error) {
	enabled, err := boolEnv("YIJIE_API_BUILTIN_WEB_ENABLED", false)
	if err != nil {
		return WebConfig{}, err
	}
	if !enabled {
		return WebConfig{}, nil
	}
	c := WebConfig{Enabled: true, InternalAddress: os.Getenv("YIJIE_API_WEB_INTERNAL_ADDRESS"), CertificatePath: os.Getenv("YIJIE_API_WEB_CERT_FILE"), KeyPath: os.Getenv("YIJIE_API_WEB_KEY_FILE"), CAPath: os.Getenv("YIJIE_API_WEB_CA_FILE")}
	c.DailyTenantCalls, err = strconv.Atoi(os.Getenv("YIJIE_API_WEB_DAILY_TENANT_CALLS"))
	if err != nil || c.DailyTenantCalls < 1 || c.DailyTenantCalls > 100000 {
		return WebConfig{}, errors.New("builtin web requires a finite daily tenant call limit")
	}
	if _, _, err = net.SplitHostPort(c.InternalAddress); err != nil {
		return WebConfig{}, errors.New("builtin web internal listener address required")
	}
	c.ExecutorOrigin, err = webwire.ServiceOrigin(os.Getenv("YIJIE_API_WEB_EXECUTOR_ORIGIN"))
	if err != nil {
		return WebConfig{}, err
	}
	if c.CertificatePath == "" || c.KeyPath == "" || c.CAPath == "" {
		return WebConfig{}, errors.New("builtin web managed service identity required")
	}
	return c, nil
}
func NewBuiltinWeb(c WebConfig, pool *pgxpool.Pool, identity, revocationIdentity webhttp.ProductIdentity) (*webhttp.Handler, *http.Server, error) {
	if !c.Enabled || identity == nil {
		return nil, nil, errors.New("builtin web identity unavailable")
	}
	clientTLS, err := webwire.ManagedIdentity(c.CertificatePath, c.KeyPath, c.CAPath, webwire.ExecutorIdentity, false)
	if err != nil {
		return nil, nil, err
	}
	serverTLS, err := webwire.ManagedIdentity(c.CertificatePath, c.KeyPath, c.CAPath, webwire.ExecutorIdentity, true)
	if err != nil {
		return nil, nil, err
	}
	repository, err := postgres.New(pool, c.DailyTenantCalls)
	if err != nil {
		return nil, nil, err
	}
	executor := &infrastructure.Executor{Origin: c.ExecutorOrigin, Client: webwire.ManagedClient(clientTLS)}
	h := &webhttp.Handler{Service: application.New(repository, executor), Identity: identity, RevokeIdentity: revocationIdentity}
	server := &http.Server{Addr: c.InternalAddress, TLSConfig: serverTLS, Handler: h.InternalHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	return h, server, nil
}
