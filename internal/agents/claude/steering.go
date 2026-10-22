package claude

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"github.com/threave-io/threave/internal/agents"
)

type claudeSteerRequest struct {
	ctx        context.Context
	id         string
	content    any
	result     chan error
	stopExpiry func() bool
}

type claudeSteerUpdate struct {
	request *claudeSteerRequest
	err     error
}

// Only the stream loop owns pending deliveries and deferred results. Writes run
// separately so a large attachment cannot block replay acknowledgements or tools.
type claudeSteering struct {
	requests       chan *claudeSteerRequest
	updates        chan claudeSteerUpdate
	done           chan struct{}
	pending        map[string]*claudeSteerRequest
	unregister     func()
	writers        sync.WaitGroup
	deferredResult *streamEvent
}

func newClaudeSteering() *claudeSteering {
	return &claudeSteering{requests: make(chan *claudeSteerRequest), updates: make(chan claudeSteerUpdate), done: make(chan struct{}), pending: make(map[string]*claudeSteerRequest)}
}

func (s *claudeSteering) close() {
	if s.unregister != nil {
		s.unregister()
	}
	for _, request := range s.pending {
		request.stopExpiry()
	}
	close(s.done)
}

func (r *streamRun) registerSteering(ctx context.Context) error {
	s := r.steering
	if r.steeringBroker == nil || s.unregister != nil {
		return nil
	}
	unregister, err := r.steeringBroker.RegisterSteering(ctx, r.sessionID, r.runID, func(deliveryCtx context.Context, input agents.SteeringInput) error {
		content, err := userMessageContent(messageWithSkills(input.Message, input.Skills), input.Attachments)
		if err != nil {
			return err
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		id[6] = (id[6] & 15) | 64
		id[8] = (id[8] & 63) | 128
		request := &claudeSteerRequest{ctx: deliveryCtx, id: fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), content: content, result: make(chan error, 1)}
		select {
		case s.requests <- request:
		case <-deliveryCtx.Done():
			return deliveryCtx.Err()
		case <-s.done:
			return errors.New("Claude run has ended")
		}
		select {
		case err := <-request.result:
			return err
		case <-deliveryCtx.Done():
			return deliveryCtx.Err()
		case <-s.done:
			// A replay can immediately precede the final result.
			select {
			case err := <-request.result:
				return err
			default:
			}
			return errors.New("Claude run ended before delivery was confirmed")
		}
	})
	if err != nil {
		return err
	}
	s.unregister = unregister
	return nil
}

func (r *streamRun) startSteeringWrite(ctx context.Context, request *claudeSteerRequest) {
	if err := request.ctx.Err(); err != nil {
		request.result <- err
		return
	}
	s := r.steering
	s.pending[request.id] = request
	request.stopExpiry = context.AfterFunc(request.ctx, func() {
		select {
		case s.updates <- claudeSteerUpdate{request, request.ctx.Err()}:
		case <-ctx.Done():
		}
	})
	s.writers.Add(1)
	go func() {
		defer s.writers.Done()
		// Closing a timed-out blocked write prevents a leaked writer or a later
		// silent delivery. It also prevents further writes to this process.
		stop := context.AfterFunc(request.ctx, func() { _ = r.stdin.Close() })
		err := r.writeJSON(map[string]any{
			"type": "user", "uuid": request.id, "session_id": "",
			"message":            map[string]any{"role": "user", "content": request.content},
			"parent_tool_use_id": nil, "origin": map[string]any{"kind": "human"},
		})
		stop()
		if err != nil {
			select {
			case s.updates <- claudeSteerUpdate{request, err}:
			case <-ctx.Done():
			}
		}
	}()
}

func (r *streamRun) finishSteeringUpdate(ctx context.Context, update claudeSteerUpdate) error {
	s := r.steering
	if s.pending[update.request.id] != update.request {
		return nil
	}
	delete(s.pending, update.request.id)
	update.request.stopExpiry()
	update.request.result <- update.err
	if len(s.pending) == 0 && s.deferredResult != nil {
		result := s.deferredResult
		s.deferredResult = nil
		r.normalizer.deferResult = false
		for _, event := range r.normalizer.normalize(result) {
			if err := r.emit(ctx, event.Event); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *streamRun) handleSteeringEvent(ctx context.Context, event *streamEvent) error {
	s := r.steering
	if s == nil {
		return nil
	}
	if event.Type == "system" && event.Subtype == "init" {
		if err := r.registerSteering(ctx); err != nil {
			return err
		}
	}
	if event.Type == "user" && event.IsReplay && event.ParentToolUseID == "" {
		if request := s.pending[event.UUID]; request != nil {
			delete(s.pending, event.UUID)
			request.stopExpiry()
			s.deferredResult = nil
			request.result <- nil
		}
	}
	if event.Type == "result" && stringAt(event.Origin, "kind") != "task-notification" {
		r.normalizer.deferResult = len(s.pending) > 0 && !event.IsError
		if r.normalizer.deferResult {
			s.deferredResult = event
		}
	}
	return nil
}
