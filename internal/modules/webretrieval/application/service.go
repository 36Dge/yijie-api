package application

import (
	"context"
	"sync"
	"time"

	d "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	"github.com/google/uuid"
)

type running struct {
	binding uuid.UUID
	cancel  context.CancelFunc
}
type cached struct {
	binding uuid.UUID
	body    []byte
	until   time.Time
}
type Service struct {
	Repository d.Repository
	executor   d.Executor
	mu         sync.Mutex
	running    map[uuid.UUID]running
	results    map[uuid.UUID]cached
}

func New(repository d.Repository, executor d.Executor) *Service {
	return &Service{Repository: repository, executor: executor, running: make(map[uuid.UUID]running), results: make(map[uuid.UUID]cached)}
}
func (s *Service) cached(o d.Operation) d.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := d.Result{Operation: o}
	for id, entry := range s.results {
		if time.Now().After(entry.until) {
			delete(s.results, id)
		}
	}
	if entry, ok := s.results[o.ID]; ok && entry.binding == o.Binding.ID && o.State == "completed" {
		result.Body = append([]byte(nil), entry.body...)
	}
	return result
}
func (s *Service) Get(ctx context.Context, b d.Binding, id uuid.UUID) (d.Result, error) {
	o, err := s.Repository.Get(ctx, b, id)
	if err != nil {
		return d.Result{}, err
	}
	return s.cached(o), nil
}
func (s *Service) Execute(ctx context.Context, b d.Binding, request d.Request) (d.Result, error) {
	if s.executor == nil {
		return d.Result{}, d.AdmissionFailure{Cause: d.Unavailable}
	}
	o, fresh, err := s.Repository.Admit(ctx, b, request)
	if err != nil {
		return d.Result{}, d.AdmissionFailure{Cause: err}
	}
	if !fresh {
		return s.cached(o), nil
	}
	execution, cancel := context.WithDeadline(ctx, o.Deadline)
	s.mu.Lock()
	s.running[o.ID] = running{binding: b.ID, cancel: cancel}
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); delete(s.running, o.ID); s.mu.Unlock() }()
	body, executionErr := s.executor.Execute(execution, o, request)
	// Settle after caller cancellation with a bounded service-owned context.
	settlement, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer stop()
	current, err := s.Repository.Get(settlement, b, o.ID)
	if err != nil {
		return d.Result{}, err
	}
	if executionErr != nil || current.State != "completed" {
		if current.State == "ready" {
			current, err = s.Repository.Cancel(settlement, b, o.ID)
		}
		if current.State == "dispatch_committed" {
			err = s.Repository.Finish(settlement, d.Receipt{Operation: current}, "unknown", "may_have_dispatched")
			if err == nil {
				current.State = "unknown"
				current.Dispatch = "may_have_dispatched"
			}
		}
		if err != nil {
			return d.Result{}, err
		}
		if executionErr == nil && len(body) > 0 && len(body) <= 65536 {
			return d.Result{Operation: current, Body: append([]byte(nil), body...)}, nil
		}
		failure := d.Code("outcome_unknown")
		if current.Dispatch == "not_dispatched" {
			failure = d.Unavailable
		}
		if ctx.Err() == context.Canceled {
			failure = "cancelled"
		} else if execution.Err() == context.DeadlineExceeded {
			failure = "timeout"
		}
		return d.Result{Operation: current, Failure: failure}, nil
	}
	if len(body) > 0 && len(body) <= 65536 {
		s.mu.Lock()
		if len(s.results) >= 64 {
			for id := range s.results {
				delete(s.results, id)
				break
			}
		}
		s.results[o.ID] = cached{binding: b.ID, body: append([]byte(nil), body...), until: time.Now().Add(5 * time.Minute)}
		s.mu.Unlock()
	}
	return s.cached(current), nil
}
func (s *Service) Cancel(ctx context.Context, b d.Binding, id uuid.UUID) (d.Operation, error) {
	o, err := s.Repository.Cancel(ctx, b, id)
	if err != nil {
		return o, err
	}
	s.mu.Lock()
	if call, ok := s.running[id]; ok && call.binding == b.ID {
		call.cancel()
	}
	s.mu.Unlock()
	return o, nil
}
func (s *Service) Revoke(ctx context.Context, b d.Binding) error {
	if err := s.Repository.Revoke(ctx, b); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, call := range s.running {
		if call.binding == b.ID {
			call.cancel()
		}
	}
	for id, result := range s.results {
		if result.binding == b.ID {
			delete(s.results, id)
		}
	}
	return nil
}
