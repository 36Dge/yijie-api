package webhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/builtinweb"
	"github.com/36Dge/yijie-api/internal/modules/webretrieval/application"
	d "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	"github.com/36Dge/yijie-api/internal/modules/webretrieval/infrastructure/postgres"
	"github.com/google/uuid"
)

type scopedFixtureExecutor struct {
	repository d.Repository
	sends      atomic.Int32
	entered    chan struct{}
	release    chan struct{}
}

func (e *scopedFixtureExecutor) Execute(ctx context.Context, o d.Operation, q d.Request) ([]byte, error) {
	receipt, err := e.repository.Claim(ctx, o.ID, o.Digest, uuid.New())
	if err != nil {
		return nil, err
	}
	var args struct{ Query string }
	_ = json.Unmarshal(q.Arguments, &args)
	if e.sends.Add(1) == 1 && e.entered != nil {
		close(e.entered)
		select {
		case <-e.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	receipt.Operation.Facts = d.SearchFacts{Transport: "responded", Outcome: "empty"}
	body := []byte(`{"results":[],"dropped_results":0,"truncated":false}`)
	state, dispatch := "completed", "completed"
	if args.Query == "recorded invalid result" {
		receipt.Operation.Facts.Outcome = "failed"
		receipt.Operation.Facts.ErrorCode = "invalid_response"
		state, dispatch = "unknown", "may_have_dispatched"
		body, _ = json.Marshal(wire.AppErrorOutput{Error: wire.AppError{Code: "invalid_response", Message: wire.ErrorMessages["invalid_response"]}})
	}
	return body, e.repository.Finish(ctx, receipt, state, dispatch)
}
func TestScopedSearchDurableIdentityReadAndRecovery(t *testing.T) {
	pool, principal := isolatedWebPrincipal(t)
	ctx := context.Background()
	repo, err := postgres.New(pool, 20)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := repo.Mint(ctx, principal, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	e := &scopedFixtureExecutor{repository: repo, entered: make(chan struct{}), release: make(chan struct{})}
	endpoint := func() *http.ServeMux {
		mux := http.NewServeMux()
		(&Handler{Service: application.New(repo, e)}).Register(mux)
		return mux
	}
	h := endpoint()
	scope := uuid.New()
	request := func(mux *http.ServeMux, method, path string, body []byte) (int, wire.DeepSearchResponse) {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+grant.Bearer)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var reply wire.DeepSearchResponse
		if w.Code == 200 {
			if err := wire.Validate("DeepSearchResponse", w.Body.Bytes()); err != nil {
				t.Errorf("receipt validation %v", err)
			}
			_ = json.Unmarshal(w.Body.Bytes(), &reply)
		}
		return w.Code, reply
	}
	search := func(mux *http.ServeMux, s uuid.UUID, query string) wire.DeepSearchResponse {
		id, _ := uuid.NewV7()
		raw, _ := json.Marshal(map[string]any{"request": map[string]any{"operation_id": id.String(), "tool": "kimi_web_search_pro", "arguments": map[string]any{"query": query, "limit": 5}}, "scope_id": s, "deadline_unix_ms": time.Now().Add(40 * time.Second).UnixMilli()})
		code, reply := request(mux, "POST", "/v2/web-retrieval/searches", raw)
		if code != 200 || reply.CallId != id.String() {
			t.Errorf("search failed %d", code)
		}
		return reply
	}
	get := func(mux *http.ServeMux, s, id uuid.UUID) (int, wire.DeepSearchResponse) {
		return request(mux, "GET", "/v2/web-retrieval/searches/"+s.String()+"/"+id.String(), nil)
	}
	if code, _ := get(h, scope, uuid.New()); code != 403 || e.sends.Load() != 0 {
		t.Fatal("read created an operation")
	}
	first := make(chan wire.DeepSearchResponse, 1)
	go func() { first <- search(h, scope, "杭州天气") }()
	select {
	case <-e.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("dispatch did not enter")
	}
	pending := search(h, scope, "杭州天气")
	close(e.release)
	original := <-first
	if pending.Receipt.Relation != "joined" || original.Receipt.Relation != "first" || original.Receipt.Outcome != "empty" || *pending.Receipt.OperationId != *original.Receipt.OperationId {
		t.Fatal("in-flight identity lost", pending.Receipt, original.Receipt)
	}
	id := *original.Receipt.OperationId
	alternate, err := uuid.Parse(pending.CallId)
	if err != nil {
		t.Fatal(err)
	}
	if code, read := get(h, scope, alternate); code != 200 || *read.Receipt.OperationId != id || read.Result == nil {
		t.Fatal("lost join response could not resolve its own authorized handle")
	}
	for i := 0; i < 10; i++ {
		r := search(h, scope, "杭州天气")
		if r.Receipt.Relation != "reused" || *r.Receipt.OperationId != id || r.Result == nil {
			t.Fatal("completed result not reused")
		}
	}
	if e.sends.Load() != 1 {
		t.Fatal("supplier dispatch count", e.sends.Load())
	}
	if code, _ := get(h, uuid.New(), id); code != 403 {
		t.Fatal("other turn read material")
	}
	if _, r := get(endpoint(), scope, id); r.Receipt.Availability != "unavailable" || r.Receipt.Outcome != "empty" || r.Result != nil {
		t.Fatal("result availability conflated with outcome", r)
	}
	if _, r := get(h, scope, id); r.Result == nil || r.Receipt.Dispatch != "responded" {
		t.Fatal("authorized result read failed")
	}
	search(h, scope, "杭州气温")
	search(h, uuid.New(), "杭州天气")
	failed := search(h, scope, "recorded invalid result")
	again := search(endpoint(), scope, "recorded invalid result")
	if *failed.Receipt.OperationId != *again.Receipt.OperationId || again.Receipt.Outcome != "failed" || again.Receipt.Dispatch != "responded" || again.Receipt.ErrorCode == nil || *again.Receipt.ErrorCode != "invalid_response" || e.sends.Load() != 4 {
		t.Fatal("dispatched failure reopened", again.Receipt, e.sends.Load())
	}
	if err = repo.Revoke(ctx, grant.Binding); err != nil {
		t.Fatal(err)
	}
	if code, _ := get(h, scope, id); code != 401 {
		t.Fatal("revoked result remained readable", code)
	}
}

func TestScopedIssuerDisconnectPreservesIndependentReader(t *testing.T) {
	pool, subject := isolatedWebPrincipal(t)
	repo, err := postgres.New(pool, 100)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := repo.Mint(context.Background(), subject, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	fixture := &scopedFixtureExecutor{repository: repo, entered: make(chan struct{}), release: make(chan struct{})}
	service := application.New(repo, fixture)
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	id, _ := uuid.NewV7()
	req := d.Request{ID: id, Tool: "kimi_web_search_pro", Arguments: []byte(`{"query":"天气"}`), Digest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", SearchScope: uuid.New(), Deadline: time.Now().Add(time.Second * 40)}
	done := make(chan error, 1)
	go func(q d.Request) { _, err := service.Execute(owner, grant.Binding, q); done <- err }(req)
	<-fixture.entered
	req.ID, _ = uuid.NewV7()
	joined, err := application.New(repo, fixture).Execute(context.Background(), grant.Binding, req)
	if err != nil || !joined.Existing || joined.Operation.ID != id {
		t.Fatal("other gateway failed to join", err)
	}
	cancel()
	close(fixture.release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	read, err := service.ReadScoped(context.Background(), grant.Binding, req.SearchScope, req.ID)
	if err != nil || len(read.Body) == 0 || read.Operation.Facts.Outcome != "empty" || fixture.sends.Load() != 1 {
		t.Fatal("disconnected subscriber lost independent result", err)
	}
}

func TestScopedSearchOnlyRetriesProvenPreclaimFailure(t *testing.T) {
	pool, principal := isolatedWebPrincipal(t)
	repo, err := postgres.New(pool, 100)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	grant, err := repo.Mint(ctx, principal, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	req := d.Request{Tool: "kimi_web_search_pro", Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SearchScope: uuid.New(), Deadline: time.Now().Add(time.Second * 40)}
	admit := func() (d.Operation, bool) {
		req.ID, _ = uuid.NewV7()
		o, fresh, err := repo.Admit(ctx, grant.Binding, req)
		if err != nil {
			t.Fatal(err)
		}
		return o, fresh
	}
	first, _ := admit()
	ended, err := repo.EndBeforeClaim(ctx, grant.Binding, first.ID, d.Unavailable)
	if err != nil || ended.Dispatch != "not_dispatched" {
		t.Fatal("preclaim evidence missing", err)
	}
	retry, fresh := admit()
	if !fresh || retry.ID == first.ID {
		t.Fatal("retryable undelivered operation blocked")
	}
	if _, err = repo.Cancel(ctx, grant.Binding, retry.ID); err != nil {
		t.Fatal(err)
	}
	after, fresh := admit()
	if fresh || after.ID != retry.ID {
		t.Fatal("native cancellation reopened operation")
	}
	original, err := repo.ReadScoped(ctx, grant.Binding, req.SearchScope, first.ID)
	if err != nil || original.Facts.ErrorCode != d.Unavailable {
		t.Fatal("original retry audit lost")
	}
}

func TestScopedUncertainSettlementRemainsReadOnlyAfterRenewal(t *testing.T) {
	pool, principal := isolatedWebPrincipal(t)
	repo, err := postgres.New(pool, 100)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	binding := uuid.New()
	grant, err := repo.Mint(ctx, principal, binding)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := uuid.NewV7()
	q := d.Request{ID: id, Tool: "kimi_web_search_pro", Arguments: []byte(`{"query":"杭州天气"}`), Digest: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", SearchScope: uuid.New(), Deadline: time.Now().Add(40 * time.Second)}
	op, fresh, err := repo.Admit(ctx, grant.Binding, q)
	if err != nil || !fresh {
		t.Fatal("admission failed", err)
	}
	claim, err := repo.Claim(ctx, op.ID, op.Digest, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	renew, err := repo.Mint(ctx, principal, binding)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &scopedFixtureExecutor{repository: repo}
	service := application.New(repo, fixture)
	q.ID, _ = uuid.NewV7()
	pending, err := service.Execute(ctx, renew.Binding, q)
	if err != nil || !pending.Existing || pending.Operation.State != "dispatch_committed" || pending.Operation.ID != id {
		t.Fatal("renewal did not join the pending original", err)
	}
	claim.Operation.Facts = d.SearchFacts{Transport: "attempted", Outcome: "unknown"}
	if err = repo.Finish(ctx, claim, "unknown", "may_have_dispatched"); err != nil {
		t.Fatal(err)
	}
	q.ID, _ = uuid.NewV7()
	settled, err := application.New(repo, fixture).Execute(ctx, renew.Binding, q)
	if err != nil || !settled.Existing || settled.Operation.ID != id || settled.Operation.Facts.Outcome != "unknown" || len(settled.Body) > 0 || fixture.sends.Load() != 0 {
		t.Fatal("uncertain operation started a fresh dispatch", err)
	}
}
