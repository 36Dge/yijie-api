package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDatabase struct {
	err error
}

func (database fakeDatabase) Ping(context.Context) error {
	return database.err
}

func TestHealthzReportsDatabaseState(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	NewHandler(Config{Environment: "test", Port: "0"}, fakeDatabase{}, nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}
	if body := response.Body.String(); body == "" || !containsAll(body, `"status":"ok"`, `"database":"connected"`) {
		t.Fatalf("unexpected health response: %s", body)
	}
}

func TestReadyzFailsWhenDatabaseIsUnavailable(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()

	NewHandler(
		Config{Environment: "test", Port: "0"},
		fakeDatabase{err: errors.New("database unavailable")},
		nil,
	).ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", response.Code)
	}
	if body := response.Body.String(); !containsAll(body, `"status":"not_ready"`) {
		t.Fatalf("unexpected readiness response: %s", body)
	}
}

func containsAll(value string, expected ...string) bool {
	for _, item := range expected {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}
