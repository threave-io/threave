package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/threave-io/threave/internal/store"
)

func TestAcknowledgeNotificationRoute(t *testing.T) {
	service := &acknowledgementNotificationService{}
	router := NewRouter(Dependencies{Notifications: service})
	request := httptest.NewRequest(http.MethodPost, "/api/notifications/acknowledge", strings.NewReader(`{"endpoint":"phone","session_id":"sess_1","seq":8}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"acknowledged":true`) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body)
	}
	if service.endpoint != "phone" || service.sessionID != "sess_1" || service.seq != 8 || service.calls != 1 {
		t.Fatalf("incorrect ACK arguments: %+v", service)
	}
}

func TestAcknowledgeNotificationErrors(t *testing.T) {
	for _, test := range []struct {
		name, body    string
		err           error
		status, calls int
	}{
		{"invalid JSON", `{`, nil, http.StatusBadRequest, 0},
		{"oversized body", `{"endpoint":"` + strings.Repeat("x", 9*1024) + `"}`, nil, http.StatusBadRequest, 0},
		{"invalid ACK", `{}`, store.ErrInvalidArgument, http.StatusBadRequest, 1},
		{"storage error", `{}`, errors.New("private storage detail"), http.StatusInternalServerError, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &acknowledgementNotificationService{err: test.err}
			router := NewRouter(Dependencies{Notifications: service})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/notifications/acknowledge", strings.NewReader(test.body)))
			if response.Code != test.status || service.calls != test.calls {
				t.Fatalf("unexpected response: %d %s; calls=%d", response.Code, response.Body, service.calls)
			}
			if strings.Contains(response.Body.String(), "private storage detail") {
				t.Fatal("storage errors should not leak in ACK responses")
			}
		})
	}
}

type acknowledgementNotificationService struct {
	NotificationService
	endpoint, sessionID string
	seq                 int64
	calls               int
	err                 error
}

func (s *acknowledgementNotificationService) Acknowledge(_ context.Context, endpoint, sessionID string, seq int64) error {
	s.endpoint, s.sessionID, s.seq = endpoint, sessionID, seq
	s.calls++
	return s.err
}

func (s *acknowledgementNotificationService) Seen(_ context.Context, sessionID string, seq int64) error {
	s.sessionID, s.seq = sessionID, seq
	s.calls++
	return s.err
}

func TestSeenNotificationRoute(t *testing.T) {
	for _, test := range []struct {
		body   string
		err    error
		status int
	}{
		{`{"session_id":"s","seq":8}`, nil, 200},
		{`{`, nil, 400},
		{`{"session_id":"` + strings.Repeat("x", 9000) + `"}`, nil, 400},
		{`{}`, store.ErrInvalidArgument, 400},
		{`{}`, errors.New("private storage detail"), 500},
	} {
		service := &acknowledgementNotificationService{err: test.err}
		response := httptest.NewRecorder()
		NewRouter(Dependencies{Notifications: service}).ServeHTTP(response, httptest.NewRequest("POST", "/api/notifications/seen", strings.NewReader(test.body)))
		if response.Code != test.status || strings.Contains(response.Body.String(), "private storage detail") {
			t.Fatalf("unexpected response: %d %s", response.Code, response.Body)
		}
	}
}
