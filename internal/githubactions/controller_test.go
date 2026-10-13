package githubactions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testController(t *testing.T, handler http.HandlerFunc) (*Controller, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	controller := newController("test-token", "owner/repo", "run-123", server.URL,
		&http.Client{Timeout: 5 * time.Second})
	return controller, server
}

func TestAvailableRequiresTokenRepoRun(t *testing.T) {
	full := newController("t", "o/r", "1", "http://x", nil)
	if !full.Available() {
		t.Fatal("esperaba Available()=true con token/repo/run")
	}
	for _, missing := range []*Controller{
		newController("", "o/r", "1", "http://x", nil),
		newController("t", "", "1", "http://x", nil),
		newController("t", "o/r", "", "http://x", nil),
		nil,
	} {
		if missing.Available() {
			t.Fatalf("esperaba Available()=false para %+v", missing)
		}
	}
}

func TestNewFromEnvReadsAndTrims(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "  secret  ")
	t.Setenv("GITHUB_REPOSITORY", "owner/repo")
	t.Setenv("GITHUB_RUN_ID", "42")
	controller := NewFromEnv()
	if !controller.Available() {
		t.Fatal("esperaba Available()=true")
	}
	if controller.token != "secret" {
		t.Fatalf("token = %q, want trimmed", controller.token)
	}
	if controller.RunID() != "42" {
		t.Fatalf("runID = %q", controller.RunID())
	}
}

func TestCancelCurrentSendsBearerAndAccepts202(t *testing.T) {
	var gotAuth, gotPath, gotMethod string
	controller, server := testController(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(http.StatusAccepted)
	})
	defer server.Close()

	if err := controller.CancelCurrent(context.Background()); err != nil {
		t.Fatalf("CancelCurrent devolvió error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/repos/owner/repo/actions/runs/run-123/cancel" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

func TestCancelCurrentRejects403(t *testing.T) {
	controller, server := testController(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	defer server.Close()

	err := controller.CancelCurrent(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("esperaba error HTTP 403, got %v", err)
	}
}

func TestCancelCurrentUnavailableWithoutRunID(t *testing.T) {
	controller := newController("t", "o/r", "", "http://x", http.DefaultClient)
	if err := controller.CancelCurrent(context.Background()); err == nil {
		t.Fatal("esperaba error por control no disponible")
	}
}

func TestDispatchPaperPostsManualSession(t *testing.T) {
	var body WorkflowDispatch
	controller, server := testController(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/actions/workflows/paper-session.yml/dispatches" {
			t.Errorf("path = %q", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body inválido: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	})
	defer server.Close()

	if err := controller.DispatchPaper(context.Background(), "", true); err != nil {
		t.Fatalf("DispatchPaper devolvió error: %v", err)
	}
	if body.Ref != "main" {
		t.Fatalf("ref = %q, want main", body.Ref)
	}
	if body.Inputs["restore_state"] != "true" {
		t.Fatalf("restore_state = %q", body.Inputs["restore_state"])
	}
}

func TestDispatchPaperRejects403(t *testing.T) {
	controller, server := testController(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	defer server.Close()

	if err := controller.DispatchPaper(context.Background(), "", true); err == nil {
		t.Fatal("esperaba error HTTP 403")
	}
}
