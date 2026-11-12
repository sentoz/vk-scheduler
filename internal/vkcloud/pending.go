package vkcloud

import (
	"sync"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
)

// PendingOperation records a process-local attempt with an unconfirmed outcome.
type PendingOperation struct {
	Attempt   uint64
	Resource  config.Resource
	Desired   string
	CreatedAt time.Time
}

type pendingOperations struct {
	mu      sync.Mutex
	next    uint64
	entries map[string]PendingOperation
}

func (p *pendingOperations) Pending(key string) (PendingOperation, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record, exists := p.entries[key]
	return record, exists
}

func (p *pendingOperations) Records() []PendingOperation {
	p.mu.Lock()
	defer p.mu.Unlock()
	records := make([]PendingOperation, 0, len(p.entries))
	for _, record := range p.entries {
		records = append(records, record)
	}
	return records
}

func (p *pendingOperations) Begin(target config.Resource, desired string) (PendingOperation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if desired != "running" && desired != "stopped" {
		return PendingOperation{}, ErrUnexpectedResponse
	}
	if _, exists := p.entries[target.Key()]; exists {
		return PendingOperation{}, ErrUnconfirmedOperation
	}
	if p.next == ^uint64(0) {
		return PendingOperation{}, ErrAttemptExhausted
	}
	p.next++
	record := PendingOperation{Attempt: p.next, Resource: target, Desired: desired, CreatedAt: time.Now().UTC()}
	if p.entries == nil {
		p.entries = make(map[string]PendingOperation)
	}
	p.entries[target.Key()] = record
	return record, nil
}

func (p *pendingOperations) Confirm(record PendingOperation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, exists := p.entries[record.Resource.Key()]
	if !exists {
		return nil
	}
	if current.Attempt != record.Attempt {
		return ErrUnconfirmedOperation
	}
	delete(p.entries, record.Resource.Key())
	return nil
}
