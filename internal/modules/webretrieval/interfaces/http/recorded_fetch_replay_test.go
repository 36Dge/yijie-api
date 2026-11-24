package webhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/builtinweb"
	"github.com/36Dge/yijie-api/internal/modules/webretrieval/application"
	d "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	"github.com/36Dge/yijie-api/internal/modules/webretrieval/infrastructure/postgres"
	"github.com/google/uuid"
)

// The receiver counts actual HTTP requests to the recorded resource. The test
// executor uses the same durable claim and terminal settlement as a live send.
type recordedPageExecutor struct {
	repo   d.Repository
	origin string
}

func (e recordedPageExecutor) Execute(ctx context.Context, op d.Operation, q d.Request) ([]byte, error) {
	receipt, err := e.repo.Claim(ctx, op.ID, op.Digest, uuid.New())
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.origin, bytes.NewReader(q.Arguments))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Replay-Scope", q.SearchScope.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || resp.StatusCode != 200 || wire.Validate("FetchSuccess", body) != nil {
		return nil, fmt.Errorf("recorded page unavailable")
	}
	receipt.Operation.Facts.Transport = "responded"
	receipt.Operation.Facts.Outcome = "results"
	return body, e.repo.Finish(ctx, receipt, "completed", "completed")
}

func TestHangzhouRecordedPageOperations(t *testing.T) {
	directory := os.Getenv("HANGZHOU_REPLAY_DIRECTORY")
	if directory == "" {
		t.Skip("requires the November 23 recording and an isolated local PostgreSQL")
	}
	raw, err := os.ReadFile(filepath.Join(directory, "source.json"))
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Fetches []struct {
			Arguments struct{ URL string }
			Body      json.RawMessage
		}
	}
	if json.Unmarshal(raw, &source) != nil || len(source.Fetches) != 4 {
		t.Fatal("expected four recorded reads")
	}
	page := source.Fetches[0]
	var mu sync.Mutex
	reads := map[string]int{}
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var args struct{ URL string }
		if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&args) != nil || args.URL != page.Arguments.URL {
			http.Error(w, "page outside recording", 404)
			return
		}
		mu.Lock()
		reads[r.Header.Get("X-Replay-Scope")]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(page.Body)
	}))
	defer supplier.Close()
	pool, principal := isolatedWebPrincipal(t)
	repo, err := postgres.New(pool, 64)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := repo.Mint(context.Background(), principal, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Handler{Service: application.New(repo, recordedPageExecutor{repo, supplier.URL})}).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	scope := uuid.New()
	var owner string
	post := func(callerScope uuid.UUID, pageURL string) (id, relation string, err error) {
		call, _ := uuid.NewV7()
		data, _ := json.Marshal(map[string]any{"scope_id": callerScope, "deadline_unix_ms": time.Now().Add(45 * time.Second).UnixMilli(), "request": map[string]any{"operation_id": call, "tool": "kimi_web_fetch", "arguments": map[string]string{"url": pageURL}}})
		req, _ := http.NewRequest("POST", server.URL+"/v2/web-retrieval/fetches", bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+grant.Bearer)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", "", err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != 200 || wire.Validate("ScopedFetchResponse", raw) != nil {
			return "", "", fmt.Errorf("unexpected fetch response %d", resp.StatusCode)
		}
		var got struct {
			Receipt struct {
				OperationID            string `json:"operation_id"`
				Relation, Availability string
			}
			Result json.RawMessage
		}
		if err = json.Unmarshal(raw, &got); err != nil {
			return "", "", err
		}
		if got.Receipt.Availability == "available" {
			var want, have any
			if json.Unmarshal(page.Body, &want) != nil || json.Unmarshal(got.Result, &have) != nil || !reflect.DeepEqual(want, have) {
				return "", "", fmt.Errorf("page body differs from recording")
			}
		}
		return got.Receipt.OperationID, got.Receipt.Relation, nil
	}
	for i, entry := range source.Fetches {
		id, relation, err := post(scope, entry.Arguments.URL)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			owner = id
		}
		if owner == "" || owner != id || i > 0 && relation != "reused" {
			t.Fatal("original operation not reused")
		}
	}
	// Concurrent fresh callers and fragment-only changes share one physical page.
	second := uuid.New()
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 4)
	start := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func(n int) {
			<-start
			id, _, err := post(second, fmt.Sprintf("%s#day-%d", page.Arguments.URL, n))
			results <- result{id, err}
		}(i)
	}
	close(start)
	var other string
	for i := 0; i < 4; i++ {
		r := <-results
		if r.err != nil || r.id == "" || r.id == owner || (other != "" && r.id != other) {
			t.Fatalf("concurrent fetch: %v", r.err)
		}
		other = r.id
	}
	mu.Lock()
	fresh := reads[second.String()]
	mu.Unlock()
	if fresh != 1 {
		t.Fatalf("fresh scope concurrent receiver count %d", fresh)
	}

	mu.Lock()
	n := reads[scope.String()]
	mu.Unlock()
	if n != 1 {
		t.Fatalf("four calls reached supplier %d times", n)
	}
	t.Logf("four original requests, one receiver HTTP request, original operation %s", owner)
	if os.Getenv("HANGZHOU_REPLAY_SERVE") != "1" {
		return
	}
	// Separate opt-in owner for the unmodified local-demo gateway. It exits
	// normally when the driver creates the completion receipt.
	listener, err := net.Listen("tcp", "127.0.0.1:18890")
	if err != nil {
		t.Fatal(err)
	}
	api := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = api.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := api.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	ready, _ := json.Marshal(map[string]any{"binding": grant.Binding.ID, "grant": grant.ID, "bearer": grant.Bearer, "revision": grant.Binding.Scope.Revision, "credentials": grant.CredentialRevision})
	if err = os.WriteFile(filepath.Join(directory, "test-grant.json"), ready, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join(directory, "test-grant.json"))
	deadline := time.Now().Add(45 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(directory, "finish-api")); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for s, n := range reads {
		if n != 1 {
			t.Errorf("scope %s read the source %d times", s, n)
		}
	}
	summary, _ := json.MarshalIndent(reads, "", "  ")
	if err = os.WriteFile(filepath.Join(directory, "recorded-http-reads.json"), summary, 0600); err != nil {
		t.Fatal(err)
	}
}
