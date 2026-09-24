// Package push owns realtime delivery: cross-node change events over Redis
// (or an in-process bus), the live connection registry, long-poll suspension
// and WebSocket fan-out, and per-release delivered-instance counting.
package push

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/go-redis/redis/v8"

	"configcenter/internal/domain"
)

// EventBus broadcasts configuration change events to every server node.
type EventBus interface {
	Publish(ctx context.Context, ev domain.PushEvent) error
	// Subscribe returns a channel of events and a cleanup function.
	Subscribe(ctx context.Context) (<-chan domain.PushEvent, func() error, error)
	Close() error
}

// MemoryBus is an in-process bus for tests and single-node deployments.
type MemoryBus struct {
	mu     sync.RWMutex
	chans  map[uint64]chan domain.PushEvent
	nextID uint64
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{chans: map[uint64]chan domain.PushEvent{}}
}

func (b *MemoryBus) Publish(_ context.Context, ev domain.PushEvent) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.chans {
		select {
		case ch <- ev:
		default:
			// Never block a publish on a slow consumer; the consumer will
			// reconcile by fetching a fresh snapshot when it next wakes.
		}
	}
	return nil
}

func (b *MemoryBus) Subscribe(_ context.Context) (<-chan domain.PushEvent, func() error, error) {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	ch := make(chan domain.PushEvent, 256)
	b.chans[id] = ch
	b.mu.Unlock()
	return ch, func() error {
		b.mu.Lock()
		delete(b.chans, id)
		b.mu.Unlock()
		return nil
	}, nil
}

func (b *MemoryBus) Close() error { return nil }

// RedisBus fans events out through a Redis Pub/Sub channel so that a change
// committed on any node wakes connections held by any node.
type RedisBus struct {
	client  redis.UniversalClient
	channel string
}

func NewRedisBus(client redis.UniversalClient) *RedisBus {
	return &RedisBus{client: client, channel: "configcenter:events"}
}

func (b *RedisBus) Publish(ctx context.Context, ev domain.PushEvent) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return b.client.Publish(ctx, b.channel, raw).Err()
}

func (b *RedisBus) Subscribe(ctx context.Context) (<-chan domain.PushEvent, func() error, error) {
	sub := b.client.Subscribe(ctx, b.channel)
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, err
	}
	rc := sub.Channel()
	out := make(chan domain.PushEvent, 256)
	go func() {
		defer close(out)
		for msg := range rc {
			var ev domain.PushEvent
			if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, func() error { return sub.Close() }, nil
}

func (b *RedisBus) Close() error { return b.client.Close() }
