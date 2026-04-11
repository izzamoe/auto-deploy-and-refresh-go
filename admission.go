package main

import (
	"errors"
	"fmt"
)

type AdmissionOutcome string

const (
	OutcomeUnauthorized AdmissionOutcome = "unauthorized"
	OutcomeBadRequest   AdmissionOutcome = "bad_request"
	OutcomeDuplicate    AdmissionOutcome = "duplicate"
	OutcomeQueued       AdmissionOutcome = "queued"
	OutcomeOK           AdmissionOutcome = "ok"
	OutcomeError        AdmissionOutcome = "error"
)

type AdmissionResult struct {
	Outcome AdmissionOutcome
	App     *App
	Tag     string
	Error   error
}

type AdmissionService struct {
	store *AppStore
	queue *DeployQueue
}

func NewAdmissionService(store *AppStore, queue *DeployQueue) *AdmissionService {
	return &AdmissionService{store: store, queue: queue}
}

// Admit resolves the bearer token to an app, validates the tag, and enqueues
// the deploy job. Returns a typed outcome for the caller to act on.
func (s *AdmissionService) Admit(bearerToken, tag string) AdmissionResult {
	app, err := s.resolveApp(bearerToken)
	if err != nil {
		return AdmissionResult{Outcome: OutcomeError, Error: err}
	}
	if app == nil {
		return AdmissionResult{Outcome: OutcomeUnauthorized}
	}

	if tag == "" {
		return AdmissionResult{Outcome: OutcomeBadRequest, App: app}
	}

	dup, err := s.queue.IsDuplicate(app.ID, tag)
	if err != nil {
		return AdmissionResult{Outcome: OutcomeError, App: app, Tag: tag, Error: err}
	}
	if dup {
		return AdmissionResult{Outcome: OutcomeDuplicate, App: app, Tag: tag}
	}

	if err := s.queue.Enqueue(app.ID, tag); err != nil {
		if errors.Is(err, ErrDuplicate) {
			return AdmissionResult{Outcome: OutcomeDuplicate, App: app, Tag: tag}
		}
		if errors.Is(err, ErrQueueFull) {
			return AdmissionResult{Outcome: OutcomeError, App: app, Tag: tag, Error: fmt.Errorf("queue full")}
		}
		return AdmissionResult{Outcome: OutcomeError, App: app, Tag: tag, Error: err}
	}

	return AdmissionResult{Outcome: OutcomeQueued, App: app, Tag: tag}
}

// AdmitImmediate resolves the bearer token and validates the tag but does NOT
// enqueue. Used by the webhook handler for the "try immediate deploy" path.
func (s *AdmissionService) AdmitImmediate(bearerToken, tag string) (*App, AdmissionResult) {
	app, err := s.resolveApp(bearerToken)
	if err != nil {
		return nil, AdmissionResult{Outcome: OutcomeError, Error: err}
	}
	if app == nil {
		return nil, AdmissionResult{Outcome: OutcomeUnauthorized}
	}

	if tag == "" {
		return app, AdmissionResult{Outcome: OutcomeBadRequest, App: app}
	}

	return app, AdmissionResult{Outcome: OutcomeOK, App: app, Tag: tag}
}

func (s *AdmissionService) resolveApp(bearerToken string) (*App, error) {
	hash := HashSecret(bearerToken)
	return s.store.GetBySecretHash(hash)
}
