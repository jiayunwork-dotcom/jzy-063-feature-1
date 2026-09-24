package push

import (
	"context"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
)

// MemTracker tracks deliveries in-process.
type MemTracker struct {
	mu      sync.Mutex
	markers map[string]map[string]struct{} // releaseID -> instance set
}

func NewMemTracker() *MemTracker {
	return &MemTracker{markers: map[string]map[string]struct{}{}}
}

func (t *MemTracker) Mark(releaseID, instanceID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	set, ok := t.markers[releaseID]
	if !ok {
		set = map[string]struct{}{}
		t.markers[releaseID] = set
	}
	if _, exists := set[instanceID]; exists {
		return false
	}
	set[instanceID] = struct{}{}
	return true
}

func (t *MemTracker) Count(releaseID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.markers[releaseID])
}

func (t *MemTracker) DeliveredTo(releaseID, instanceID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.markers[releaseID][instanceID]
	return ok
}

// RedisTracker mirrors the in-memory set into Redis so delivery counts stay
// correct across multiple backend nodes.
type RedisTracker struct {
	client redis.UniversalClient
	ttl    time.Duration
}

func NewRedisTracker(client redis.UniversalClient) *RedisTracker {
	return &RedisTracker{client: client, ttl: 24 * time.Hour}
}

func (t *RedisTracker) key(releaseID string) string {
	return "configcenter:delivered:" + releaseID
}

func (t *RedisTracker) Mark(ctx context.Context, releaseID, instanceID string) bool {
	// SADD returns 1 when the member is newly added.
	n, err := t.client.SAdd(ctx, t.key(releaseID), instanceID).Result()
	if err != nil {
		return false
	}
	_ = t.client.Expire(ctx, t.key(releaseID), t.ttl).Err()
	return n == 1
}

func (t *RedisTracker) Count(ctx context.Context, releaseID string) int {
	n, err := t.client.SCard(ctx, t.key(releaseID)).Result()
	if err != nil {
		return 0
	}
	return int(n)
}

func (t *RedisTracker) DeliveredTo(ctx context.Context, releaseID, instanceID string) bool {
	ok, err := t.client.SIsMember(ctx, t.key(releaseID), instanceID).Result()
	return err == nil && ok
}

// Compile-time argument adapters: the Tracker interface is synchronous and
// context-free. Redis calls are fast and background-contexted; callers that
// need cancellation should batch via the Hub's MemTracker in tests.
func (t *RedisTracker) MarkSync(releaseID, instanceID string) bool {
	return t.Mark(context.Background(), releaseID, instanceID)
}

func (t *RedisTracker) CountSync(releaseID string) int {
	return t.Count(context.Background(), releaseID)
}

func (t *RedisTracker) DeliveredToSync(releaseID, instanceID string) bool {
	return t.DeliveredTo(context.Background(), releaseID, instanceID)
}

// trackerAdapter makes the context-bound Redis tracker satisfy Tracker.
type redisSyncTracker struct{ r *RedisTracker }

func (t redisSyncTracker) Mark(releaseID, instanceID string) bool {
	return t.r.MarkSync(releaseID, instanceID)
}
func (t redisSyncTracker) Count(releaseID string) int { return t.r.CountSync(releaseID) }
func (t redisSyncTracker) DeliveredTo(releaseID, instanceID string) bool {
	return t.r.DeliveredToSync(releaseID, instanceID)
}

// NewRedisTrackerSync returns the synchronous Tracker interface.
func NewRedisTrackerSync(client redis.UniversalClient) Tracker {
	return redisSyncTracker{NewRedisTracker(client)}
}
