package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/threave-io/threave/internal/agents"
	"github.com/threave-io/threave/internal/agents/fake"
	eventservice "github.com/threave-io/threave/internal/events"
	runcontrol "github.com/threave-io/threave/internal/session"
	"github.com/threave-io/threave/internal/store"
)

const testSessionID = "sess_test"

var testCreatedAt = time.Date(2026, 6, 12, 16, 0, 0, 123456789, time.FixedZone("EDT", -4*60*60))

type noopEventService struct{}

func (noopEventService) Append(context.Context, eventservice.AppendParams) (store.Event, error) {
	return store.Event{}, nil
}

func (noopEventService) Subscribe(string) (<-chan store.Event, func()) {
	ch := make(chan store.Event)
	return ch, func() { close(ch) }
}

func (noopEventService) SubscribeAll() (<-chan store.Event, func()) {
	ch := make(chan store.Event)
	return ch, func() { close(ch) }
}

type noopAgentRegistry struct{}

func (noopAgentRegistry) Get(string) (agents.Agent, bool) {
	return nil, false
}

type noopRunManager struct{}

func (noopRunManager) Register(context.Context, string) (context.Context, func(), error) {
	return context.Background(), func() {}, nil
}

func (noopRunManager) RegisterRun(context.Context, string, string) (context.Context, func(), error) {
	return context.Background(), func() {}, nil
}

func (noopRunManager) Cancel(string, runcontrol.Cancellation) error            { return nil }
func (noopRunManager) CancelRun(string, string, runcontrol.Cancellation) error { return nil }

func (noopRunManager) Cancellation(string) (runcontrol.Cancellation, bool) {
	return runcontrol.Cancellation{}, false
}

func (noopRunManager) Active(string) bool            { return false }
func (noopRunManager) ActiveRun(string, string) bool { return false }

func (noopRunManager) OpenUserInput(context.Context, agents.UserInputRequest) (agents.UserInputWaiter, error) {
	return nil, nil
}

func (noopRunManager) PendingUserInput(string, string) (agents.UserInputRequest, error) {
	return agents.UserInputRequest{}, store.ErrNotFound
}

func (noopRunManager) AnswerUserInputWithPersistence(context.Context, string, string, agents.UserInputResponse, func() error) error {
	return nil
}

func (noopRunManager) OpenPermission(context.Context, agents.PermissionRequest) (agents.PermissionWaiter, error) {
	return nil, nil
}

func (noopRunManager) PendingPermission(string, string) (agents.PermissionRequest, error) {
	return agents.PermissionRequest{}, nil
}

func (noopRunManager) ResolvePermission(string, string, agents.PermissionResponse) error { return nil }

func TestHealthRoute(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if got := strings.TrimSpace(rec.Body.String()); got != `{"status":"ok"}` {
		t.Fatalf("expected health response %q, got %q", `{"status":"ok"}`, got)
	}

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected content type application/json, got %q", got)
	}
}

func TestStaticAssetsServeIndexAndFrontendRoutes(t *testing.T) {
	handler := NewRouter(Dependencies{StaticAssets: testStaticAssets()})

	for _, route := range []string{"/", "/sessions/sess_123", "/sessions/sess_123/files/src/main.go"} {
		t.Run(route, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, route, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); !strings.Contains(got, `<div id="root"></div>`) {
				t.Fatalf("expected index.html body, got %q", got)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
				t.Fatalf("expected text/html content type, got %q", got)
			}
		})
	}
}

func TestStaticAssetsServeFilesWithContentTypes(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{StaticAssets: testStaticAssets()}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, "console.log") {
		t.Fatalf("expected javascript body, got %q", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Fatalf("expected javascript content type, got %q", got)
	}
}

func TestStaticAssetsServePrecompressedHashedAssets(t *testing.T) {
	assets := testStaticAssets()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(assets["assets/app.js"].Data); err != nil {
		t.Fatalf("compress test asset: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close test compressor: %v", err)
	}
	assets["assets/app.js.gz"] = &fstest.MapFile{Data: compressed.Bytes()}
	handler := NewRouter(Dependencies{StaticAssets: assets})

	t.Run("accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		req.Header.Set("Accept-Encoding", "br, gzip")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("expected gzip content encoding, got %q", got)
		}
		if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
			t.Fatalf("expected Accept-Encoding vary header, got %q", got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
			t.Fatalf("expected javascript content type, got %q", got)
		}
		reader, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("open compressed asset: %v", err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read compressed asset: %v", err)
		}
		if err := reader.Close(); err != nil {
			t.Fatalf("close compressed asset: %v", err)
		}
		if got := string(body); got != string(assets["assets/app.js"].Data) {
			t.Fatalf("expected original asset body, got %q", got)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		req.Header.Set("Accept-Encoding", "gzip;q=0")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("expected identity content, got encoding %q", got)
		}
		if got := rec.Body.String(); got != string(assets["assets/app.js"].Data) {
			t.Fatalf("expected original asset body, got %q", got)
		}
	})

	t.Run("range uses identity representation", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Range", "bytes=0-6")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusPartialContent {
			t.Fatalf("expected status %d, got %d", http.StatusPartialContent, rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("expected identity ranged content, got encoding %q", got)
		}
		if got := rec.Body.String(); got != `console` {
			t.Fatalf("expected ranged asset body %q, got %q", `console`, got)
		}
	})
}

func TestStaticAssetsFallBackWhenPrecompressedAssetIsUnavailable(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{StaticAssets: testStaticAssets()}).ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("expected identity content, got encoding %q", got)
	}
	if got := rec.Body.String(); got != `console.log("gorchestra")` {
		t.Fatalf("expected original asset body, got %q", got)
	}
}

func TestStaticAssetsDoNotFallbackForMissingAssetFiles(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{StaticAssets: testStaticAssets()}).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

func TestStaticAssetsSetCacheHeaders(t *testing.T) {
	handler := NewRouter(Dependencies{StaticAssets: testStaticAssets()})

	tests := []struct {
		name      string
		path      string
		wantCache string
	}{
		{name: "route fallback", path: "/sessions/sess_123", wantCache: revalidatingCache},
		{name: "index", path: "/", wantCache: revalidatingCache},
		{name: "service worker", path: "/service-worker.js", wantCache: revalidatingCache},
		{name: "hashed asset", path: "/assets/app.js", wantCache: immutableAssetCache},
		{name: "manifest", path: "/manifest.webmanifest", wantCache: staticShellCache},
		{name: "icon", path: "/icon.svg", wantCache: staticShellCache},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != test.wantCache {
				t.Fatalf("expected Cache-Control %q, got %q", test.wantCache, got)
			}
		})
	}
}

func TestAPIRoutePrecedenceAndMissingAPIRoute(t *testing.T) {
	handler := NewRouter(Dependencies{StaticAssets: testStaticAssets()})

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("expected health status %d, got %d", http.StatusOK, health.Code)
	}
	if got := strings.TrimSpace(health.Body.String()); got != `{"status":"ok"}` {
		t.Fatalf("expected API health response, got %q", got)
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected missing API status %d, got %d", http.StatusNotFound, missing.Code)
	}
	assertErrorResponse(t, missing, "not found")
}

func TestListSessionsReturnsMostRecentlyUpdatedFirst(t *testing.T) {
	fakeStore := newFakeHTTPStore()
	oldUpdatedAt := testCreatedAt.Add(-10 * time.Minute)
	newUpdatedAt := testCreatedAt.Add(5 * time.Minute)
	fakeStore.addSessionWith(store.Session{
		ID:        "sess_old",
		Title:     "Old session",
		AgentType: "fake",
		Status:    store.SessionStatusIdle,
		CreatedAt: oldUpdatedAt,
		UpdatedAt: oldUpdatedAt,
	})
	fakeStore.addSessionWith(store.Session{
		ID:        "sess_new",
		Title:     "New session",
		AgentType: "codex",
		Status:    store.SessionStatusRunning,
		CreatedAt: testCreatedAt,
		UpdatedAt: newUpdatedAt,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions?limit=10", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: fakeStore}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response listSessionsResponse
	decodeJSON(t, rec, &response)
	if len(response.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %#v", response.Sessions)
	}
	if response.Sessions[0].ID != "sess_new" || response.Sessions[1].ID != "sess_old" {
		t.Fatalf("expected sessions sorted newest first, got %#v", response.Sessions)
	}
	if response.Sessions[0].UpdatedAt != newUpdatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("expected UTC updated_at, got %q", response.Sessions[0].UpdatedAt)
	}
}

func testStaticAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {
			Data: []byte(`<!doctype html><html><body><div id="root"></div><script type="module" src="/assets/app.js"></script></body></html>`),
		},
		"assets/app.js": {
			Data: []byte(`console.log("gorchestra")`),
		},
		"icon.svg": {
			Data: []byte(`<svg></svg>`),
		},
		"manifest.webmanifest": {
			Data: []byte(`{"name":"Gorchestra"}`),
		},
		"service-worker.js": {
			Data: []byte(`self.addEventListener("push", () => {})`),
		},
	}
}

func TestListSessionsAppliesDefaultAndCapsLimit(t *testing.T) {
	for _, test := range []struct {
		name      string
		query     string
		wantLimit int
	}{
		{name: "default", query: "", wantLimit: defaultSessionLimit},
		{name: "cap", query: "?limit=5000", wantLimit: maxSessionLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeHTTPStore()
			store.addSession(testSessionID)
			req := httptest.NewRequest(http.MethodGet, "/api/sessions"+test.query, nil)
			rec := httptest.NewRecorder()

			NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
			}
			if got := store.lastListSessionsLimit(t); got != test.wantLimit {
				t.Fatalf("expected list sessions limit %d, got %d", test.wantLimit, got)
			}
		})
	}
}

func TestListSessionsRejectsInvalidLimit(t *testing.T) {
	for _, limit := range []string{"-1", "nope"} {
		t.Run(limit, func(t *testing.T) {
			store := newFakeHTTPStore()
			req := httptest.NewRequest(http.MethodGet, "/api/sessions?limit="+limit, nil)
			rec := httptest.NewRecorder()

			NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
			assertErrorResponse(t, rec, "limit must be a non-negative integer")
			if got := store.listSessionsCallCount(); got != 0 {
				t.Fatalf("expected no list sessions calls, got %d", got)
			}
		})
	}
}

func TestListSessionsFiltersByStatus(t *testing.T) {
	fakeStore := newFakeHTTPStore()
	fakeStore.addSessionWith(store.Session{
		ID:        "sess_running",
		Title:     "Running session",
		AgentType: "fake",
		Status:    store.SessionStatusRunning,
		CreatedAt: testCreatedAt,
		UpdatedAt: testCreatedAt,
	})
	fakeStore.addSessionWith(store.Session{
		ID:        "sess_failed",
		Title:     "Failed session",
		AgentType: "fake",
		Status:    store.SessionStatusFailed,
		CreatedAt: testCreatedAt,
		UpdatedAt: testCreatedAt,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions?status=running", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: fakeStore}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := fakeStore.lastListSessionsStatus(t); got != store.SessionStatusRunning {
		t.Fatalf("expected status filter running, got %q", got)
	}

	var response listSessionsResponse
	decodeJSON(t, rec, &response)
	if len(response.Sessions) != 1 || response.Sessions[0].ID != "sess_running" {
		t.Fatalf("expected only running session, got %#v", response.Sessions)
	}
}

func TestListSessionsIncludesGlobalEventCursor(t *testing.T) {
	baseStore := newFakeHTTPStore()
	baseStore.addSession(testSessionID)
	fakeStore := &fakeGlobalHTTPStore{fakeHTTPStore: baseStore, cursor: 37}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: fakeStore}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response listSessionsResponse
	decodeJSON(t, rec, &response)
	if response.EventCursor != 37 {
		t.Fatalf("expected event cursor 37, got %d", response.EventCursor)
	}
	if !reflect.DeepEqual(fakeStore.calls, []string{"cursor", "sessions"}) {
		t.Fatalf("expected cursor capture before session snapshot, got calls %#v", fakeStore.calls)
	}
}

func TestListSessionsCanIncludeArchived(t *testing.T) {
	fakeStore := newFakeHTTPStore()
	archivedAt := testCreatedAt.Add(10 * time.Minute)
	fakeStore.addSessionWith(store.Session{
		ID:         "sess_archived",
		Title:      "Archived session",
		AgentType:  "fake",
		Status:     store.SessionStatusIdle,
		CreatedAt:  testCreatedAt,
		UpdatedAt:  archivedAt,
		ArchivedAt: &archivedAt,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions?include_archived=true", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: fakeStore}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !fakeStore.lastListSessionsIncludeArchived(t) {
		t.Fatal("expected include_archived filter to be forwarded")
	}

	var response listSessionsResponse
	decodeJSON(t, rec, &response)
	if len(response.Sessions) != 1 || response.Sessions[0].ID != "sess_archived" {
		t.Fatalf("expected archived session in response, got %#v", response.Sessions)
	}
}

func TestListSessionsRejectsInvalidStatus(t *testing.T) {
	for _, status := range []string{"paused", "completed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			fakeStore := newFakeHTTPStore()
			req := httptest.NewRequest(http.MethodGet, "/api/sessions?status="+status, nil)
			rec := httptest.NewRecorder()

			NewRouter(Dependencies{Store: fakeStore}).ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
			assertErrorResponse(t, rec, "status is unsupported")
			if got := fakeStore.listSessionsCallCount(); got != 0 {
				t.Fatalf("expected no list sessions calls, got %d", got)
			}
		})
	}
}

func TestListSessionsRejectsInvalidIncludeArchived(t *testing.T) {
	fakeStore := newFakeHTTPStore()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions?include_archived=maybe", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: fakeStore}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "include_archived must be a boolean")
	if got := fakeStore.listSessionsCallCount(); got != 0 {
		t.Fatalf("expected no list sessions calls, got %d", got)
	}
}

func TestRestoreSessionRouteExists(t *testing.T) {
	fakeStore := newFakeHTTPStore()
	fakeStore.addSessionWith(store.Session{
		ID:        testSessionID,
		Title:     "Archived session",
		AgentType: "fake",
		Status:    store.SessionStatusIdle,
		CreatedAt: testCreatedAt,
		UpdatedAt: testCreatedAt,
		ArchivedAt: func() *time.Time {
			value := testCreatedAt
			return &value
		}(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+testSessionID+"/restore", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: fakeStore, Events: noopEventService{}, Agents: noopAgentRegistry{}, Runs: noopRunManager{}}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestGetSessionReturnsSession(t *testing.T) {
	fakeStore := newFakeHTTPStore()
	fakeStore.addSessionWith(store.Session{
		ID:         testSessionID,
		Title:      "Inspect repository",
		AgentType:  "codex",
		Status:     store.SessionStatusIdle,
		TokenCount: 12345,
		CreatedAt:  testCreatedAt,
		UpdatedAt:  testCreatedAt,
	})
	fakeStore.setEvents(
		testSessionID,
		testEvent(1, "agent.message.delta"),
		testEvent(2, "tool.call.started"),
		testEvent(3, "tool.call.completed"),
		testEvent(4, "file.change.started"),
		testEvent(5, "file.change.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID, nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: fakeStore}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response sessionResponse
	decodeJSON(t, rec, &response)
	if response.ID != testSessionID || response.AgentType != "codex" || response.Status != string(store.SessionStatusIdle) {
		t.Fatalf("unexpected session response: %#v", response)
	}
	if response.CompletedAt != nil {
		t.Fatalf("expected no completed_at for idle session, got %#v", response.CompletedAt)
	}
	if response.EventCount != 5 {
		t.Fatalf("expected event_count 5, got %d", response.EventCount)
	}
	if response.ToolCount != 2 {
		t.Fatalf("expected tool_count 2, got %d", response.ToolCount)
	}
	if response.TokenCount != 12345 {
		t.Fatalf("expected token_count 12345, got %d", response.TokenCount)
	}
}

func TestGetSessionReturns404ForUnknownSession(t *testing.T) {
	store := newFakeHTTPStore()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/missing", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "session not found")
}

func TestClearSessionNotificationAttentionReturnsUpdatedSession(t *testing.T) {
	fakeStore := newFakeHTTPStore()
	fakeStore.addSessionWith(store.Session{
		ID:                       testSessionID,
		Title:                    "Inspect repository",
		AgentType:                "codex",
		Status:                   store.SessionStatusIdle,
		NotificationAttentionSeq: 7,
		CreatedAt:                testCreatedAt,
		UpdatedAt:                testCreatedAt,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+testSessionID+"/notification-attention/clear", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: fakeStore}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response sessionResponse
	decodeJSON(t, rec, &response)
	if response.NotificationAttentionSeq != 0 {
		t.Fatalf("expected notification attention cleared, got %d", response.NotificationAttentionSeq)
	}

	session, err := fakeStore.GetSession(context.Background(), testSessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.NotificationAttentionSeq != 0 {
		t.Fatalf("expected store notification attention cleared, got %d", session.NotificationAttentionSeq)
	}
}

func TestClearAllSessionNotificationAttention(t *testing.T) {
	fakeStore := newFakeHTTPStore()
	fakeStore.addSessionWith(store.Session{
		ID:                       "sess_first",
		Title:                    "First",
		AgentType:                "codex",
		Status:                   store.SessionStatusIdle,
		NotificationAttentionSeq: 4,
		CreatedAt:                testCreatedAt,
		UpdatedAt:                testCreatedAt,
	})
	fakeStore.addSessionWith(store.Session{
		ID:                       "sess_second",
		Title:                    "Second",
		AgentType:                "codex",
		Status:                   store.SessionStatusIdle,
		NotificationAttentionSeq: 7,
		CreatedAt:                testCreatedAt,
		UpdatedAt:                testCreatedAt,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/notification-attention/clear", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: fakeStore}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	for _, sessionID := range []string{"sess_first", "sess_second"} {
		session, err := fakeStore.GetSession(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("get session: %v", err)
		}
		if session.NotificationAttentionSeq != 0 {
			t.Fatalf("expected notification attention cleared for %s, got %d", sessionID, session.NotificationAttentionSeq)
		}
	}
}

func TestEventHistoryReturnsEventsAfterSeq(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "agent.message.delta"),
		testEvent(2, "agent.tool.started"),
		testEvent(3, "agent.tool.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?after_seq=1&limit=10", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response eventHistoryResponse
	decodeJSON(t, rec, &response)

	if got, want := len(response.Events), 2; got != want {
		t.Fatalf("expected %d events, got %d: %#v", want, got, response.Events)
	}
	if response.Events[0].Seq != 2 || response.Events[1].Seq != 3 {
		t.Fatalf("expected seqs [2 3], got [%d %d]", response.Events[0].Seq, response.Events[1].Seq)
	}
	if got := response.Events[0].CreatedAt; got != testCreatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("expected UTC created_at %q, got %q", testCreatedAt.UTC().Format(time.RFC3339Nano), got)
	}

	var payload map[string]string
	if err := json.Unmarshal(response.Events[0].Payload, &payload); err != nil {
		t.Fatalf("expected payload to be JSON object: %v", err)
	}
	if got := payload["text"]; got != "event 2" {
		t.Fatalf("expected payload text %q, got %q", "event 2", got)
	}
}

func TestEventHistoryFiltersDebugEventsByDefault(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "user.message.completed"),
		testEvent(2, "agent.log.delta"),
		testEventWithPayload(3, "provider.codex.event", map[string]any{"provider_event_type": "turn/completed"}),
		testEventWithPayload(4, "provider.codex.event", map[string]any{"provider_event_type": "thread/tokenUsage/updated"}),
		testEvent(5, "agent.message.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?after_seq=0&limit=10", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response eventHistoryResponse
	decodeJSON(t, rec, &response)

	if got := eventSeqs(response.Events); !reflect.DeepEqual(got, []int64{1, 4, 5}) {
		t.Fatalf("expected non-debug seqs [1 4 5], got %v", got)
	}
}

func TestEventHistoryIncludesDebugEventsWhenRequested(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "user.message.completed"),
		testEvent(2, "agent.log.delta"),
		testEventWithPayload(3, "provider.codex.event", map[string]any{"provider_event_type": "turn/completed"}),
		testEvent(4, "agent.message.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?after_seq=0&limit=10&include_debug=true", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response eventHistoryResponse
	decodeJSON(t, rec, &response)

	if got := eventSeqs(response.Events); !reflect.DeepEqual(got, []int64{1, 2, 3, 4}) {
		t.Fatalf("expected all seqs [1 2 3 4], got %v", got)
	}
}

func TestEventHistoryRejectsInvalidDebugFilter(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?include_debug=maybe", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "include_debug must be a boolean")
}

func TestEventHistoryTruncatesLargePayloadStrings(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEventWithPayload(1, "tool.call.completed", map[string]any{
			"item_id":           "tool_1",
			"aggregated_output": strings.Repeat("x", maxEventPayloadStringLen+1024),
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?after_seq=0&limit=10", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response eventHistoryResponse
	decodeJSON(t, rec, &response)
	if got, want := len(response.Events), 1; got != want {
		t.Fatalf("expected %d event, got %d", want, got)
	}
	if got := len(response.Events[0].Payload); got > maxToolEventPayloadBytes {
		t.Fatalf("expected tool payload below %d bytes, got %d", maxToolEventPayloadBytes, got)
	}

	var payload map[string]any
	if err := json.Unmarshal(response.Events[0].Payload, &payload); err != nil {
		t.Fatalf("expected payload to be JSON object: %v", err)
	}
	output, ok := payload["aggregated_output"].(string)
	if !ok {
		t.Fatalf("expected aggregated_output string, got %#v", payload["aggregated_output"])
	}
	if len(output) > maxEventPayloadStringLen {
		t.Fatalf("expected output to be capped at %d bytes, got %d", maxEventPayloadStringLen, len(output))
	}
	if !strings.Contains(output, "gorchestra truncated") {
		t.Fatal("expected truncation marker in output")
	}
	if strings.Contains(output, "truncated -") {
		t.Fatalf("expected a valid truncation marker, got %q", output)
	}
	if payload["_gorchestra_truncated"] != true {
		t.Fatalf("expected truncation marker flag, got %#v", payload["_gorchestra_truncated"])
	}
}

func TestBoundedEventResponsesCompactsSingleOversizedEvent(t *testing.T) {
	oversized := testEventWithPayload(1, "tool.call.completed", map[string]any{
		"aggregated_output": strings.Repeat("x", 1024),
	})

	for _, preferLatest := range []bool{false, true} {
		responses := boundedEventResponses([]store.Event{oversized}, preferLatest, 512)
		if got, want := len(responses), 1; got != want {
			t.Fatalf("preferLatest=%t: expected %d event, got %d", preferLatest, want, got)
		}

		var payload map[string]any
		if err := json.Unmarshal(responses[0].Payload, &payload); err != nil {
			t.Fatalf("preferLatest=%t: expected payload to be JSON object: %v", preferLatest, err)
		}
		if payload["_gorchestra_window_truncated"] != true {
			t.Fatalf("preferLatest=%t: expected window truncation marker, got %#v", preferLatest, payload)
		}
	}
}

func TestEventAttachmentServesOriginalImageDataWhenHistoryIsTruncated(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	imageData := bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47}, maxEventPayloadStringLen/4)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageData)
	store.setEvents(
		testSessionID,
		testEventWithPayload(1, "user.message.completed", map[string]any{
			"text": "see image",
			"attachments": []map[string]any{
				{
					"name":       "large.png",
					"media_type": "image/png",
					"data_url":   dataURL,
					"size_bytes": len(imageData),
				},
			},
		}),
	)

	history := httptest.NewRecorder()
	NewRouter(Dependencies{Store: store}).ServeHTTP(
		history,
		httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?after_seq=0&limit=10", nil),
	)
	if history.Code != http.StatusOK {
		t.Fatalf("expected history status %d, got %d with body %s", http.StatusOK, history.Code, history.Body.String())
	}
	var response eventHistoryResponse
	decodeJSON(t, history, &response)
	var payload map[string]any
	if err := json.Unmarshal(response.Events[0].Payload, &payload); err != nil {
		t.Fatalf("expected payload to be JSON object: %v", err)
	}
	attachments := payload["attachments"].([]any)
	attachment := attachments[0].(map[string]any)
	if got := attachment["data_url"].(string); got != "" {
		t.Fatalf("expected history data_url to be omitted, got %q", got)
	}

	image := httptest.NewRecorder()
	NewRouter(Dependencies{Store: store}).ServeHTTP(
		image,
		httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/1/attachments/0", nil),
	)
	if image.Code != http.StatusOK {
		t.Fatalf("expected image status %d, got %d with body %s", http.StatusOK, image.Code, image.Body.String())
	}
	if got := image.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("expected image/png content type, got %q", got)
	}
	if !bytes.Equal(image.Body.Bytes(), imageData) {
		t.Fatalf("expected original image bytes, got %d bytes", image.Body.Len())
	}
}

func TestEventToolContentServesBinaryResultBlocks(t *testing.T) {
	tests := []struct {
		name      string
		block     map[string]any
		mediaType string
		data      []byte
	}{
		{
			name:      "image",
			block:     map[string]any{"type": "image", "mimeType": "image/png"},
			mediaType: "image/png",
			data:      bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47}, maxEventPayloadStringLen/4),
		},
		{
			name:      "audio",
			block:     map[string]any{"type": "audio", "mimeType": "audio/wav"},
			mediaType: "audio/wav",
			data:      []byte("RIFFaudio"),
		},
		{
			name: "embedded resource",
			block: map[string]any{
				"type": "resource",
				"resource": map[string]any{
					"uri":      "mcp://files/report.pdf",
					"mimeType": "application/pdf",
				},
			},
			mediaType: "application/pdf",
			data:      []byte("%PDF-result"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			block := maps.Clone(test.block)
			if test.name == "embedded resource" {
				resource := maps.Clone(block["resource"].(map[string]any))
				resource["blob"] = base64.StdEncoding.EncodeToString(test.data)
				block["resource"] = resource
			} else {
				block["data"] = base64.StdEncoding.EncodeToString(test.data)
			}

			store := newFakeHTTPStore()
			store.addSession(testSessionID)
			store.setEvents(
				testSessionID,
				testEventWithPayload(1, "tool.call.completed", map[string]any{
					"result": map[string]any{"content": []any{block}},
				}),
			)

			response := httptest.NewRecorder()
			NewRouter(Dependencies{Store: store}).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/1/tool-content/0", nil),
			)
			if response.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != test.mediaType {
				t.Fatalf("expected %q content type, got %q", test.mediaType, got)
			}
			if !bytes.Equal(response.Body.Bytes(), test.data) {
				t.Fatalf("expected original content, got %q", response.Body.Bytes())
			}

			if test.name == "image" {
				history := httptest.NewRecorder()
				NewRouter(Dependencies{Store: store}).ServeHTTP(
					history,
					httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?after_seq=0&limit=10", nil),
				)
				var historyResponse eventHistoryResponse
				decodeJSON(t, history, &historyResponse)
				var historyPayload map[string]any
				if err := json.Unmarshal(historyResponse.Events[0].Payload, &historyPayload); err != nil {
					t.Fatalf("expected history payload: %v", err)
				}
				result := historyPayload["result"].(map[string]any)
				content := result["content"].([]any)
				historyBlock := content[0].(map[string]any)
				if historyBlock["data"] != "" || historyBlock["_gorchestra_truncated"] != true {
					t.Fatalf("expected binary history data to be omitted, got %#v", historyBlock)
				}
			}
		})
	}
}

func TestGeneratedImageHistoryUsesCompactMetadataAndServesStoredPNG(t *testing.T) {
	ctx := context.Background()
	dbStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "images.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbStore.Close() })
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{AgentType: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	image := bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47}, 10000)
	payload, _ := json.Marshal(map[string]any{
		"item_type": "imageGeneration", "tool": "Generate image",
		"result": map[string]any{"content": []any{map[string]any{
			"type": "image", "mimeType": "image/png", "name": "Generated image.png", "data": base64.StdEncoding.EncodeToString(image),
		}}},
	})
	event, err := dbStore.AppendEvent(ctx, store.AppendEventParams{
		SessionID: session.ID, Type: "tool.call.completed", Role: "assistant", Status: store.EventStatusCompleted, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{Store: dbStore})
	history := httptest.NewRecorder()
	router.ServeHTTP(history, httptest.NewRequest(http.MethodGet, "/api/sessions/"+session.ID+"/events?tail=true", nil))
	var response eventHistoryResponse
	decodeJSON(t, history, &response)
	if len(response.Events) != 1 || response.Events[0].Type != "tool.call.completed" || history.Body.Len() > 2000 {
		t.Fatalf("expected visible compact image event, got %d events and %d bytes", len(response.Events), history.Body.Len())
	}
	imageResponse := httptest.NewRecorder()
	router.ServeHTTP(imageResponse, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sessions/%s/events/%d/tool-content/0", session.ID, event.Seq), nil))
	if imageResponse.Code != http.StatusOK || imageResponse.Header().Get("Content-Type") != "image/png" || !bytes.Equal(imageResponse.Body.Bytes(), image) {
		t.Fatalf("expected original PNG, got HTTP %d with %d bytes", imageResponse.Code, imageResponse.Body.Len())
	}
}

func TestEventToolOutputServesExternalizedStoreBlob(t *testing.T) {
	ctx := context.Background()
	dbStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = dbStore.Close() })
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{AgentType: "codex"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	output := strings.Repeat("externalized output\n", 2000)
	payload, err := json.Marshal(map[string]any{"output": output})
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	event, err := dbStore.AppendEvent(ctx, store.AppendEventParams{
		SessionID: session.ID,
		Type:      "tool.call.completed",
		Role:      "assistant",
		Status:    store.EventStatusCompleted,
		Payload:   payload,
	})
	if err != nil {
		t.Fatalf("append event: %v", err)
	}

	response := httptest.NewRecorder()
	NewRouter(Dependencies{Store: dbStore}).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sessions/%s/events/%d/tool-output", session.ID, event.Seq), nil),
	)
	if response.Code != http.StatusOK || response.Body.String() != output {
		t.Fatalf("expected full externalized output, got status %d and %d bytes", response.Code, response.Body.Len())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/plain; charset=utf-8" {
		t.Fatalf("expected text output content type, got %q", contentType)
	}
}

func TestFiftyTurnHistoryKeepsLargeToolResultsBelowWireBudget(t *testing.T) {
	ctx := context.Background()
	dbStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = dbStore.Close() })
	session, err := dbStore.CreateSession(ctx, store.CreateSessionParams{AgentType: "codex"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for turn := 0; turn < 50; turn++ {
		for _, item := range []struct {
			eventType string
			role      string
			payload   map[string]any
		}{
			{"user.message.completed", "user", map[string]any{"text": fmt.Sprintf("turn %d", turn)}},
			{"tool.call.started", "assistant", map[string]any{"item_id": fmt.Sprintf("tool_%d", turn), "command": "large-command"}},
			{"tool.call.completed", "assistant", map[string]any{"item_id": fmt.Sprintf("tool_%d", turn), "output": strings.Repeat(fmt.Sprintf("turn-%d-output ", turn), 10_000)}},
		} {
			payload, marshalErr := json.Marshal(item.payload)
			if marshalErr != nil {
				t.Fatalf("marshal turn event: %v", marshalErr)
			}
			if _, appendErr := dbStore.AppendEvent(ctx, store.AppendEventParams{
				SessionID: session.ID,
				Type:      item.eventType,
				Role:      item.role,
				Status:    store.EventStatusCompleted,
				Payload:   payload,
			}); appendErr != nil {
				t.Fatalf("append turn event: %v", appendErr)
			}
		}
	}

	response := httptest.NewRecorder()
	NewRouter(Dependencies{Store: dbStore}).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sessions/%s/events?tail=true&turns=50&max_bytes=%d", session.ID, maxEventHistoryBytes), nil),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("expected history status 200, got %d: %s", response.Code, response.Body.String())
	}
	if response.Body.Len() >= 512*1024 {
		t.Fatalf("expected compact 50-turn response below 512 KiB, got %d bytes", response.Body.Len())
	}
	var history eventHistoryResponse
	decodeJSON(t, response, &history)
	if len(history.Events) != 150 {
		t.Fatalf("expected complete 50-turn history, got %d events", len(history.Events))
	}
}

func TestEventHistoryAppliesDefaultLimit(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	call := store.lastListCall(t)
	if call.afterSeq != 0 {
		t.Fatalf("expected default after_seq 0, got %d", call.afterSeq)
	}
	if call.limit != defaultEventLimit {
		t.Fatalf("expected default limit %d, got %d", defaultEventLimit, call.limit)
	}
}

func TestEventHistoryCapsLargeLimit(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?limit=5000", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	call := store.lastListCall(t)
	if call.limit != maxEventLimit {
		t.Fatalf("expected capped limit %d, got %d", maxEventLimit, call.limit)
	}
}

func TestEventHistoryTailReturnsRecentEvents(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "agent.message.delta"),
		testEvent(2, "agent.message.delta"),
		testEvent(3, "agent.message.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?tail=true&limit=2", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response eventHistoryResponse
	decodeJSON(t, rec, &response)

	if got, want := len(response.Events), 3; got != want {
		t.Fatalf("expected %d events, got %d: %#v", want, got, response.Events)
	}
	if got := eventSeqs(response.Events); !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Fatalf("expected seqs [1 2 3], got %v", got)
	}
	call := store.lastListCall(t)
	if call.mode != "before" || call.beforeSeq != 2 {
		t.Fatalf("expected boundary backfill before seq 2, got mode=%q before_seq=%d", call.mode, call.beforeSeq)
	}
}

func TestEventHistoryTailReturnsCompleteRecentTurns(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "user.message.completed"),
		testEvent(2, "agent.message.completed"),
		testEvent(3, "user.message.completed"),
		testEvent(4, "agent.log.delta"),
		testEvent(5, "agent.message.completed"),
		testEvent(6, "user.message.completed"),
		testEvent(7, "agent.message.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?tail=true&turns=2", nil)
	rec := httptest.NewRecorder()
	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response eventHistoryResponse
	decodeJSON(t, rec, &response)
	if got := eventSeqs(response.Events); !reflect.DeepEqual(got, []int64{3, 5, 6, 7}) {
		t.Fatalf("expected two complete visible turns, got %v", got)
	}
	call := store.lastListCall(t)
	if call.mode != "tail_turns" || call.turns != 2 || call.filter.IncludeDebug {
		t.Fatalf("expected filtered two-turn tail call, got %#v", call)
	}
}

func TestEventHistoryLatestByteBudgetKeepsWholeTurns(t *testing.T) {
	events := []store.Event{
		testEvent(1, "session.status.updated"),
		testEvent(2, "user.message.completed"),
		testEventWithPayload(3, "agent.message.completed", map[string]any{"text": strings.Repeat("a", 512)}),
		testEvent(4, "user.message.completed"),
		testEventWithPayload(5, "tool.call.completed", map[string]any{"text": strings.Repeat("b", 512)}),
		testEvent(6, "agent.message.completed"),
	}
	all := eventResponses(events)
	budget := encodedEventResponsesBytes(all[3:]) + 2

	responses := boundedEventResponses(events, true, budget)
	if got := eventSeqs(responses); !reflect.DeepEqual(got, []int64{4, 5, 6}) {
		t.Fatalf("expected the latest complete turn, got %v", got)
	}
	if responses[0].Type != "user.message.completed" {
		t.Fatalf("expected a whole-turn boundary, got %q", responses[0].Type)
	}
}

func TestEventHistoryLatestByteBudgetPreservesPreambleWhenItFits(t *testing.T) {
	events := []store.Event{
		testEvent(1, "session.status.updated"),
		testEvent(2, "user.message.completed"),
		testEvent(3, "agent.message.completed"),
	}

	responses := boundedEventResponses(events, true, maxEventHistoryBytes)
	if got := eventSeqs(responses); !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Fatalf("expected fitting preamble events to remain, got %v", got)
	}
}

func TestEventHistoryTurnWindowExpandsPastDefaultEventCountWhenBytesRemain(t *testing.T) {
	fakeStore := newFakeHTTPStore()
	fakeStore.addSession(testSessionID)
	events := make([]store.Event, 0, 600)
	for seq := int64(1); seq <= 600; seq++ {
		eventType := "agent.message.completed"
		if seq == 1 || seq%100 == 1 {
			eventType = "user.message.completed"
		}
		events = append(events, testEvent(seq, eventType))
	}
	fakeStore.setEvents(testSessionID, events...)
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/sessions/"+testSessionID+"/events?tail=true&turns=50&max_bytes=2097152",
		nil,
	)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: fakeStore}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response eventHistoryResponse
	decodeJSON(t, rec, &response)
	if got, want := len(response.Events), 600; got != want {
		t.Fatalf("expected %d byte-fitting events, got %d", want, got)
	}
	if got := fakeStore.listCallCount(); got != 2 {
		t.Fatalf("expected adaptive 500 then 1000 event reads, got %d", got)
	}
}

func TestEventHistoryReportsServerCursorWhenVisiblePageIsEmpty(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(testSessionID, testEvent(4, "provider.codex.request"))
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?after_seq=0", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response eventHistoryResponse
	decodeJSON(t, rec, &response)
	if len(response.Events) != 0 || response.Page.ServerLastSeq != 4 {
		t.Fatalf("expected empty visible page with server cursor 4, got %#v", response)
	}
}

func TestEventHistoryCompressesLargeJSONResponses(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEventWithPayload(1, "agent.message.completed", map[string]any{"text": strings.Repeat("compressible", 512)}),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?after_seq=0", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("expected gzip response, got %q", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("expected Accept-Encoding vary header, got %q", got)
	}
	reader, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("open gzip response: %v", err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip response: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close gzip response: %v", err)
	}
	var response eventHistoryResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode gzip response: %v", err)
	}
	if got := eventSeqs(response.Events); !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("expected compressed event response, got %v", got)
	}
}

func TestEventHistoryBeforeSeqReturnsPreviousCompleteTurns(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "user.message.completed"),
		testEvent(2, "agent.message.completed"),
		testEvent(3, "user.message.completed"),
		testEvent(4, "agent.log.delta"),
		testEvent(5, "agent.message.completed"),
		testEvent(6, "user.message.completed"),
		testEvent(7, "agent.message.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?before_seq=6&turns=2&include_debug=true", nil)
	rec := httptest.NewRecorder()
	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response eventHistoryResponse
	decodeJSON(t, rec, &response)
	if got := eventSeqs(response.Events); !reflect.DeepEqual(got, []int64{1, 2, 3, 4, 5}) {
		t.Fatalf("expected previous two complete turns, got %v", got)
	}
	call := store.lastListCall(t)
	if call.mode != "before_turns" || call.beforeSeq != 6 || call.turns != 2 || !call.filter.IncludeDebug {
		t.Fatalf("expected unfiltered two-turn before call, got %#v", call)
	}
}

func TestEventHistoryAfterSeqReturnsNextCompleteTurnsWithHardLimit(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "user.message.completed"),
		testEvent(2, "agent.message.completed"),
		testEvent(3, "user.message.completed"),
		testEvent(4, "agent.message.completed"),
		testEvent(5, "user.message.completed"),
		testEvent(6, "agent.message.completed"),
		testEvent(7, "user.message.completed"),
		testEvent(8, "agent.message.completed"),
		testEvent(9, "user.message.completed"),
		testEvent(10, "agent.message.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?after_seq=2&turns=2&limit=3", nil)
	rec := httptest.NewRecorder()
	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response eventHistoryResponse
	decodeJSON(t, rec, &response)
	if got := eventSeqs(response.Events); !reflect.DeepEqual(got, []int64{3, 4, 5}) {
		t.Fatalf("expected bounded forward turn page, got %v", got)
	}
	if !response.Page.HasNewer || !response.Page.EndsMidTurn {
		t.Fatalf("expected forward continuation metadata, got %#v", response.Page)
	}
	call := store.lastListCall(t)
	if call.mode != "after_turns" || call.afterSeq != 2 || call.turns != 2 || call.limit != 3 {
		t.Fatalf("expected bounded forward turn call, got %#v", call)
	}
}

func TestEventHistoryAroundSeqReturnsTargetWithNeighboringTurns(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "user.message.completed"),
		testEvent(2, "agent.message.completed"),
		testEvent(3, "user.message.completed"),
		testEvent(4, "tool.call.started"),
		testEvent(5, "tool.call.completed"),
		testEvent(6, "agent.message.completed"),
		testEvent(7, "user.message.completed"),
		testEvent(8, "agent.message.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?around_seq=4&turns=1", nil)
	rec := httptest.NewRecorder()
	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response eventHistoryResponse
	decodeJSON(t, rec, &response)
	if got := eventSeqs(response.Events); !reflect.DeepEqual(got, []int64{3, 4, 5, 6, 7, 8}) {
		t.Fatalf("expected target turn window, got %v", got)
	}
	if !response.Page.HasOlder {
		t.Fatalf("expected older continuation, got %#v", response.Page)
	}
}

func TestEventHistoryRejectsInvalidTurnPagination(t *testing.T) {
	for _, query := range []string{
		"?tail=true&turns=0",
		"?tail=true&turns=nope",
		"?turns=2",
	} {
		t.Run(query, func(t *testing.T) {
			store := newFakeHTTPStore()
			store.addSession(testSessionID)
			req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events"+query, nil)
			rec := httptest.NewRecorder()
			NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestEventHistoryRejectsInvalidByteBudget(t *testing.T) {
	for _, maxBytes := range []string{"0", "-1", "nope"} {
		t.Run(maxBytes, func(t *testing.T) {
			store := newFakeHTTPStore()
			store.addSession(testSessionID)
			req := httptest.NewRequest(
				http.MethodGet,
				"/api/sessions/"+testSessionID+"/events?tail=true&turns=2&max_bytes="+maxBytes,
				nil,
			)
			rec := httptest.NewRecorder()
			NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
			assertErrorResponse(t, rec, "max_bytes must be a positive integer")
			if got := store.listCallCount(); got != 0 {
				t.Fatalf("expected no history read, got %d", got)
			}
		})
	}
}

func TestEventHistoryCapsLargeTurnLimit(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?tail=true&turns=500", nil)
	rec := httptest.NewRecorder()
	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if call := store.lastListCall(t); call.turns != maxEventTurnLimit {
		t.Fatalf("expected capped turn limit %d, got %#v", maxEventTurnLimit, call)
	}
}

func TestEventHistoryBeforeSeqReturnsPreviousEvents(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "agent.message.delta"),
		testEvent(2, "agent.message.delta"),
		testEvent(3, "agent.message.delta"),
		testEvent(4, "agent.message.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?before_seq=4&limit=2", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response eventHistoryResponse
	decodeJSON(t, rec, &response)

	if got, want := len(response.Events), 3; got != want {
		t.Fatalf("expected %d events, got %d: %#v", want, got, response.Events)
	}
	if got := eventSeqs(response.Events); !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Fatalf("expected seqs [1 2 3], got %v", got)
	}
	call := store.lastListCall(t)
	if call.mode != "before" || call.beforeSeq != 2 {
		t.Fatalf("expected boundary backfill before seq 2, got mode=%q before_seq=%d", call.mode, call.beforeSeq)
	}
}

func TestEventHistoryTailKeepsExactLimitWhenBoundaryIsSafe(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "user.message.completed"),
		testEvent(2, "agent.message.completed"),
		testEvent(3, "agent.run.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?tail=true&limit=2", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response eventHistoryResponse
	decodeJSON(t, rec, &response)

	if got := eventSeqs(response.Events); !reflect.DeepEqual(got, []int64{2, 3}) {
		t.Fatalf("expected seqs [2 3], got %v", got)
	}
	if got := store.listCallCount(); got != 1 {
		t.Fatalf("expected a single list call, got %d", got)
	}
}

func TestEventHistoryBeforeSeqOverfetchesToolCompletionBoundary(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEventWithPayload(1, "tool.call.started", map[string]any{"item_id": "tool_1"}),
		testEventWithPayload(2, "tool.call.completed", map[string]any{"item_id": "tool_1"}),
		testEvent(3, "agent.message.completed"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?before_seq=3&limit=1", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response eventHistoryResponse
	decodeJSON(t, rec, &response)

	if got := eventSeqs(response.Events); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("expected seqs [1 2], got %v", got)
	}
}

func TestEventHistoryRejectsMixedCursors(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?after_seq=1&tail=true", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "use only one event history cursor")
	if got := store.listCallCount(); got != 0 {
		t.Fatalf("expected no list calls, got %d", got)
	}
}

func TestEventHistoryRejectsInvalidAfterSeq(t *testing.T) {
	for _, afterSeq := range []string{"-1", "nope"} {
		t.Run(afterSeq, func(t *testing.T) {
			store := newFakeHTTPStore()
			store.addSession(testSessionID)

			req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?after_seq="+afterSeq, nil)
			rec := httptest.NewRecorder()

			NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
			assertErrorResponse(t, rec, "after_seq must be a non-negative integer")
			if got := store.listCallCount(); got != 0 {
				t.Fatalf("expected no list calls, got %d", got)
			}
		})
	}
}

func TestEventHistoryRejectsInvalidLimit(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events?limit=nope", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "limit must be a non-negative integer")
	if got := store.listCallCount(); got != 0 {
		t.Fatalf("expected no list calls, got %d", got)
	}
}

func TestEventHistoryReturns404ForUnknownSession(t *testing.T) {
	store := newFakeHTTPStore()

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/missing/events", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store}).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, rec.Code, rec.Body.String())
	}
	assertErrorResponse(t, rec, "session not found")
	if got := store.listCallCount(); got != 0 {
		t.Fatalf("expected no list calls, got %d", got)
	}
}

func TestSSEReplaySendsMissedEventsBeforeLiveEvents(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(testSessionID, testEvent(1, "agent.message.delta"))

	subscriber := &fakeSubscriber{}
	store.onList = func(string, int64, int) {
		subscriber.send(testEvent(2, "agent.message.completed"))
		subscriber.closeAll()
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/stream?after_seq=0", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected content type text/event-stream, got %q", got)
	}

	body := rec.Body.String()
	assertSeqOrder(t, body, 1, 2)
}

func TestSSEReplayMergesBufferedTransientEvents(t *testing.T) {
	fakeStore := newFakeHTTPStore()
	fakeStore.addSession(testSessionID)
	fakeStore.setEvents(testSessionID, testEvent(1, "agent.run.started"))
	transient := testEvent(2, "agent.message.delta")
	transient.Transient = true
	subscriber := &recentFakeSubscriber{
		recent: []store.Event{
			testEvent(1, "agent.run.started"),
			transient,
		},
	}
	fakeStore.onList = func(string, int64, int) {
		subscriber.closeAll()
	}

	recorder := httptest.NewRecorder()
	NewRouter(Dependencies{Store: fakeStore, Events: subscriber}).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/stream?after_seq=0", nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	assertSeqOrder(t, body, 1, 2)
	if !strings.Contains(body, `"transient":true`) {
		t.Fatalf("expected buffered transient event in replay:\n%s", body)
	}
}

func TestSSERequestsBoundedTailResyncWhenReplayExceedsLimit(t *testing.T) {
	fakeStore := newFakeHTTPStore()
	fakeStore.addSession(testSessionID)
	events := make([]store.Event, 0, maxEventLimit+1)
	for seq := 1; seq <= maxEventLimit+1; seq++ {
		events = append(events, testEvent(int64(seq), "agent.message.completed"))
	}
	fakeStore.setEvents(testSessionID, events...)

	rec := httptest.NewRecorder()
	NewRouter(Dependencies{Store: fakeStore, Events: &fakeSubscriber{}}).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/stream?after_seq=0", nil),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: "+streamResyncEventType+"\n") {
		t.Fatalf("expected bounded resync control event:\n%s", body)
	}
	if !strings.Contains(body, `"reason":"replay_window_exceeded"`) {
		t.Fatalf("expected replay overflow reason:\n%s", body)
	}
	if strings.Contains(body, `"seq":`) {
		t.Fatalf("expected no durable replay events before resync:\n%s", body)
	}
}

func TestSSEFiltersDebugReplayAndLiveEventsByDefault(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "agent.log.delta"),
		testEvent(2, "agent.message.completed"),
	)

	subscriber := &fakeSubscriber{}
	store.onList = func(string, int64, int) {
		subscriber.send(testEvent(3, "agent.log.delta"))
		subscriber.send(testEvent(4, "agent.run.completed"))
		subscriber.closeAll()
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/stream?after_seq=0", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if strings.Contains(body, `"seq":1`) || strings.Contains(body, `"seq":3`) {
		t.Fatalf("expected debug events to be filtered from stream:\n%s", body)
	}
	if !strings.Contains(body, `"seq":2`) || !strings.Contains(body, `"seq":4`) {
		t.Fatalf("expected visible replay and live events in stream:\n%s", body)
	}
}

func TestSSEIncludesDebugEventsWhenRequested(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "agent.log.delta"),
		testEvent(2, "agent.message.completed"),
	)

	subscriber := &fakeSubscriber{}
	store.onList = func(string, int64, int) {
		subscriber.closeAll()
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/stream?after_seq=0&include_debug=true", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"seq":1`) || !strings.Contains(body, `"seq":2`) {
		t.Fatalf("expected debug and visible replay events in stream:\n%s", body)
	}
}

func TestSSEUsesIDEventAndDataFields(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(testSessionID, testEvent(1, "agent.message.delta"))

	subscriber := &fakeSubscriber{}
	store.onList = func(string, int64, int) {
		subscriber.closeAll()
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/stream", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "id: 1\n") {
		t.Fatalf("expected SSE id field in body:\n%s", body)
	}
	if !strings.Contains(body, "event: agent.message.delta\n") {
		t.Fatalf("expected SSE event field in body:\n%s", body)
	}

	response := firstSSEData(t, body)
	if response.Seq != 1 {
		t.Fatalf("expected data seq 1, got %d", response.Seq)
	}
	if response.Type != "agent.message.delta" {
		t.Fatalf("expected data type agent.message.delta, got %q", response.Type)
	}

	var payload map[string]string
	if err := json.Unmarshal(response.Payload, &payload); err != nil {
		t.Fatalf("expected payload to be JSON object: %v", err)
	}
	if got := payload["text"]; got != "event 1" {
		t.Fatalf("expected payload text %q, got %q", "event 1", got)
	}
}

func TestSSETruncatesLargePayloadStrings(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEventWithPayload(1, "tool.call.completed", map[string]any{
			"item_id":           "tool_1",
			"aggregated_output": strings.Repeat("x", maxEventPayloadStringLen+1024),
		}),
	)

	subscriber := &fakeSubscriber{}
	store.onList = func(string, int64, int) {
		subscriber.closeAll()
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/stream", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	response := firstSSEData(t, rec.Body.String())
	var payload map[string]any
	if err := json.Unmarshal(response.Payload, &payload); err != nil {
		t.Fatalf("expected payload to be JSON object: %v", err)
	}
	output, ok := payload["aggregated_output"].(string)
	if !ok {
		t.Fatalf("expected aggregated_output string, got %#v", payload["aggregated_output"])
	}
	if len(output) > maxEventPayloadStringLen {
		t.Fatalf("expected output to be capped at %d bytes, got %d", maxEventPayloadStringLen, len(output))
	}
	if !strings.Contains(output, "gorchestra truncated") {
		t.Fatal("expected truncation marker in output")
	}
	if payload["_gorchestra_truncated"] != true {
		t.Fatalf("expected truncation marker flag, got %#v", payload["_gorchestra_truncated"])
	}
}

func TestSSEFlushesHeadersBeforeWaitingForEvents(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)

	subscriber := &fakeSubscriber{}
	store.onList = func(string, int64, int) {
		subscriber.closeAll()
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/stream", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !rec.Flushed {
		t.Fatal("expected stream headers to be flushed before waiting for events")
	}
	if body := rec.Body.String(); !strings.Contains(body, ": connected\n\n") {
		t.Fatalf("expected initial connected comment in stream body:\n%s", body)
	}
}

func TestSessionActivityStreamSendsAllLiveSessionEvents(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)

	subscriber := &fakeSubscriber{}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/activity/stream", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)
	}()

	waitFor(t, func() bool {
		return subscriber.subscribeAllCount() == 1
	})

	subscriber.sendAll(testEvent(1, "agent.input.requested"))
	subscriber.closeAll()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("activity stream did not exit after subscriber closed")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, ": connected\n\n") {
		t.Fatalf("expected initial connected comment in stream body:\n%s", body)
	}
	if !strings.Contains(body, "event: agent.input.requested\n") {
		t.Fatalf("expected input requested event in body:\n%s", body)
	}
	response := firstSSEData(t, body)
	if response.SessionID != testSessionID || response.Type != "agent.input.requested" {
		t.Fatalf("expected activity event for %s, got %#v", testSessionID, response)
	}
}

func TestSessionActivityStreamUpdatesItsTransientWatchWithoutReconnecting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbStore, events, _, handler := newIntegrationAPI(t, ctx, fake.New())
	first := createIntegrationSession(t, ctx, dbStore)
	second := createIntegrationSession(t, ctx, dbStore)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/sessions/activity/stream?after_cursor=0&client_id=browser-one&watch_session_id="+first.ID,
		nil,
	).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, req)
	}()
	waitFor(t, func() bool { return events.SessionActivityStats().ActiveSubscribers == 1 })

	watchBody := bytes.NewBufferString(fmt.Sprintf(
		`{"client_id":"browser-one","session_id":"%s","include_debug":false}`,
		second.ID,
	))
	watchReq := httptest.NewRequest(http.MethodPut, "/api/sessions/activity/watch", watchBody)
	watchRec := httptest.NewRecorder()
	handler.ServeHTTP(watchRec, watchReq)
	if watchRec.Code != http.StatusOK || !strings.Contains(watchRec.Body.String(), `"connected":true`) {
		t.Fatalf("expected connected watch update, got %d: %s", watchRec.Code, watchRec.Body.String())
	}

	transient, err := events.Append(ctx, eventservice.AppendParams{
		SessionID: second.ID,
		Type:      "agent.message.delta",
		Role:      "assistant",
		Status:    store.EventStatusDelta,
		Payload:   json.RawMessage(`{"text":"multiplexed"}`),
	})
	if err != nil {
		t.Fatalf("append transient event: %v", err)
	}
	waitFor(t, func() bool { return events.SessionActivityStats().TransientDeliveries == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("activity stream did not close after cancellation")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: agent.message.delta\n") {
		t.Fatalf("expected watched transient event in body:\n%s", body)
	}
	if strings.Contains(body, fmt.Sprintf("id: %d\n", transient.Seq)) {
		t.Fatalf("transient event must not advance the durable SSE cursor:\n%s", body)
	}
	diagnostics := httptest.NewRecorder()
	handler.ServeHTTP(diagnostics, httptest.NewRequest(http.MethodGet, "/api/diagnostics/performance", nil))
	if !strings.Contains(diagnostics.Body.String(), `"live_transient_events":1`) ||
		!strings.Contains(diagnostics.Body.String(), `"active_connections":0`) {
		t.Fatalf("expected completed transient stream diagnostics: %s", diagnostics.Body.String())
	}
}

func TestSessionActivityStreamCanReceiveEverySessionsTransientOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dbStore, events, _, handler := newIntegrationAPI(t, ctx, fake.New())
	first := createIntegrationSession(t, ctx, dbStore)
	second := createIntegrationSession(t, ctx, dbStore)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/sessions/activity/stream?after_cursor=0&client_id=browser-one&watch_session_id="+first.ID+"&live_scope=all",
		nil,
	).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, req)
	}()
	waitFor(t, func() bool { return events.SessionActivityStats().ActiveSubscribers == 1 })

	if _, err := events.Append(ctx, eventservice.AppendParams{
		SessionID: second.ID,
		Type:      "agent.message.delta",
		Role:      "assistant",
		Status:    store.EventStatusDelta,
		Payload:   json.RawMessage(`{"message_id":"msg_background","text":"background"}`),
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return events.SessionActivityStats().TransientDeliveries == 1 })
	watchBody := bytes.NewBufferString(fmt.Sprintf(
		`{"client_id":"browser-one","session_id":"%s","include_debug":false}`,
		second.ID,
	))
	watchRec := httptest.NewRecorder()
	handler.ServeHTTP(watchRec, httptest.NewRequest(http.MethodPut, "/api/sessions/activity/watch", watchBody))
	if watchRec.Code != http.StatusOK || !strings.Contains(watchRec.Body.String(), "background") ||
		!strings.Contains(watchRec.Body.String(), `"watermark":1`) {
		t.Fatalf("expected selected-session live snapshot, got %d: %s", watchRec.Code, watchRec.Body.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("activity stream did not close after cancellation")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: session.live.snapshot\n") {
		t.Fatalf("expected initial live snapshot in body:\n%s", body)
	}
	if !strings.Contains(body, `"session_id":"`+second.ID+`"`) || !strings.Contains(body, "background") {
		t.Fatalf("expected background transient event in body:\n%s", body)
	}
}

func TestLiveEventResponsePreservesBoundedSnapshotPayload(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"message_id": "msg_large",
		"text":       strings.Repeat("x", maxOtherEventPayloadBytes+1024),
	})
	if err != nil {
		t.Fatal(err)
	}
	event := testEvent(1, "agent.message.delta")
	event.Payload = payload
	event.Transient = true

	response := newLiveEventResponse(event)
	if !bytes.Equal(response.Payload, payload) {
		t.Fatalf("expected live snapshot payload to remain intact: got %d bytes, want %d", len(response.Payload), len(payload))
	}
}

func TestSessionActivityStreamReplaysAfterGlobalCursor(t *testing.T) {
	baseStore := newFakeHTTPStore()
	baseStore.addSession(testSessionID)
	first := testEvent(8, "agent.message.completed")
	first.GlobalSeq = 21
	second := testEvent(9, "agent.run.completed")
	second.GlobalSeq = 22
	fakeStore := &fakeGlobalHTTPStore{
		fakeHTTPStore: baseStore,
		cursor:        22,
		globalEvents:  []store.Event{first, second},
	}
	subscriber := &fakeSubscriber{}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/activity/stream?after_cursor=21", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		NewRouter(Dependencies{Store: fakeStore, Events: subscriber}).ServeHTTP(rec, req)
	}()
	waitFor(t, func() bool { return subscriber.subscribeAllCount() == 1 })
	subscriber.closeAll()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("activity stream did not exit")
	}

	body := rec.Body.String()
	if strings.Contains(body, `"global_seq":21`) || !strings.Contains(body, `"global_seq":22`) {
		t.Fatalf("expected replay strictly after global cursor 21:\n%s", body)
	}
	if !strings.Contains(body, "id: 22\n") {
		t.Fatalf("expected global cursor as SSE id:\n%s", body)
	}
}

func TestSessionActivityStreamWithoutCursorRemainsLiveOnlyForLegacyClients(t *testing.T) {
	baseStore := newFakeHTTPStore()
	baseStore.addSession(testSessionID)
	replayed := testEvent(8, "agent.message.completed")
	replayed.GlobalSeq = 21
	fakeStore := &fakeGlobalHTTPStore{
		fakeHTTPStore: baseStore,
		cursor:        21,
		globalEvents:  []store.Event{replayed},
	}
	subscriber := &fakeSubscriber{}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/activity/stream", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		NewRouter(Dependencies{Store: fakeStore, Events: subscriber}).ServeHTTP(rec, req)
	}()
	waitFor(t, func() bool { return subscriber.subscribeAllCount() == 1 })
	subscriber.closeAll()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("activity stream did not exit")
	}

	if strings.Contains(rec.Body.String(), `"global_seq":21`) {
		t.Fatalf("expected a cursorless legacy connection to remain live-only:\n%s", rec.Body.String())
	}
}

func TestSessionActivityStreamRequestsSnapshotWhenReplayWindowIsExceeded(t *testing.T) {
	baseStore := newFakeHTTPStore()
	baseStore.addSession(testSessionID)
	events := make([]store.Event, maxEventLimit+1)
	for index := range events {
		events[index] = testEvent(int64(index+1), "agent.message.completed")
		events[index].GlobalSeq = int64(index + 1)
	}
	fakeStore := &fakeGlobalHTTPStore{
		fakeHTTPStore: baseStore,
		cursor:        int64(len(events)),
		globalEvents:  events,
	}
	subscriber := &fakeSubscriber{}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/activity/stream?after_cursor=0", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: fakeStore, Events: subscriber}).ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: "+streamResyncEventType+"\n") ||
		!strings.Contains(body, `"cursor":1001`) {
		t.Fatalf("expected an explicit resync at the current cursor:\n%s", body)
	}
	if strings.Contains(body, `"global_seq":1`) {
		t.Fatalf("expected no partial replay before resync:\n%s", body)
	}
}

func TestSessionActivityStreamExcludesSelectedSession(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)

	subscriber := &fakeSubscriber{}
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/sessions/activity/stream?exclude_session_id="+testSessionID,
		nil,
	)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)
	}()

	waitFor(t, func() bool {
		return subscriber.subscribeAllCount() == 1
	})

	excluded := testEvent(1, "agent.run.completed")
	subscriber.sendAll(excluded)
	included := testEvent(2, "agent.run.completed")
	included.SessionID = "sess_other"
	subscriber.sendAll(included)
	subscriber.closeAll()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("activity stream did not exit")
	}

	body := rec.Body.String()
	if strings.Contains(body, `"session_id":"`+testSessionID+`"`) {
		t.Fatalf("expected selected session to be excluded:\n%s", body)
	}
	if !strings.Contains(body, `"session_id":"sess_other"`) {
		t.Fatalf("expected another session to remain visible:\n%s", body)
	}
}

func TestSessionActivityStreamFiltersDebugEvents(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)

	subscriber := &fakeSubscriber{}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/activity/stream", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)
	}()

	waitFor(t, func() bool {
		return subscriber.subscribeAllCount() == 1
	})

	subscriber.sendAll(testEvent(1, "agent.log.delta"))
	subscriber.sendAll(testEvent(2, "agent.run.completed"))
	subscriber.closeAll()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("activity stream did not exit after subscriber closed")
	}

	body := rec.Body.String()
	if strings.Contains(body, `"seq":1`) {
		t.Fatalf("expected debug activity event to be filtered:\n%s", body)
	}
	if !strings.Contains(body, `"seq":2`) {
		t.Fatalf("expected visible activity event in stream:\n%s", body)
	}
}

func TestSSESkipsDuplicateLiveEventsAlreadySentDuringReplay(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(
		testSessionID,
		testEvent(1, "agent.message.delta"),
		testEvent(2, "agent.message.completed"),
	)

	subscriber := &fakeSubscriber{}
	store.onList = func(string, int64, int) {
		subscriber.send(testEvent(2, "agent.message.completed"))
		subscriber.closeAll()
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/stream", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if got := strings.Count(body, `"seq":2`); got != 1 {
		t.Fatalf("expected duplicate seq 2 to be filtered once, got %d occurrences in body:\n%s", got, body)
	}
}

func TestSSEDoesNotLoseEventsAppendedDuringStreamSetup(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)
	store.setEvents(testSessionID, testEvent(1, "agent.message.delta"))

	subscriber := &fakeSubscriber{}
	appended := false
	store.onList = func(string, int64, int) {
		if appended {
			return
		}
		appended = true

		event := testEvent(2, "agent.message.completed")
		store.appendEvent(event)
		subscriber.send(event)
		subscriber.closeAll()
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/stream?after_seq=0", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	assertSeqOrder(t, body, 1, 2)
	if got := strings.Count(body, `"seq":2`); got != 1 {
		t.Fatalf("expected setup event seq 2 exactly once, got %d occurrences in body:\n%s", got, body)
	}
}

func TestStreamCleanupUnsubscribesWhenRequestCancelled(t *testing.T) {
	store := newFakeHTTPStore()
	store.addSession(testSessionID)

	subscriber := &fakeSubscriber{}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+testSessionID+"/events/stream", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		NewRouter(Dependencies{Store: store, Events: subscriber}).ServeHTTP(rec, req)
	}()

	waitFor(t, func() bool {
		return subscriber.subscribeCount() == 1
	})

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not exit after request cancellation")
	}

	if got := subscriber.unsubscribeCount(); got != 1 {
		t.Fatalf("expected one unsubscribe, got %d", got)
	}
}

type fakeHTTPStore struct {
	mu       sync.Mutex
	sessions map[string]store.Session
	events   map[string][]store.Event

	listCalls         []listCall
	listSessionsCalls []listSessionsCall
	onList            func(sessionID string, afterSeq int64, limit int)
	getErr            error
	listErr           error
}

type fakeGlobalHTTPStore struct {
	*fakeHTTPStore
	cursor       int64
	globalEvents []store.Event
	calls        []string
}

func (s *fakeGlobalHTTPStore) GlobalEventCursor(context.Context) (int64, error) {
	s.calls = append(s.calls, "cursor")
	return s.cursor, nil
}

func (s *fakeGlobalHTTPStore) ListSessions(ctx context.Context, params store.ListSessionsParams) ([]store.Session, error) {
	s.calls = append(s.calls, "sessions")
	return s.fakeHTTPStore.ListSessions(ctx, params)
}

func (s *fakeGlobalHTTPStore) ListGlobalEventsFiltered(
	_ context.Context,
	afterCursor int64,
	limit int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	events := make([]store.Event, 0, len(s.globalEvents))
	for _, event := range s.globalEvents {
		if event.GlobalSeq > afterCursor && eventVisible(event, filter) {
			events = append(events, event)
		}
	}
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return append([]store.Event(nil), events...), nil
}

type listCall struct {
	sessionID string
	afterSeq  int64
	beforeSeq int64
	limit     int
	turns     int
	mode      string
	filter    store.EventListFilter
}

type listSessionsCall struct {
	limit           int
	status          store.SessionStatus
	includeArchived bool
}

func newFakeHTTPStore() *fakeHTTPStore {
	return &fakeHTTPStore{
		sessions: make(map[string]store.Session),
		events:   make(map[string][]store.Event),
	}
}

func (s *fakeHTTPStore) addSession(id string) {
	s.addSessionWith(store.Session{
		ID:        id,
		Title:     "Test session",
		AgentType: "fake",
		Status:    store.SessionStatusIdle,
		CreatedAt: testCreatedAt,
		UpdatedAt: testCreatedAt,
	})
}

func (s *fakeHTTPStore) addSessionWith(session store.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[session.ID] = session
}

func (s *fakeHTTPStore) CreateSession(_ context.Context, params store.CreateSessionParams) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(params.AgentType) == "" {
		return store.Session{}, store.ErrInvalidArgument
	}

	id := fmt.Sprintf("sess_fake_%d", len(s.sessions)+1)
	session := store.Session{
		ID:            id,
		Title:         params.Title,
		AgentType:     params.AgentType,
		Status:        store.SessionStatusIdle,
		WorkspacePath: params.WorkspacePath,
		CreatedAt:     testCreatedAt,
		UpdatedAt:     testCreatedAt,
	}
	s.sessions[id] = session

	return session, nil
}

func (s *fakeHTTPStore) setEvents(sessionID string, events ...store.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events[sessionID] = append([]store.Event(nil), events...)
	sort.Slice(s.events[sessionID], func(i, j int) bool {
		return s.events[sessionID][i].Seq < s.events[sessionID][j].Seq
	})
}

func (s *fakeHTTPStore) appendEvent(event store.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events[event.SessionID] = append(s.events[event.SessionID], event)
	sort.Slice(s.events[event.SessionID], func(i, j int) bool {
		return s.events[event.SessionID][i].Seq < s.events[event.SessionID][j].Seq
	})
}

func (s *fakeHTTPStore) GetSession(_ context.Context, stringID string) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.getErr != nil {
		return store.Session{}, s.getErr
	}

	session, ok := s.sessions[stringID]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	s.applySessionCounts(&session)

	return session, nil
}

func (s *fakeHTTPStore) ListSessions(_ context.Context, params store.ListSessionsParams) ([]store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.listSessionsCalls = append(s.listSessionsCalls, listSessionsCall{
		limit:           params.Limit,
		status:          params.Status,
		includeArchived: params.IncludeArchived,
	})

	sessions := make([]store.Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		if !params.IncludeArchived && session.ArchivedAt != nil {
			continue
		}
		if params.Status != "" && session.Status != params.Status {
			continue
		}
		s.applySessionCounts(&session)
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].UpdatedAt.Equal(sessions[j].UpdatedAt) {
			return sessions[i].ID > sessions[j].ID
		}
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	if params.Limit > 0 && len(sessions) > params.Limit {
		sessions = sessions[:params.Limit]
	}

	return append([]store.Session(nil), sessions...), nil
}

func (s *fakeHTTPStore) applySessionCounts(session *store.Session) {
	events := s.events[session.ID]
	session.EventCount = int64(len(events))
	session.ToolCount = int64(countToolActivityEvents(events))
	for _, event := range events {
		if !event.Transient && event.Seq > session.LastEventSeq {
			session.LastEventSeq = event.Seq
		}
	}
}

func countToolActivityEvents(events []store.Event) int {
	count := 0
	for _, event := range events {
		if event.Type == "tool.call.started" || event.Type == "file.change.started" {
			count++
		}
	}
	return count
}

func (s *fakeHTTPStore) UpdateSessionTitle(_ context.Context, params store.UpdateSessionTitleParams) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[params.ID]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}

	session.Title = strings.TrimSpace(params.Title)
	session.UpdatedAt = testCreatedAt
	s.sessions[params.ID] = session

	return session, nil
}

func (s *fakeHTTPStore) UpdateSessionParent(_ context.Context, params store.UpdateSessionParentParams) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[params.ID]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	session.ParentSessionID = strings.TrimSpace(params.ParentSessionID)
	if session.ParentSessionID == "" {
		session.LineageDepth = 0
	} else if parent, exists := s.sessions[session.ParentSessionID]; exists {
		session.LineageDepth = parent.LineageDepth + 1
	} else {
		return store.Session{}, store.ErrNotFound
	}
	session.UpdatedAt = testCreatedAt
	s.sessions[params.ID] = session

	return session, nil
}

func (s *fakeHTTPStore) UpdateSessionWorkspace(_ context.Context, params store.UpdateSessionWorkspaceParams) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[params.ID]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}

	session.WorkspacePath = strings.TrimSpace(params.WorkspacePath)
	session.UpdatedAt = testCreatedAt
	s.sessions[params.ID] = session

	return session, nil
}

func (s *fakeHTTPStore) UpdateSessionAgentOptions(_ context.Context, params store.UpdateSessionAgentOptionsParams) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[params.ID]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}

	session.AgentOptions = params.AgentOptions
	session.UpdatedAt = testCreatedAt
	s.sessions[params.ID] = session

	return session, nil
}

func (s *fakeHTTPStore) UpdateSessionPin(_ context.Context, params store.UpdateSessionPinParams) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[params.ID]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	if params.Pinned && session.ArchivedAt != nil {
		return store.Session{}, store.ErrInvalidArgument
	}
	if params.Pinned {
		pinnedAt := testCreatedAt.Add(12 * time.Minute)
		session.PinnedAt = &pinnedAt
	} else {
		session.PinnedAt = nil
	}
	s.sessions[params.ID] = session

	return session, nil
}

func (s *fakeHTTPStore) ArchiveSession(_ context.Context, params store.ArchiveSessionParams) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[params.ID]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}

	archivedAt := testCreatedAt
	session.ArchivedAt = &archivedAt
	session.PinnedAt = nil
	session.UpdatedAt = archivedAt
	s.sessions[params.ID] = session

	return session, nil
}

func (s *fakeHTTPStore) RestoreSession(_ context.Context, params store.RestoreSessionParams) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[params.ID]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	session.ArchivedAt = nil
	session.UpdatedAt = testCreatedAt.Add(11 * time.Minute)
	s.sessions[params.ID] = session
	s.applySessionCounts(&session)

	return session, nil
}

func (s *fakeHTTPStore) UpdateSessionStatus(_ context.Context, params store.UpdateSessionStatusParams) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[params.ID]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}

	session.Status = params.Status
	session.UpdatedAt = testCreatedAt
	if isTestTerminalSessionStatus(params.Status) {
		completedAt := testCreatedAt
		session.CompletedAt = &completedAt
	} else {
		session.CompletedAt = nil
	}
	s.sessions[params.ID] = session

	return session, nil
}

func (s *fakeHTTPStore) SetSessionProviderSessionID(_ context.Context, params store.SetSessionProviderSessionIDParams) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[params.ID]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	if session.ProviderSessionID != "" && session.ProviderSessionID != params.ProviderSessionID && !params.Replace {
		return store.Session{}, store.ErrInvalidArgument
	}
	session.ProviderSessionID = params.ProviderSessionID
	session.UpdatedAt = testCreatedAt
	s.sessions[params.ID] = session
	return session, nil
}

func (s *fakeHTTPStore) ClearSessionProviderSessionID(_ context.Context, params store.ClearSessionProviderSessionIDParams) (store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[params.ID]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	session.ProviderSessionID = ""
	session.UpdatedAt = testCreatedAt
	s.sessions[params.ID] = session
	return session, nil
}

func (s *fakeHTTPStore) ClearNotificationAttention(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(sessionID) == "" {
		return store.ErrInvalidArgument
	}

	session, ok := s.sessions[sessionID]
	if !ok {
		return nil
	}
	session.NotificationAttentionSeq = 0
	s.sessions[sessionID] = session
	return nil
}

func (s *fakeHTTPStore) ClearAllNotificationAttention(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for sessionID, session := range s.sessions {
		session.NotificationAttentionSeq = 0
		s.sessions[sessionID] = session
	}
	return nil
}

func (s *fakeHTTPStore) EnqueueMessage(context.Context, store.EnqueueMessageParams) (store.QueuedMessage, error) {
	return store.QueuedMessage{}, nil
}

func (s *fakeHTTPStore) ListQueuedMessages(context.Context, string) ([]store.QueuedMessage, error) {
	return nil, nil
}

func (s *fakeHTTPStore) RemoveQueuedMessage(context.Context, store.QueueMessageIDParams) (store.QueuedMessage, error) {
	return store.QueuedMessage{}, store.ErrNotFound
}

func (s *fakeHTTPStore) ClaimNextQueuedMessage(context.Context, string) (store.QueuedMessage, error) {
	return store.QueuedMessage{}, store.ErrNotFound
}

func (s *fakeHTTPStore) MarkQueuedMessageSent(context.Context, store.QueueMessageIDParams) (store.QueuedMessage, error) {
	return store.QueuedMessage{}, nil
}

func (s *fakeHTTPStore) ReleaseQueuedMessage(context.Context, store.QueueMessageIDParams) (store.QueuedMessage, error) {
	return store.QueuedMessage{}, nil
}

func (s *fakeHTTPStore) ListEvents(_ context.Context, sessionID string, afterSeq int64, limit int) ([]store.Event, error) {
	s.mu.Lock()
	s.listCalls = append(s.listCalls, listCall{sessionID: sessionID, afterSeq: afterSeq, limit: limit, mode: "after"})
	onList := s.onList
	listErr := s.listErr
	s.mu.Unlock()

	if onList != nil {
		onList(sessionID, afterSeq, limit)
	}
	if listErr != nil {
		return nil, listErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	events := s.events[sessionID]
	filtered := make([]store.Event, 0, len(events))
	for _, event := range events {
		if event.Seq > afterSeq {
			filtered = append(filtered, event)
		}
	}

	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}

	return append([]store.Event(nil), filtered...), nil
}

func (s *fakeHTTPStore) ListEventsFiltered(
	ctx context.Context,
	sessionID string,
	afterSeq int64,
	limit int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	if filter.IncludeDebug {
		return s.ListEvents(ctx, sessionID, afterSeq, limit)
	}

	s.mu.Lock()
	s.listCalls = append(s.listCalls, listCall{sessionID: sessionID, afterSeq: afterSeq, limit: limit, mode: "after", filter: filter})
	onList := s.onList
	listErr := s.listErr
	s.mu.Unlock()

	if onList != nil {
		onList(sessionID, afterSeq, limit)
	}
	if listErr != nil {
		return nil, listErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	events := s.events[sessionID]
	filtered := make([]store.Event, 0, len(events))
	for _, event := range events {
		if event.Seq > afterSeq && eventVisible(event, filter) {
			filtered = append(filtered, event)
		}
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return append([]store.Event(nil), filtered...), nil
}

func (s *fakeHTTPStore) GetEvent(_ context.Context, sessionID string, seq int64) (store.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listErr != nil {
		return store.Event{}, s.listErr
	}

	for _, event := range s.events[sessionID] {
		if event.Seq == seq {
			return event, nil
		}
	}
	return store.Event{}, store.ErrNotFound
}

func (s *fakeHTTPStore) ListRecentEvents(_ context.Context, sessionID string, limit int) ([]store.Event, error) {
	s.mu.Lock()
	s.listCalls = append(s.listCalls, listCall{sessionID: sessionID, limit: limit, mode: "tail"})
	listErr := s.listErr
	s.mu.Unlock()

	if listErr != nil {
		return nil, listErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	events := s.events[sessionID]
	start := 0
	if limit > 0 && len(events) > limit {
		start = len(events) - limit
	}
	return append([]store.Event(nil), events[start:]...), nil
}

func (s *fakeHTTPStore) ListRecentEventsFiltered(
	ctx context.Context,
	sessionID string,
	limit int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	if filter.IncludeDebug {
		return s.ListRecentEvents(ctx, sessionID, limit)
	}

	s.mu.Lock()
	s.listCalls = append(s.listCalls, listCall{sessionID: sessionID, limit: limit, mode: "tail", filter: filter})
	listErr := s.listErr
	s.mu.Unlock()

	if listErr != nil {
		return nil, listErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	events := s.events[sessionID]
	filtered := filterVisibleEvents(events, filter)
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return append([]store.Event(nil), filtered...), nil
}

func (s *fakeHTTPStore) ListEventsBefore(_ context.Context, sessionID string, beforeSeq int64, limit int) ([]store.Event, error) {
	s.mu.Lock()
	s.listCalls = append(s.listCalls, listCall{sessionID: sessionID, beforeSeq: beforeSeq, limit: limit, mode: "before"})
	listErr := s.listErr
	s.mu.Unlock()

	if listErr != nil {
		return nil, listErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	events := s.events[sessionID]
	filtered := make([]store.Event, 0, len(events))
	for _, event := range events {
		if event.Seq < beforeSeq {
			filtered = append(filtered, event)
		}
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return append([]store.Event(nil), filtered...), nil
}

func (s *fakeHTTPStore) ListEventsBeforeFiltered(
	ctx context.Context,
	sessionID string,
	beforeSeq int64,
	limit int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	if filter.IncludeDebug {
		return s.ListEventsBefore(ctx, sessionID, beforeSeq, limit)
	}

	s.mu.Lock()
	s.listCalls = append(s.listCalls, listCall{sessionID: sessionID, beforeSeq: beforeSeq, limit: limit, mode: "before", filter: filter})
	listErr := s.listErr
	s.mu.Unlock()

	if listErr != nil {
		return nil, listErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	events := s.events[sessionID]
	filtered := make([]store.Event, 0, len(events))
	for _, event := range events {
		if event.Seq < beforeSeq && eventVisible(event, filter) {
			filtered = append(filtered, event)
		}
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return append([]store.Event(nil), filtered...), nil
}

func (s *fakeHTTPStore) ListRecentEventTurnsFiltered(
	_ context.Context,
	sessionID string,
	turns int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	return s.listEventTurns(sessionID, 0, false, false, turns, 0, filter)
}

func (s *fakeHTTPStore) ListRecentEventTurnsPageFiltered(
	_ context.Context,
	sessionID string,
	turns int,
	limit int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	return s.listEventTurns(sessionID, 0, false, false, turns, limit, filter)
}

func (s *fakeHTTPStore) ListEventTurnsBeforeFiltered(
	_ context.Context,
	sessionID string,
	beforeSeq int64,
	turns int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	return s.listEventTurns(sessionID, beforeSeq, true, false, turns, 0, filter)
}

func (s *fakeHTTPStore) ListEventTurnsBeforePageFiltered(
	_ context.Context,
	sessionID string,
	beforeSeq int64,
	turns int,
	limit int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	return s.listEventTurns(sessionID, beforeSeq, true, false, turns, limit, filter)
}

func (s *fakeHTTPStore) ListEventTurnsAfterFiltered(
	_ context.Context,
	sessionID string,
	afterSeq int64,
	turns int,
	limit int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	return s.listEventTurns(sessionID, afterSeq, false, true, turns, limit, filter)
}

func (s *fakeHTTPStore) listEventTurns(
	sessionID string,
	boundSeq int64,
	hasBeforeSeq bool,
	hasAfterSeq bool,
	turns int,
	limit int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	s.mu.Lock()
	mode := "tail_turns"
	if hasBeforeSeq {
		mode = "before_turns"
	} else if hasAfterSeq {
		mode = "after_turns"
	}
	s.listCalls = append(s.listCalls, listCall{
		sessionID: sessionID,
		beforeSeq: boundSeq,
		afterSeq:  boundSeq,
		limit:     limit,
		turns:     turns,
		mode:      mode,
		filter:    filter,
	})
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}

	events := s.events[sessionID]
	turnStarts := make([]int64, 0)
	for _, event := range events {
		if hasBeforeSeq && event.Seq >= boundSeq {
			continue
		}
		if hasAfterSeq && event.Seq <= boundSeq {
			continue
		}
		if event.Type == "user.message.completed" {
			turnStarts = append(turnStarts, event.Seq)
		}
	}
	startSeq := int64(0)
	endSeq := int64(0)
	if hasAfterSeq {
		startSeq = boundSeq + 1
		if len(turnStarts) > turns {
			endSeq = turnStarts[turns]
		}
	} else if len(turnStarts) >= turns {
		startSeq = turnStarts[len(turnStarts)-turns]
	}

	filtered := make([]store.Event, 0, len(events))
	for _, event := range events {
		if startSeq > 0 && event.Seq < startSeq {
			continue
		}
		if hasBeforeSeq && event.Seq >= boundSeq {
			continue
		}
		if endSeq > 0 && event.Seq >= endSeq {
			continue
		}
		if eventVisible(event, filter) {
			filtered = append(filtered, event)
		}
	}
	if limit > 0 && len(filtered) > limit {
		if hasAfterSeq {
			filtered = filtered[:limit]
		} else {
			filtered = filtered[len(filtered)-limit:]
		}
	}
	return append([]store.Event(nil), filtered...), nil
}

func filterVisibleEvents(events []store.Event, filter store.EventListFilter) []store.Event {
	filtered := make([]store.Event, 0, len(events))
	for _, event := range events {
		if eventVisible(event, filter) {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func (s *fakeHTTPStore) lastListCall(t *testing.T) listCall {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.listCalls) == 0 {
		t.Fatal("expected at least one ListEvents call")
	}

	return s.listCalls[len(s.listCalls)-1]
}

func (s *fakeHTTPStore) listCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.listCalls)
}

func (s *fakeHTTPStore) lastListSessionsLimit(t *testing.T) int {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.listSessionsCalls) == 0 {
		t.Fatal("expected at least one ListSessions call")
	}
	return s.listSessionsCalls[len(s.listSessionsCalls)-1].limit
}

func (s *fakeHTTPStore) lastListSessionsStatus(t *testing.T) store.SessionStatus {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.listSessionsCalls) == 0 {
		t.Fatal("expected at least one ListSessions call")
	}
	return s.listSessionsCalls[len(s.listSessionsCalls)-1].status
}

func (s *fakeHTTPStore) listSessionsCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.listSessionsCalls)
}

func (s *fakeHTTPStore) lastListSessionsIncludeArchived(t *testing.T) bool {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.listSessionsCalls) == 0 {
		t.Fatal("expected at least one ListSessions call")
	}
	return s.listSessionsCalls[len(s.listSessionsCalls)-1].includeArchived
}

func isTestTerminalSessionStatus(status store.SessionStatus) bool {
	return status == store.SessionStatusFailed
}

type fakeSubscriber struct {
	mu sync.Mutex

	channels        []chan store.Event
	allChannels     []chan store.Event
	subscribes      int
	allSubscribes   int
	unsubscribes    int
	allUnsubscribes int
}

type recentFakeSubscriber struct {
	fakeSubscriber
	recent []store.Event
}

func (s *recentFakeSubscriber) Recent(string) []store.Event {
	return append([]store.Event(nil), s.recent...)
}

func (s *fakeSubscriber) Subscribe(string) (<-chan store.Event, func()) {
	ch := make(chan store.Event, 16)

	s.mu.Lock()
	s.subscribes++
	s.channels = append(s.channels, ch)
	s.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()

			s.unsubscribes++
		})
	}

	return ch, unsubscribe
}

func (s *fakeSubscriber) SubscribeAll() (<-chan store.Event, func()) {
	ch := make(chan store.Event, 16)

	s.mu.Lock()
	s.allSubscribes++
	s.allChannels = append(s.allChannels, ch)
	s.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()

			s.allUnsubscribes++
		})
	}

	return ch, unsubscribe
}

func (s *fakeSubscriber) Append(context.Context, eventservice.AppendParams) (store.Event, error) {
	return store.Event{}, nil
}

func (s *fakeSubscriber) send(event store.Event) {
	ch := s.lastChannel()
	ch <- event
}

func (s *fakeSubscriber) sendAll(event store.Event) {
	ch := s.lastAllChannel()
	ch <- event
}

func (s *fakeSubscriber) closeAll() {
	s.mu.Lock()
	channels := append([]chan store.Event(nil), s.channels...)
	allChannels := append([]chan store.Event(nil), s.allChannels...)
	s.mu.Unlock()

	for _, ch := range channels {
		close(ch)
	}
	for _, ch := range allChannels {
		close(ch)
	}
}

func (s *fakeSubscriber) subscribeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.subscribes
}

func (s *fakeSubscriber) subscribeAllCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.allSubscribes
}

func (s *fakeSubscriber) unsubscribeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.unsubscribes
}

func (s *fakeSubscriber) unsubscribeAllCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.allUnsubscribes
}

func (s *fakeSubscriber) lastChannel() chan store.Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.channels) == 0 {
		panic("no subscriber channel")
	}

	return s.channels[len(s.channels)-1]
}

func (s *fakeSubscriber) lastAllChannel() chan store.Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.allChannels) == 0 {
		panic("no all-subscriber channel")
	}

	return s.allChannels[len(s.allChannels)-1]
}

func testEvent(seq int64, eventType string) store.Event {
	return store.Event{
		ID:        fmt.Sprintf("evt_%03d", seq),
		SessionID: testSessionID,
		Seq:       seq,
		Type:      eventType,
		Role:      "assistant",
		Status:    store.EventStatusDelta,
		Payload:   json.RawMessage(fmt.Sprintf(`{"text":"event %d"}`, seq)),
		CreatedAt: testCreatedAt,
	}
}

func testEventWithPayload(seq int64, eventType string, payload map[string]any) store.Event {
	event := testEvent(seq, eventType)
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	event.Payload = encoded
	return event
}

func eventSeqs(events []eventResponse) []int64 {
	seqs := make([]int64, 0, len(events))
	for _, event := range events {
		seqs = append(seqs, event.Seq)
	}
	return seqs
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, value any) {
	t.Helper()

	if err := json.Unmarshal(rec.Body.Bytes(), value); err != nil {
		t.Fatalf("failed to decode JSON response %q: %v", rec.Body.String(), err)
	}
}

func assertErrorResponse(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()

	var response errorResponse
	decodeJSON(t, rec, &response)
	if response.Error != want {
		t.Fatalf("expected error %q, got %q", want, response.Error)
	}
}

func firstSSEData(t *testing.T, body string) eventResponse {
	t.Helper()

	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}

		var response eventResponse
		if err := json.Unmarshal([]byte(data), &response); err != nil {
			t.Fatalf("failed to decode SSE data %q: %v", data, err)
		}
		return response
	}

	t.Fatalf("expected SSE data line in body:\n%s", body)
	return eventResponse{}
}

func assertSeqOrder(t *testing.T, body string, first int64, second int64) {
	t.Helper()

	firstIndex := strings.Index(body, fmt.Sprintf(`"seq":%d`, first))
	if firstIndex < 0 {
		t.Fatalf("expected seq %d in body:\n%s", first, body)
	}

	secondIndex := strings.Index(body, fmt.Sprintf(`"seq":%d`, second))
	if secondIndex < 0 {
		t.Fatalf("expected seq %d in body:\n%s", second, body)
	}

	if firstIndex > secondIndex {
		t.Fatalf("expected seq %d before seq %d in body:\n%s", first, second, body)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("condition was not met before timeout")
}

var _ Store = (*fakeHTTPStore)(nil)
var _ EventService = (*fakeSubscriber)(nil)
