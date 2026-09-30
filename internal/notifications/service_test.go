package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/threave-io/threave/internal/store"
)

func TestServiceSendsTerminalRunNotifications(t *testing.T) {
	ctx := context.Background()
	fakeStore := &memoryStore{
		keys: store.NotificationKeys{
			PublicKey:  "public",
			PrivateKey: "private",
		},
		session: store.Session{ID: "sess_1", Title: "Build release"},
		recentEvents: []store.Event{
			{Type: "agent.message.completed", Payload: []byte(`{"text":"The release build is complete and ready to verify."}`)},
		},
		subscriptions: []store.PushSubscription{
			{Endpoint: "endpoint-1", P256DH: "p256dh", Auth: "auth", Origin: "https://example.test"},
		},
	}
	sender := &recordingSender{responses: []*http.Response{testResponse(http.StatusCreated)}}
	service := NewService(fakeStore, WithSender(sender))

	service.notifyTerminalEvent(ctx, store.Event{
		SessionID: "sess_1",
		Seq:       8,
		Type:      "agent.run.completed",
		Status:    store.EventStatusCompleted,
	})

	if len(sender.payloads) != 1 {
		t.Fatalf("expected one push notification, got %d", len(sender.payloads))
	}
	if !bytes.Contains(sender.payloads[0], []byte(`"title":"Completed"`)) {
		t.Fatalf("expected completion title in payload, got %s", sender.payloads[0])
	}
	if bytes.Contains(sender.payloads[0], []byte(`"web_push"`)) {
		t.Fatalf("expected classic service worker payload without web_push, got %s", sender.payloads[0])
	}
	if !bytes.Contains(sender.payloads[0], []byte(`"url":"https://example.test/sessions/sess_1?notification_seq=8"`)) {
		t.Fatalf("expected absolute notification URL in payload, got %s", sender.payloads[0])
	}
	if !bytes.Contains(sender.payloads[0], []byte(`"seq":8`)) {
		t.Fatalf("expected terminal seq in payload, got %s", sender.payloads[0])
	}
	if !bytes.Contains(sender.payloads[0], []byte(`"session_id":"sess_1"`)) {
		t.Fatalf("expected session id in payload, got %s", sender.payloads[0])
	}
	if !bytes.Contains(sender.payloads[0], []byte("Build release: The release build is complete")) {
		t.Fatalf("expected response excerpt in payload, got %s", sender.payloads[0])
	}
	var payload map[string]any
	if err := json.Unmarshal(sender.payloads[0], &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if _, ok := payload["notification"]; ok {
		t.Fatalf("expected classic payload without notification object, got %s", sender.payloads[0])
	}
	if payload["session_id"] != "sess_1" || payload["event_type"] != "agent.run.completed" || payload["seq"] != float64(8) {
		t.Fatalf("expected metadata in notification payload, got %#v", payload)
	}
	if len(fakeStore.attempts) != 1 || fakeStore.attempts[0].HTTPStatus != http.StatusCreated {
		t.Fatalf("expected recorded delivery attempt, got %#v", fakeStore.attempts)
	}
	if len(fakeStore.attention) != 1 || fakeStore.attention[0].SessionID != "sess_1" || fakeStore.attention[0].Seq != 8 {
		t.Fatalf("expected notification attention recorded, got %#v", fakeStore.attention)
	}
}

func TestServiceSendsGenericPermissionNotification(t *testing.T) {
	ctx := context.Background()
	fakeStore := &memoryStore{keys: store.NotificationKeys{PublicKey: "public", PrivateKey: "private"}, session: store.Session{ID: "sess_1", Title: "Deploy release"}, subscriptions: []store.PushSubscription{{Endpoint: "endpoint-1", P256DH: "p256dh", Auth: "auth", Origin: "https://example.test"}}}
	sender := &recordingSender{responses: []*http.Response{testResponse(http.StatusCreated)}}
	NewService(fakeStore, WithSender(sender)).notifyPermissionEvent(ctx, store.Event{SessionID: "sess_1", Seq: 9, Type: "agent.permission.requested", Status: store.EventStatusStarted, Payload: []byte(`{"command":"secret command"}`)})
	if len(sender.payloads) != 1 || !bytes.Contains(sender.payloads[0], []byte(`"title":"Approval needed"`)) {
		t.Fatalf("unexpected permission notification %s", sender.payloads)
	}
	if bytes.Contains(sender.payloads[0], []byte("secret command")) {
		t.Fatalf("permission notification leaked request detail: %s", sender.payloads[0])
	}
	if len(fakeStore.attention) != 1 || fakeStore.attention[0].Seq != 9 {
		t.Fatalf("expected permission attention, got %#v", fakeStore.attention)
	}
}

func TestServiceDisablesGoneSubscriptions(t *testing.T) {
	ctx := context.Background()
	fakeStore := &memoryStore{
		keys: store.NotificationKeys{
			PublicKey:  "public",
			PrivateKey: "private",
		},
		subscriptions: []store.PushSubscription{
			{Endpoint: "endpoint-1", P256DH: "p256dh", Auth: "auth"},
		},
	}
	sender := &recordingSender{responses: []*http.Response{testResponse(http.StatusGone)}}
	service := NewService(fakeStore, WithSender(sender))

	if err := service.SendTest(ctx); err != nil {
		t.Fatalf("send test: %v", err)
	}

	if len(fakeStore.disabled) != 1 || fakeStore.disabled[0].Endpoint != "endpoint-1" {
		t.Fatalf("expected stale subscription disabled, got %#v", fakeStore.disabled)
	}
}

func TestServiceSendsBadgeVariantTestNotifications(t *testing.T) {
	for _, test := range []struct {
		name          string
		variant       string
		kind          string
		declarative   bool
		badge         any
		expectTag     bool
		expectData    bool
		classicWorker bool
	}{
		{
			name:        "declarative string",
			variant:     "declarative-string",
			kind:        "test-declarative-string",
			declarative: true,
			badge:       "1",
			expectTag:   true,
			expectData:  true,
		},
		{
			name:        "declarative number",
			variant:     "declarative-number",
			kind:        "test-declarative-number",
			declarative: true,
			badge:       float64(1),
			expectTag:   true,
			expectData:  true,
		},
		{
			name:        "minimal number",
			variant:     "declarative-minimal-number",
			kind:        "test-declarative-minimal-number",
			declarative: true,
			badge:       float64(1),
		},
		{
			name:          "classic service worker",
			variant:       "classic-sw-number",
			kind:          "test-classic-sw-number",
			classicWorker: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fakeStore := &memoryStore{
				keys: store.NotificationKeys{
					PublicKey:  "public",
					PrivateKey: "private",
				},
				subscriptions: []store.PushSubscription{
					{Endpoint: "endpoint-1", P256DH: "p256dh", Auth: "auth", Origin: "https://example.test"},
				},
			}
			sender := &recordingSender{responses: []*http.Response{testResponse(http.StatusCreated)}}
			service := NewService(fakeStore, WithSender(sender))

			if err := service.SendBadgeVariantTest(ctx, test.variant); err != nil {
				t.Fatalf("send badge variant test: %v", err)
			}

			if len(sender.payloads) != 1 {
				t.Fatalf("expected one push notification, got %d", len(sender.payloads))
			}
			var payload map[string]any
			if err := json.Unmarshal(sender.payloads[0], &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if test.classicWorker {
				if _, ok := payload["web_push"]; ok {
					t.Fatalf("expected classic payload without web_push, got %s", sender.payloads[0])
				}
				if _, ok := payload["notification"]; ok {
					t.Fatalf("expected classic payload without notification, got %s", sender.payloads[0])
				}
				if payload["url"] != "https://example.test/" {
					t.Fatalf("expected absolute root url, got %s", sender.payloads[0])
				}
			} else {
				notification, ok := payload["notification"].(map[string]any)
				if !ok {
					t.Fatalf("expected notification object, got %#v", payload["notification"])
				}
				if payload["web_push"] != float64(8030) || notification["app_badge"] != test.badge {
					t.Fatalf("expected declarative app badge payload, got %s", sender.payloads[0])
				}
				if _, ok := notification["tag"]; ok != test.expectTag {
					t.Fatalf("unexpected tag presence in payload %s", sender.payloads[0])
				}
				if _, ok := notification["data"]; ok != test.expectData {
					t.Fatalf("unexpected data presence in payload %s", sender.payloads[0])
				}
				if notification["navigate"] != "https://example.test/" {
					t.Fatalf("expected absolute root navigate URL, got %s", sender.payloads[0])
				}
			}
			if len(fakeStore.attempts) != 1 || fakeStore.attempts[0].PayloadKind != test.kind {
				t.Fatalf("expected delivery attempt kind %s, got %#v", test.kind, fakeStore.attempts)
			}
		})
	}
}

func TestServiceIgnoresNonTerminalEvents(t *testing.T) {
	fakeStore := &memoryStore{}
	sender := &recordingSender{}
	service := NewService(fakeStore, WithSender(sender))
	source := newMemoryEventSource()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	service.Start(ctx, source)
	source.events <- store.Event{SessionID: "sess_1", Type: "agent.message.delta", Status: store.EventStatusDelta}
	time.Sleep(20 * time.Millisecond)

	if len(sender.payloads) != 0 {
		t.Fatalf("expected no push notifications, got %d", len(sender.payloads))
	}
}

func TestWebPushHTTPClientSetsDeclarativeNotificationContentType(t *testing.T) {
	base := &recordingHTTPClient{}
	client := webPushHTTPClient{base: base, declarative: true}
	request, err := http.NewRequest(http.MethodPost, "https://push.example.test", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")

	if _, err := client.Do(request); err != nil {
		t.Fatalf("do request: %v", err)
	}

	if base.request == nil {
		t.Fatal("expected wrapped client to receive request")
	}
	if got := base.request.Header.Get("Content-Type"); got != declarativeNotificationContentType {
		t.Fatalf("expected content type %q, got %q", declarativeNotificationContentType, got)
	}
}

func TestWebPushHTTPClientPreservesClassicContentType(t *testing.T) {
	base := &recordingHTTPClient{}
	client := webPushHTTPClient{base: base}
	request, err := http.NewRequest(http.MethodPost, "https://push.example.test", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")

	if _, err := client.Do(request); err != nil {
		t.Fatalf("do request: %v", err)
	}

	if base.request == nil {
		t.Fatal("expected wrapped client to receive request")
	}
	if got := base.request.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("expected classic content type preserved, got %q", got)
	}
}

type memoryStore struct {
	keys          store.NotificationKeys
	session       store.Session
	recentEvents  []store.Event
	subscriptions []store.PushSubscription
	disabled      []store.DisablePushSubscriptionParams
	attempts      []store.RecordPushDeliveryAttemptParams
	attention     []store.MarkNotificationAttentionParams
}

func (s *memoryStore) GetNotificationKeys(context.Context) (store.NotificationKeys, error) {
	if s.keys.PublicKey == "" {
		return store.NotificationKeys{}, store.ErrNotFound
	}
	return s.keys, nil
}

func (s *memoryStore) SetNotificationKeys(_ context.Context, params store.SetNotificationKeysParams) (store.NotificationKeys, error) {
	s.keys = store.NotificationKeys{PublicKey: params.PublicKey, PrivateKey: params.PrivateKey}
	return s.keys, nil
}

func (s *memoryStore) SavePushSubscription(_ context.Context, params store.SavePushSubscriptionParams) (store.PushSubscription, error) {
	subscription := store.PushSubscription{
		Endpoint:  params.Endpoint,
		P256DH:    params.P256DH,
		Auth:      params.Auth,
		UserAgent: params.UserAgent,
		Origin:    params.Origin,
	}
	s.subscriptions = append(s.subscriptions, subscription)
	return subscription, nil
}

func (s *memoryStore) DeletePushSubscription(context.Context, string) error {
	return nil
}

func (s *memoryStore) DisablePushSubscription(_ context.Context, params store.DisablePushSubscriptionParams) error {
	s.disabled = append(s.disabled, params)
	return nil
}

func (s *memoryStore) ListPushSubscriptions(context.Context) ([]store.PushSubscription, error) {
	return s.subscriptions, nil
}

func (s *memoryStore) RecordPushDeliveryAttempt(_ context.Context, params store.RecordPushDeliveryAttemptParams) (store.PushDeliveryAttempt, error) {
	s.attempts = append(s.attempts, params)
	return store.PushDeliveryAttempt{
		ID:             int64(len(s.attempts)),
		EndpointHash:   params.EndpointHash,
		Origin:         params.Origin,
		PayloadKind:    params.PayloadKind,
		SessionID:      params.SessionID,
		EventType:      params.EventType,
		HTTPStatus:     params.HTTPStatus,
		ResponseStatus: params.ResponseStatus,
		Error:          params.Error,
		CreatedAt:      time.Now(),
	}, nil
}

func (s *memoryStore) MarkNotificationAttention(_ context.Context, params store.MarkNotificationAttentionParams) error {
	s.attention = append(s.attention, params)
	return nil
}

func (s *memoryStore) ListPushDeliveryAttempts(context.Context, int) ([]store.PushDeliveryAttempt, error) {
	attempts := make([]store.PushDeliveryAttempt, 0, len(s.attempts))
	for index, attempt := range s.attempts {
		attempts = append(attempts, store.PushDeliveryAttempt{
			ID:             int64(index + 1),
			EndpointHash:   attempt.EndpointHash,
			Origin:         attempt.Origin,
			PayloadKind:    attempt.PayloadKind,
			SessionID:      attempt.SessionID,
			EventType:      attempt.EventType,
			HTTPStatus:     attempt.HTTPStatus,
			ResponseStatus: attempt.ResponseStatus,
			Error:          attempt.Error,
			CreatedAt:      time.Now(),
		})
	}
	return attempts, nil
}

func (s *memoryStore) GetSession(context.Context, string) (store.Session, error) {
	return s.session, nil
}

func (s *memoryStore) ListRecentEvents(context.Context, string, int) ([]store.Event, error) {
	return s.recentEvents, nil
}

type recordingSender struct {
	responses []*http.Response
	payloads  [][]byte
}

func (s *recordingSender) Send(_ context.Context, _ store.NotificationKeys, _ store.PushSubscription, payload []byte) (*http.Response, error) {
	s.payloads = append(s.payloads, append([]byte(nil), payload...))
	if len(s.responses) == 0 {
		return testResponse(http.StatusCreated), nil
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

type recordingHTTPClient struct {
	request *http.Request
}

func (c *recordingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.request = req
	return testResponse(http.StatusCreated), nil
}

type memoryEventSource struct {
	events chan store.Event
}

func newMemoryEventSource() *memoryEventSource {
	return &memoryEventSource{events: make(chan store.Event, 1)}
}

func (s *memoryEventSource) SubscribeAll() (<-chan store.Event, func()) {
	return s.events, func() {}
}

func testResponse(statusCode int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Body:       io.NopCloser(bytes.NewReader(nil)),
	}
}

func (s *memoryStore) GetEvent(_ context.Context, sessionID string, seq int64) (store.Event, error) {
	for _, event := range s.recentEvents {
		if event.SessionID == sessionID && event.Seq == seq {
			return event, nil
		}
	}
	return store.Event{}, store.ErrNotFound
}
