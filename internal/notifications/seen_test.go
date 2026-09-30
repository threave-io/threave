package notifications

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/threave-io/threave/internal/store"
)

func TestSeenSuppressesAllDevicesForExactTerminalEvent(t *testing.T) {
	for _, kind := range []string{"agent.run.completed", "agent.run.failed", "agent.run.cancelled"} {
		t.Run(kind, func(t *testing.T) {
			db := &memoryStore{
				keys:          store.NotificationKeys{PublicKey: "public", PrivateKey: "private"},
				session:       store.Session{ID: "s"},
				subscriptions: []store.PushSubscription{{Endpoint: "phone"}, {Endpoint: "tablet"}},
				recentEvents:  []store.Event{{SessionID: "s", Seq: 8, Type: kind}},
			}
			sender := &recordingSender{}
			service := NewService(db, WithSender(sender))
			service.foregroundGrace = 0
			if err := service.Seen(context.Background(), "s", 8); err != nil {
				t.Fatal(err)
			}
			service.notifyTerminalEvent(context.Background(), db.recentEvents[0])
			if len(sender.payloads) != 0 || len(db.attempts) != 0 || len(db.attention) != 0 {
				t.Fatal("seen completion sent notifications or recorded attention")
			}
			service.notifyTerminalEvent(context.Background(), store.Event{SessionID: "s", Seq: 9, Type: kind})
			if len(sender.payloads) != 2 {
				t.Fatal("different completion was suppressed")
			}
		})
	}
}

func TestSeenValidationWithoutSubscription(t *testing.T) {
	db := &memoryStore{recentEvents: []store.Event{{SessionID: "s", Seq: 1, Type: "agent.permission.requested"}, {SessionID: "s", Seq: 2, Type: "agent.run.completed"}}}
	service := NewService(db)
	for _, input := range []completionKey{{"", 2}, {"s", 0}, {"other", 2}, {"s", 1}, {"s", 3}} {
		if err := service.Seen(context.Background(), input.sessionID, input.seq); !errors.Is(err, store.ErrInvalidArgument) {
			t.Fatalf("invalid input accepted: %+v, %v", input, err)
		}
	}
	if err := service.Seen(context.Background(), "s", 2); err != nil {
		t.Fatal(err)
	}
	if !service.completionSeen("s", 2, time.Now()) {
		t.Fatal("missing acknowledgment")
	}
	if service.completionSeen("s", 2, time.Now().Add(foregroundAckTTL)) {
		t.Fatal("expired acknowledgment survived")
	}
}

func TestSeenCapacityAndConcurrentAccess(t *testing.T) {
	db := &memoryStore{}
	for seq := int64(1); seq <= maxForegroundAcks+1; seq++ {
		db.recentEvents = append(db.recentEvents, store.Event{SessionID: "s", Seq: seq, Type: "agent.run.completed"})
	}
	service := NewService(db)
	for _, event := range db.recentEvents {
		if err := service.Seen(context.Background(), "s", event.Seq); err != nil {
			t.Fatal(err)
		}
	}
	if len(service.seenCompletions) != maxForegroundAcks || service.completionSeen("s", 1, time.Now()) {
		t.Fatal("capacity eviction failed")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			_ = service.Seen(context.Background(), "s", 2)
		}
	}()
	for i := 0; i < 100; i++ {
		service.completionSeen("s", 2, time.Now())
	}
	<-done
}

type seeingSender struct {
	service *Service
	calls   int
}

func (s *seeingSender) Send(ctx context.Context, _ store.NotificationKeys, _ store.PushSubscription, _ []byte) (*http.Response, error) {
	s.calls++
	return testResponse(http.StatusCreated), s.service.Seen(ctx, "s", 8)
}

func TestSeenStopsRemainingDeliveriesAndCancelledGrace(t *testing.T) {
	db := &memoryStore{keys: store.NotificationKeys{PublicKey: "public", PrivateKey: "private"},
		session: store.Session{ID: "s"}, subscriptions: []store.PushSubscription{{Endpoint: "a"}, {Endpoint: "b"}},
		recentEvents: []store.Event{{SessionID: "s", Seq: 8, Type: "agent.run.completed"}}}
	sender := &seeingSender{}
	service := NewService(db, WithSender(sender))
	sender.service = service
	service.foregroundGrace = 0
	service.notifyTerminalEvent(context.Background(), db.recentEvents[0])
	if sender.calls != 1 {
		t.Fatal("late acknowledgment did not stop remaining delivery")
	}
	service.foregroundGrace = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := service.sendToActiveSubscriptions(ctx, notificationInput{SessionID: "s", Seq: 9, EventType: "agent.run.completed"})
	if !errors.Is(err, context.Canceled) || sender.calls != 1 {
		t.Fatal("cancelled grace sent push")
	}
}
