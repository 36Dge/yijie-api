package webhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/builtinweb"
	"github.com/36Dge/yijie-api/internal/modules/webretrieval/application"
	d "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	"github.com/36Dge/yijie-api/internal/modules/webretrieval/infrastructure/postgres"
	"github.com/36Dge/yijie-api/internal/platform/migrations"
	"github.com/36Dge/yijie-api/internal/platform/webwire"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ordinaryExecutor struct {
	repository  d.Repository
	calls       atomic.Int32
	unavailable bool
}

func (e *ordinaryExecutor) Execute(ctx context.Context, o d.Operation, q d.Request) ([]byte, error) {
	if e.unavailable {
		return nil, d.Unavailable
	}
	receipt, err := e.repository.Claim(ctx, o.ID, o.Digest, uuid.New())
	if err != nil {
		return nil, err
	}
	e.calls.Add(1)
	if err = e.repository.Finish(ctx, receipt, "completed", "completed"); err != nil {
		return nil, err
	}
	return []byte(`{"results":[{"title":"Public documentation","url":"https://example.com/docs","snippet":"Ordinary local response"}],"truncated":false}`), nil
}
func TestBuiltinWebProductLifecycle(t *testing.T) {
	dsn := os.Getenv("FEAT175_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("explicit isolated FEAT-175 PostgreSQL required")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(u.Path, "/yijie_feat175_test_") {
		t.Fatal("refusing non-test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = migrations.Run(ctx, dsn, "up"); err != nil {
		t.Fatal("isolated migration failed", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	scope := d.Scope{User: uuid.New(), Tenant: uuid.New(), Revision: 1}
	role := uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id) VALUES($1)`, []any{scope.User}},
		{`INSERT INTO tenants(id,name) VALUES($1,'FEAT-175 test')`, []any{scope.Tenant}},
		{`INSERT INTO tenant_memberships(tenant_id,user_id) VALUES($1,$2)`, []any{scope.Tenant, scope.User}},
		{`INSERT INTO roles(id,tenant_id,role_key,name) VALUES($1,$2,'web_test','Web test')`, []any{role, scope.Tenant}},
		{`INSERT INTO membership_roles(tenant_id,user_id,role_id) VALUES($1,$2,$3)`, []any{scope.Tenant, scope.User, role}},
		{`INSERT INTO role_permissions(tenant_id,role_id,permission_key) VALUES($1,$2,'web.retrieve')`, []any{scope.Tenant, role}},
	} {
		if _, err = pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal("test principal setup failed", err)
		}
	}
	repo, err := postgres.New(pool, 20)
	if err != nil {
		t.Fatal(err)
	}
	executor := &ordinaryExecutor{repository: repo}
	s := application.New(repo, executor)
	h := &Handler{Service: s, Identity: func(*http.Request) (d.Scope, error) { return scope, nil }}
	mux := http.NewServeMux()
	h.Register(mux)
	call := func(method, path string, body []byte, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	mintBody, _ := json.Marshal(wire.DelegationRequest{NativeBindingReference: uuid.New()})
	if err := wire.Validate("DelegationRequest", mintBody); err != nil {
		t.Fatal("generated contract compilation", err)
	}
	minted := call("POST", "/v1/web-retrieval/delegations", mintBody, "local-product-session")
	if minted.Code != 200 || wire.Validate("Delegation", minted.Body.Bytes()) != nil {
		t.Fatal("delegation contract failed", minted.Code)
	}
	var grant wire.Delegation
	_ = json.Unmarshal(minted.Body.Bytes(), &grant)
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(map[string]any{"operation_id": id.String(), "tool": "kimi_web_search", "arguments": map[string]any{"query": "public documentation"}})
	for i := 0; i < 2; i++ {
		w := call("POST", "/v1/web-retrieval/operations", request, grant.Bearer)
		if w.Code != 200 || wire.Validate("ExecutionResult", w.Body.Bytes()) != nil {
			t.Fatal("execution contract failed", w.Code)
		}
		var result wire.ExecutionResult
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		if result.Operation.State != "completed" || result.Operation.ResultAvailability != "available" || result.Result == nil {
			t.Fatal("expected completed retained result", result.Operation)
		}
	}
	if executor.calls.Load() != 1 {
		t.Fatal("replay dispatched again")
	}
	changed := bytes.Replace(request, []byte("public documentation"), []byte("another ordinary topic"), 1)
	conflict := call("POST", "/v1/web-retrieval/operations", changed, grant.Bearer)
	if conflict.Code != 409 || wire.Validate("APIError", conflict.Body.Bytes()) != nil {
		t.Fatal("operation conflict missing", conflict.Code)
	}
	b, err := repo.Resolve(ctx, grant.Bearer)
	if err != nil {
		t.Fatal(err)
	}
	restored := application.New(repo, executor)
	result, err := restored.Get(ctx, b, id)
	if err != nil || Status(result).ResultAvailability != "result_unavailable" {
		t.Fatal("restart must not reconstruct bodies")
	}
	limited, _ := postgres.New(pool, 1)
	newID, _ := uuid.NewV7()
	next := bytes.ReplaceAll(request, []byte(id.String()), []byte(newID.String()))
	parsed, _ := webwire.Request(next)
	if _, _, err = limited.Admit(ctx, b, parsed); err != d.Quota {
		t.Fatal("finite quota not enforced", err)
	}
	o, fresh, err := repo.Admit(ctx, b, parsed)
	if err != nil || !fresh {
		t.Fatal("new admission failed", err)
	}
	if _, err = repo.Cancel(ctx, b, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Claim(ctx, o.ID, o.Digest, uuid.New()); err != d.Conflict {
		t.Fatal("cancelled operation dispatched", err)
	}
	executor.unavailable = true
	failureID, _ := uuid.NewV7()
	failureBody := bytes.ReplaceAll(request, []byte(id.String()), []byte(failureID.String()))
	unavailable := call("POST", "/v1/web-retrieval/operations", failureBody, grant.Bearer)
	var failed wire.ExecutionResult
	if unavailable.Code != 200 || wire.Validate("ExecutionResult", unavailable.Body.Bytes()) != nil || json.Unmarshal(unavailable.Body.Bytes(), &failed) != nil || failed.Result == nil {
		t.Fatal("unavailable service result missing")
	}
	errorBytes, _ := failed.Result.MarshalJSON()
	var problem wire.AppErrorOutput
	_ = json.Unmarshal(errorBytes, &problem)
	if failed.Operation.Dispatch != "not_dispatched" || problem.Error.Code != "upstream_unavailable" {
		t.Fatal("service outage was confused with user cancellation")
	}
	revoked := call("POST", "/v1/web-retrieval/bindings/"+grant.BindingId.String()+"/revoke", nil, grant.Bearer)
	if revoked.Code != 200 {
		t.Fatal("binding revoke failed", revoked.Code)
	}
	if _, err = repo.Resolve(ctx, grant.Bearer); err != d.Expired {
		t.Fatal("revoked grant remained usable", err)
	}
	if executor.calls.Load() != 1 {
		t.Fatal("unexpected provider execution")
	}
	// Product identity can revoke its own binding even when the old delegation
	// is absent; this is independent of authorizing another paid operation.
	h.RevokeIdentity = func(*http.Request) (d.Scope, error) { return scope, nil }
	secondBody, _ := json.Marshal(wire.DelegationRequest{NativeBindingReference: uuid.New()})
	second := call("POST", "/v1/web-retrieval/delegations", secondBody, "local-product-session")
	var other wire.Delegation
	if second.Code != 200 || json.Unmarshal(second.Body.Bytes(), &other) != nil {
		t.Fatal("second binding unavailable")
	}
	productRevoke := call("POST", "/v1/web-retrieval/bindings/"+other.BindingId.String()+"/revoke", nil, "local-product-session")
	if productRevoke.Code != 200 {
		t.Fatal("product-session revocation unavailable", productRevoke.Code)
	}
	if _, err = repo.Resolve(ctx, other.Bearer); err != d.Expired {
		t.Fatal("product revocation retained the grant", err)
	}
}
