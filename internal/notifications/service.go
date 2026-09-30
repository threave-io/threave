package notifications

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/threave-io/threave/internal/store"
)

const (
	DefaultSubscriber                  = "https://github.com/threave-io/threave"
	declarativeNotificationContentType = "application/notification+json"
	sendTimeout                        = 15 * time.Second
)

type Store interface {
	GetNotificationKeys(ctx context.Context) (store.NotificationKeys, error)
	SetNotificationKeys(ctx context.Context, params store.SetNotificationKeysParams) (store.NotificationKeys, error)
	SavePushSubscription(ctx context.Context, params store.SavePushSubscriptionParams) (store.PushSubscription, error)
	DeletePushSubscription(ctx context.Context, endpoint string) error
	DisablePushSubscription(ctx context.Context, params store.DisablePushSubscriptionParams) error
	ListPushSubscriptions(ctx context.Context) ([]store.PushSubscription, error)
	RecordPushDeliveryAttempt(ctx context.Context, params store.RecordPushDeliveryAttemptParams) (store.PushDeliveryAttempt, error)
	ListPushDeliveryAttempts(ctx context.Context, limit int) ([]store.PushDeliveryAttempt, error)
	MarkNotificationAttention(ctx context.Context, params store.MarkNotificationAttentionParams) error
	GetSession(ctx context.Context, id string) (store.Session, error)
	GetEvent(ctx context.Context, sessionID string, seq int64) (store.Event, error)
	ListRecentEvents(ctx context.Context, sessionID string, limit int) ([]store.Event, error)
}

type EventSource interface {
	SubscribeAll() (<-chan store.Event, func())
}

type SubscriptionInput struct {
	Endpoint  string
	P256DH    string
	Auth      string
	UserAgent string
	Origin    string
}

type Sender interface {
	Send(ctx context.Context, keys store.NotificationKeys, subscription store.PushSubscription, payload []byte) (*http.Response, error)
}

type Service struct {
	store           Store
	sender          Sender
	subscriber      string
	logger          *log.Logger
	ackMu           sync.Mutex
	foregroundAcks  map[acknowledgementKey]time.Time
	seenCompletions map[completionKey]time.Time
	foregroundGrace time.Duration
}

type DebugState struct {
	PublicKeyFingerprint string
	Subscriptions        []DebugSubscription
	RecentAttempts       []DebugDeliveryAttempt
}

type DebugSubscription struct {
	EndpointHash string
	Origin       string
	UserAgent    string
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DisabledAt   *time.Time
}

type DebugDeliveryAttempt struct {
	ID             int64
	EndpointHash   string
	Origin         string
	PayloadKind    string
	SessionID      string
	EventType      string
	HTTPStatus     int
	ResponseStatus string
	Error          string
	CreatedAt      time.Time
}

type Option func(*Service)

func WithSender(sender Sender) Option {
	return func(s *Service) {
		if sender != nil {
			s.sender = sender
		}
	}
}

func WithLogger(logger *log.Logger) Option {
	return func(s *Service) {
		if logger != nil {
			s.logger = logger
		}
	}
}

func WithSubscriber(subscriber string) Option {
	return func(s *Service) {
		if strings.TrimSpace(subscriber) != "" {
			s.subscriber = strings.TrimSpace(subscriber)
		}
	}
}

func NewService(store Store, opts ...Option) *Service {
	service := &Service{
		store:           store,
		sender:          webPushSender{subscriber: DefaultSubscriber},
		subscriber:      DefaultSubscriber,
		logger:          log.Default(),
		foregroundGrace: time.Second,
	}
	for _, opt := range opts {
		opt(service)
	}
	if sender, ok := service.sender.(webPushSender); ok {
		sender.subscriber = service.subscriber
		service.sender = sender
	}
	return service
}

func (s *Service) PublicKey(ctx context.Context) (string, error) {
	keys, err := s.ensureKeys(ctx)
	if err != nil {
		return "", err
	}
	return keys.PublicKey, nil
}

func (s *Service) SaveSubscription(ctx context.Context, input SubscriptionInput) (store.PushSubscription, error) {
	subscription, err := s.store.SavePushSubscription(ctx, store.SavePushSubscriptionParams{
		Endpoint:  input.Endpoint,
		P256DH:    input.P256DH,
		Auth:      input.Auth,
		UserAgent: input.UserAgent,
		Origin:    input.Origin,
	})
	if err != nil {
		return store.PushSubscription{}, err
	}
	s.logf("notification subscription saved: endpoint=%s origin=%q user_agent=%q", endpointFingerprint(subscription.Endpoint), subscription.Origin, subscription.UserAgent)
	return subscription, nil
}

func (s *Service) DeleteSubscription(ctx context.Context, endpoint string) error {
	if err := s.store.DeletePushSubscription(ctx, endpoint); err != nil {
		return err
	}
	s.logf("notification subscription deleted: endpoint=%s", endpointFingerprint(endpoint))
	return nil
}

func (s *Service) SendTest(ctx context.Context) error {
	return s.sendToActiveSubscriptions(ctx, notificationInput{
		Kind:    "test",
		Title:   "Threave notifications enabled",
		Body:    "You will be notified when a session stops.",
		Path:    "/",
		Tag:     "gorchestra-test",
		Classic: true,
	})
}

func (s *Service) SendBadgeVariantTest(ctx context.Context, variant string) error {
	switch strings.TrimSpace(variant) {
	case "":
		return s.SendTest(ctx)
	case "declarative-string":
		return s.sendToActiveSubscriptions(ctx, notificationInput{
			Kind:            "test-declarative-string",
			Title:           "Badge test",
			Body:            "Declarative string badge",
			Path:            "/",
			Tag:             "gorchestra-test-declarative-string",
			BadgeValue:      "1",
			IncludeMetadata: true,
		})
	case "minimal-badge", "declarative-minimal-string":
		return s.sendToActiveSubscriptions(ctx, notificationInput{
			Kind:       "test-declarative-minimal-string",
			Title:      "Badge test",
			Body:       "Minimal declarative string badge",
			Path:       "/",
			BadgeValue: "1",
		})
	case "declarative-number":
		return s.sendToActiveSubscriptions(ctx, notificationInput{
			Kind:            "test-declarative-number",
			Title:           "Badge test",
			Body:            "Declarative numeric badge",
			Path:            "/",
			Tag:             "gorchestra-test-declarative-number",
			BadgeValue:      1,
			IncludeMetadata: true,
		})
	case "declarative-minimal-number":
		return s.sendToActiveSubscriptions(ctx, notificationInput{
			Kind:       "test-declarative-minimal-number",
			Title:      "Badge test",
			Body:       "Minimal declarative numeric badge",
			Path:       "/",
			BadgeValue: 1,
		})
	case "classic-sw-number":
		return s.sendToActiveSubscriptions(ctx, notificationInput{
			Kind:    "test-classic-sw-number",
			Title:   "Badge test",
			Body:    "Classic service worker badge",
			Path:    "/",
			Tag:     "gorchestra-test-classic-sw-number",
			Classic: true,
		})
	default:
		return fmt.Errorf("unsupported notification test variant %q", variant)
	}
}

func (s *Service) SendMinimalBadgeTest(ctx context.Context) error {
	return s.sendToActiveSubscriptions(ctx, notificationInput{
		Kind:       "test-declarative-minimal-string",
		Title:      "Badge test",
		Body:       "Minimal declarative string badge",
		Path:       "/",
		BadgeValue: "1",
	})
}

func (s *Service) Debug(ctx context.Context) (DebugState, error) {
	keys, err := s.ensureKeys(ctx)
	if err != nil {
		return DebugState{}, err
	}
	subscriptions, err := s.store.ListPushSubscriptions(ctx)
	if err != nil {
		return DebugState{}, err
	}
	attempts, err := s.store.ListPushDeliveryAttempts(ctx, 20)
	if err != nil {
		return DebugState{}, err
	}

	state := DebugState{
		PublicKeyFingerprint: endpointFingerprint(keys.PublicKey),
		Subscriptions:        make([]DebugSubscription, 0, len(subscriptions)),
		RecentAttempts:       make([]DebugDeliveryAttempt, 0, len(attempts)),
	}
	for _, subscription := range subscriptions {
		state.Subscriptions = append(state.Subscriptions, DebugSubscription{
			EndpointHash: endpointFingerprint(subscription.Endpoint),
			Origin:       subscription.Origin,
			UserAgent:    subscription.UserAgent,
			LastError:    subscription.LastError,
			CreatedAt:    subscription.CreatedAt,
			UpdatedAt:    subscription.UpdatedAt,
			DisabledAt:   subscription.DisabledAt,
		})
	}
	for _, attempt := range attempts {
		state.RecentAttempts = append(state.RecentAttempts, DebugDeliveryAttempt{
			ID:             attempt.ID,
			EndpointHash:   attempt.EndpointHash,
			Origin:         attempt.Origin,
			PayloadKind:    attempt.PayloadKind,
			SessionID:      attempt.SessionID,
			EventType:      attempt.EventType,
			HTTPStatus:     attempt.HTTPStatus,
			ResponseStatus: attempt.ResponseStatus,
			Error:          attempt.Error,
			CreatedAt:      attempt.CreatedAt,
		})
	}
	return state, nil
}

func (s *Service) Start(ctx context.Context, source EventSource) {
	if source == nil {
		return
	}

	events, unsubscribe := source.SubscribeAll()
	go func() {
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				if event.Type == "agent.permission.requested" {
					go s.notifyPermissionEvent(ctx, event)
				} else if isTerminalRunEvent(event.Type) {
					go s.notifyTerminalEvent(ctx, event)
				}
			}
		}
	}()
}

func (s *Service) notifyPermissionEvent(parent context.Context, event store.Event) {
	ctx, cancel := context.WithTimeout(parent, sendTimeout)
	defer cancel()
	session, err := s.store.GetSession(ctx, event.SessionID)
	if err != nil {
		s.logf("permission notification session lookup failed: %v", err)
		return
	}
	body := "An agent is waiting for approval."
	if strings.TrimSpace(session.Title) != "" {
		body = session.Title + " is waiting for approval."
	}
	if err := s.sendToActiveSubscriptions(ctx, notificationInput{Kind: "permission", Title: "Approval needed", Body: body,
		Path: "/sessions/" + event.SessionID, Tag: "gorchestra-session-" + event.SessionID, SessionID: event.SessionID,
		EventType: event.Type, Status: string(event.Status), Seq: event.Seq, Classic: true}); err != nil {
		s.logf("permission notification send failed: %v", err)
	}
}

func (s *Service) notifyTerminalEvent(parent context.Context, event store.Event) {
	ctx, cancel := context.WithTimeout(parent, sendTimeout)
	defer cancel()

	session, err := s.store.GetSession(ctx, event.SessionID)
	if err != nil {
		s.logf("notification session lookup failed: %v", err)
		return
	}
	recentEvents, err := s.store.ListRecentEvents(ctx, event.SessionID, 50)
	if err != nil {
		s.logf("notification recent events lookup failed: %v", err)
	}
	excerpt := latestAgentMessageExcerpt(recentEvents)

	if err := s.sendToActiveSubscriptions(ctx, notificationInput{
		Kind:      "terminal",
		Title:     terminalNotificationTitle(event.Type),
		Body:      terminalNotificationBody(session, excerpt),
		Path:      "/sessions/" + event.SessionID,
		Tag:       "gorchestra-session-" + event.SessionID,
		SessionID: event.SessionID,
		EventType: event.Type,
		Status:    string(event.Status),
		Seq:       event.Seq,
		Classic:   true,
	}); err != nil {
		s.logf("notification send failed: %v", err)
	}
}

func (s *Service) sendToActiveSubscriptions(ctx context.Context, input notificationInput) error {
	keys, err := s.ensureKeys(ctx)
	if err != nil {
		return err
	}

	subscriptions, err := s.store.ListPushSubscriptions(ctx)
	if err != nil {
		return err
	}
	if len(subscriptions) == 0 {
		s.logf("notification send skipped: no active push subscriptions")
		return nil
	}
	// Give the SSE client a short opportunity to acknowledge the event before
	// sending a user-visible push. Safari cannot safely discard received pushes.
	grace := s.foregroundGrace
	if isTerminalRunEvent(input.EventType) {
		grace *= 2
	}
	if input.SessionID != "" && input.Seq > 0 && grace > 0 {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	s.logf("notification send started: kind=%s subscriptions=%d", input.Kind, len(subscriptions))

	var errs []error
	attentionRecorded := false
	for _, subscription := range subscriptions {
		if isTerminalRunEvent(input.EventType) && s.completionSeen(input.SessionID, input.Seq, time.Now()) {
			break
		}
		if input.SessionID != "" && input.Seq > 0 && s.acknowledged(subscription.Endpoint, input.SessionID, input.Seq, time.Now()) {
			s.logf("notification send skipped: foreground acknowledgement endpoint=%s session_id=%s seq=%d", endpointFingerprint(subscription.Endpoint), input.SessionID, input.Seq)
			continue
		}
		payload, err := json.Marshal(newNotificationPayload(input, subscription))
		if err != nil {
			errs = append(errs, fmt.Errorf("build push payload for %s: %w", subscription.Endpoint, err))
			continue
		}
		response, err := s.sender.Send(ctx, keys, subscription, payload)
		if err != nil {
			s.logf("notification send transport failed: endpoint=%s error=%v", endpointFingerprint(subscription.Endpoint), err)
			s.recordDeliveryAttempt(ctx, subscription, input, 0, "", err.Error())
			errs = append(errs, fmt.Errorf("%s: %w", subscription.Endpoint, err))
			continue
		}
		if response == nil {
			s.logf("notification send completed: endpoint=%s response=nil", endpointFingerprint(subscription.Endpoint))
			s.recordDeliveryAttempt(ctx, subscription, input, 0, "nil", "")
			continue
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		s.recordDeliveryAttempt(ctx, subscription, input, response.StatusCode, response.Status, "")
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			s.logf("notification send accepted: endpoint=%s status=%s", endpointFingerprint(subscription.Endpoint), response.Status)
			if !attentionRecorded && input.SessionID != "" && input.Seq > 0 {
				attentionRecorded = true
				if err := s.store.MarkNotificationAttention(ctx, store.MarkNotificationAttentionParams{
					SessionID: input.SessionID,
					Seq:       input.Seq,
					EventType: input.EventType,
				}); err != nil {
					s.logf("notification attention record failed: session_id=%s seq=%d error=%v", input.SessionID, input.Seq, err)
				}
			}
			continue
		}
		statusText := response.Status
		if response.StatusCode == http.StatusGone || response.StatusCode == http.StatusNotFound {
			if err := s.store.DisablePushSubscription(ctx, store.DisablePushSubscriptionParams{
				Endpoint:  subscription.Endpoint,
				LastError: statusText,
			}); err != nil {
				errs = append(errs, fmt.Errorf("disable stale subscription %s: %w", subscription.Endpoint, err))
			}
			s.logf("notification subscription disabled: endpoint=%s status=%s", endpointFingerprint(subscription.Endpoint), statusText)
			continue
		}
		s.logf("notification send rejected: endpoint=%s status=%s", endpointFingerprint(subscription.Endpoint), statusText)
		errs = append(errs, fmt.Errorf("%s: push service returned %s", subscription.Endpoint, statusText))
	}

	return errors.Join(errs...)
}

func (s *Service) recordDeliveryAttempt(ctx context.Context, subscription store.PushSubscription, input notificationInput, httpStatus int, responseStatus string, errorMessage string) {
	if _, err := s.store.RecordPushDeliveryAttempt(ctx, store.RecordPushDeliveryAttemptParams{
		EndpointHash:   endpointFingerprint(subscription.Endpoint),
		Origin:         subscription.Origin,
		PayloadKind:    input.Kind,
		SessionID:      input.SessionID,
		EventType:      input.EventType,
		HTTPStatus:     httpStatus,
		ResponseStatus: responseStatus,
		Error:          errorMessage,
	}); err != nil {
		s.logf("notification delivery attempt record failed: endpoint=%s error=%v", endpointFingerprint(subscription.Endpoint), err)
	}
}

func (s *Service) ensureKeys(ctx context.Context) (store.NotificationKeys, error) {
	keys, err := s.store.GetNotificationKeys(ctx)
	if err == nil {
		return keys, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.NotificationKeys{}, err
	}

	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return store.NotificationKeys{}, fmt.Errorf("generate vapid keys: %w", err)
	}

	return s.store.SetNotificationKeys(ctx, store.SetNotificationKeysParams{
		PublicKey:  publicKey,
		PrivateKey: privateKey,
	})
}

func (s *Service) logf(format string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Printf(format, args...)
}

type notificationInput struct {
	Kind            string
	Title           string
	Body            string
	Path            string
	Tag             string
	SessionID       string
	EventType       string
	Status          string
	Seq             int64
	BadgeValue      any
	Classic         bool
	IncludeMetadata bool
}

type notificationPayload struct {
	WebPush      int                      `json:"web_push,omitempty"`
	Notification *declarativeNotification `json:"notification,omitempty"`
	Title        string                   `json:"title,omitempty"`
	Body         string                   `json:"body,omitempty"`
	URL          string                   `json:"url,omitempty"`
	Tag          string                   `json:"tag,omitempty"`
	SessionID    string                   `json:"session_id,omitempty"`
	EventType    string                   `json:"event_type,omitempty"`
	Status       string                   `json:"status,omitempty"`
	Kind         string                   `json:"kind,omitempty"`
	Seq          int64                    `json:"seq,omitempty"`
}

type declarativeNotification struct {
	Title    string            `json:"title"`
	Body     string            `json:"body,omitempty"`
	Navigate string            `json:"navigate,omitempty"`
	Tag      string            `json:"tag,omitempty"`
	AppBadge any               `json:"app_badge,omitempty"`
	Data     *notificationData `json:"data,omitempty"`
}

type notificationData struct {
	URL       string `json:"url,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	EventType string `json:"event_type,omitempty"`
	Status    string `json:"status,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Seq       int64  `json:"seq,omitempty"`
}

func newNotificationPayload(input notificationInput, subscription store.PushSubscription) notificationPayload {
	path := input.Path
	if path == "" {
		path = "/"
	}
	if input.Seq > 0 && input.SessionID != "" {
		path = pathWithNotificationSeq(path, input.Seq)
	}
	navigate := absoluteSubscriptionURL(subscription, path)
	if input.Classic {
		return notificationPayload{
			Title:     input.Title,
			Body:      input.Body,
			URL:       navigate,
			Tag:       input.Tag,
			SessionID: input.SessionID,
			EventType: input.EventType,
			Status:    input.Status,
			Kind:      input.Kind,
			Seq:       input.Seq,
		}
	}
	badgeValue := input.BadgeValue
	if badgeValue == nil {
		badgeValue = "1"
	}
	notification := &declarativeNotification{
		Title:    input.Title,
		Body:     input.Body,
		Navigate: navigate,
		Tag:      input.Tag,
		AppBadge: badgeValue,
	}
	if input.IncludeMetadata {
		notification.Data = &notificationData{
			URL:       path,
			SessionID: input.SessionID,
			EventType: input.EventType,
			Status:    input.Status,
			Kind:      input.Kind,
			Seq:       input.Seq,
		}
	}
	return notificationPayload{
		WebPush:      8030,
		Notification: notification,
	}
}

func absoluteSubscriptionURL(subscription store.PushSubscription, path string) string {
	origin := strings.TrimSpace(subscription.Origin)
	if origin == "" {
		return path
	}
	parsedOrigin, err := url.Parse(origin)
	if err != nil || parsedOrigin.Scheme == "" || parsedOrigin.Host == "" {
		return path
	}
	parsedOrigin.Path = ""
	parsedOrigin.RawPath = ""
	parsedOrigin.RawQuery = ""
	parsedOrigin.Fragment = ""

	if strings.TrimSpace(path) == "" {
		path = "/"
	}
	parsedPath, err := url.Parse(path)
	if err == nil && parsedPath.IsAbs() {
		return parsedPath.String()
	}
	if err == nil {
		if parsedPath.Path == "" {
			parsedPath.Path = "/"
		}
		if !strings.HasPrefix(parsedPath.Path, "/") {
			parsedPath.Path = "/" + parsedPath.Path
		}
		parsedOrigin.Path = parsedPath.Path
		parsedOrigin.RawQuery = parsedPath.RawQuery
		return parsedOrigin.String()
	}
	parsedOrigin.Path = path
	return parsedOrigin.String()
}

func pathWithNotificationSeq(path string, seq int64) string {
	parsed, err := url.Parse(path)
	if err != nil {
		return path
	}
	values := parsed.Query()
	values.Set("notification_seq", fmt.Sprintf("%d", seq))
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

type webPushSender struct {
	subscriber string
}

func (s webPushSender) Send(ctx context.Context, keys store.NotificationKeys, subscription store.PushSubscription, payload []byte) (*http.Response, error) {
	declarative := isDeclarativePayload(payload)
	return webpush.SendNotificationWithContext(
		ctx,
		payload,
		&webpush.Subscription{
			Endpoint: subscription.Endpoint,
			Keys: webpush.Keys{
				P256dh: subscription.P256DH,
				Auth:   subscription.Auth,
			},
		},
		&webpush.Options{
			HTTPClient:      webPushHTTPClient{declarative: declarative},
			Subscriber:      s.subscriber,
			VAPIDPublicKey:  keys.PublicKey,
			VAPIDPrivateKey: keys.PrivateKey,
			TTL:             60 * 60 * 24,
			Urgency:         webpush.UrgencyHigh,
		},
	)
}

func isDeclarativePayload(payload []byte) bool {
	return bytes.Contains(payload, []byte(`"web_push":8030`))
}

type webPushHTTPClient struct {
	base        webpush.HTTPClient
	declarative bool
}

func (c webPushHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if c.declarative {
		req.Header.Set("Content-Type", declarativeNotificationContentType)
	}
	if c.base != nil {
		return c.base.Do(req)
	}
	return http.DefaultClient.Do(req)
}

func isTerminalRunEvent(eventType string) bool {
	return eventType == "agent.run.completed" || eventType == "agent.run.failed" || eventType == "agent.run.cancelled"
}

func sessionNotificationName(session store.Session) string {
	title := singleLineText(session.Title)
	if title == "" {
		return "Untitled session"
	}
	return title
}

func terminalNotificationTitle(eventType string) string {
	switch eventType {
	case "agent.run.completed":
		return "Completed"
	case "agent.run.cancelled":
		return "Cancelled"
	default:
		return "Failed"
	}
}

func terminalNotificationBody(session store.Session, excerpt string) string {
	sessionName := sessionNotificationName(session)
	if excerpt == "" {
		return sessionName
	}
	return sessionName + ": " + excerpt
}

func latestAgentMessageExcerpt(events []store.Event) string {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != "agent.message.completed" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		text, _ := payload["text"].(string)
		if excerpt := notificationExcerpt(text); excerpt != "" {
			return excerpt
		}
	}
	return ""
}

func notificationExcerpt(text string) string {
	fields := strings.Fields(singleLineText(text))
	if len(fields) == 0 {
		return ""
	}
	if len(fields) > 18 {
		fields = fields[:18]
	}
	excerpt := strings.Join(fields, " ")
	if len(excerpt) > 160 {
		excerpt = excerpt[:160]
	}
	if len(fields) == 18 {
		excerpt += "..."
	}
	return excerpt
}

func singleLineText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func endpointFingerprint(endpoint string) string {
	if endpoint == "" {
		return "empty"
	}
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])[:12]
}
