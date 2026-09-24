package push

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"configcenter/internal/domain"
)

// Conn is one live client: either a suspended long-poll or a WebSocket.
type Conn struct {
	ID          string
	InstanceID  string // stable identity supplied by the client (defaults to IP)
	TenantID    string
	NamespaceID string
	GroupID     string // may be empty when subscribing at namespace scope
	Env         string
	IP          string
	WebSocket   bool

	wait     chan struct{}   // closed by dispatch to release a long-poll
	wakeOnce sync.Once       // guards the close
	notify   chan []byte     // outbound websocket frames
	socket   *websocket.Conn // closes when the client disconnects
	done     chan struct{}   // closed when the websocket read loop ends

	CreatedAt time.Time
}

func newConn(c ConnMeta) *Conn {
	id := c.InstanceID
	if id == "" {
		id = c.IP
	}
	return &Conn{
		ID: id, InstanceID: id,
		TenantID: c.TenantID, NamespaceID: c.NamespaceID, GroupID: c.GroupID,
		Env: c.Env, IP: c.IP, WebSocket: c.Socket != nil,
		wait: make(chan struct{}), notify: make(chan []byte, 32),
		socket: c.Socket, done: make(chan struct{}), CreatedAt: time.Now(),
	}
}

// Done returns a channel closed when the websocket peer is gone.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Enqueue queues an outbound frame for a websocket connection. It is the only
// supported way to send frames, keeping writePump the single socket writer.
func (c *Conn) Enqueue(frame []byte) bool {
	if c.socket == nil {
		return false
	}
	select {
	case c.notify <- frame:
		return true
	default:
		return false // slow consumer; it will reconcile via snapshot on reconnect
	}
}

// ConnMeta describes a connection being registered.
type ConnMeta struct {
	InstanceID  string
	TenantID    string
	NamespaceID string
	GroupID     string
	Env         string
	IP          string
	Socket      *websocket.Conn
}

// Decider is consulted by the hub for every matching event. Returning true
// means this connection is entitled to wake up / receive the push. The
// service layer implements the gray gate here.
type Decider interface {
	ShouldDeliver(ev domain.PushEvent, c *Conn) bool
}

// Tracker records which instances actually received a gray release.
type Tracker interface {
	// Mark records a delivery and reports whether it is the first for the pair.
	Mark(releaseID, instanceID string) bool
	Count(releaseID string) int
	DeliveredTo(releaseID, instanceID string) bool
}

// Hub is the in-node live connection registry and dispatcher.
type Hub struct {
	mu       sync.Mutex
	conns    map[string]*Conn
	lastPush map[string]domain.NamespaceStats // key: tenant/namespace
	tracker  Tracker
}

// NewHub builds the hub. A nil tracker falls back to the in-memory one.
func NewHub(tracker Tracker) *Hub {
	if tracker == nil {
		tracker = NewMemTracker()
	}
	return &Hub{
		conns:    map[string]*Conn{},
		lastPush: map[string]domain.NamespaceStats{},
		tracker:  tracker,
	}
}

// Register adds a connection and returns its handle.
func (h *Hub) Register(meta ConnMeta) *Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := newConn(meta)
	h.conns[c.ID] = c
	if c.socket != nil {
		go c.writePump()
		go c.readPump(h)
	}
	return c
}

// Unregister removes a connection (long-poll ended, websocket closed).
func (h *Hub) Unregister(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.conns[c.ID]; ok && cur == c {
		delete(h.conns, c.ID)
	}
}

// Wait blocks until the connection is woken by an event or the timeout
// elapses. It returns true when woken.
func (h *Hub) Wait(c *Conn, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c.wait:
		return true
	case <-timer.C:
		return false
	case <-c.done:
		return true
	}
}

// Dispatch applies an inbound event to every matching connection. Connections
// are matched on tenant + namespace; the decider enforces gray selection.
// Returns the number of connections woken/notified.
func (h *Hub) Dispatch(ev domain.PushEvent, decider Decider) int {
	h.mu.Lock()
	targets := make([]*Conn, 0)
	for _, c := range h.conns {
		if c.TenantID != ev.TenantID || c.NamespaceID != ev.NamespaceID {
			continue
		}
		if decider != nil && !decider.ShouldDeliver(ev, c) {
			continue
		}
		targets = append(targets, c)
	}
	h.recordPushLocked(ev)
	h.mu.Unlock()

	woken := 0
	frame := mustFrame(ev)
	for _, c := range targets {
		if ev.ReleaseID != "" && ev.Type != domain.EventRollback {
			h.tracker.Mark(ev.ReleaseID, c.InstanceID)
		}
		if c.socket != nil {
			if c.Enqueue(frame) {
				woken++
			}
			continue
		}
		c.wakeOnce.Do(func() { close(c.wait) })
		woken++
	}
	return woken
}

// Tracker exposes the delivery tracker.
func (h *Hub) Tracker() Tracker { return h.tracker }

func (h *Hub) recordPushLocked(ev domain.PushEvent) {
	key := ev.TenantID + "/" + ev.NamespaceID
	now := time.Now()
	st := h.lastPush[key]
	st.NamespaceID = ev.NamespaceID
	st.LastPushAt = &now
	st.LastPushEnv = ev.Env
	if ev.Version > st.LastVersion {
		st.LastVersion = ev.Version
	}
	h.lastPush[key] = st
}

// ListInstances returns every connected instance in a namespace, once by
// instance id (a long-polling and a websocket connection from the same
// instance count as one).
func (h *Hub) ListInstances(tenantID, namespaceID string) []InstanceView {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]InstanceView{}
	for _, c := range h.conns {
		if c.TenantID != tenantID || c.NamespaceID != namespaceID {
			continue
		}
		if _, ok := seen[c.InstanceID]; ok {
			continue
		}
		seen[c.InstanceID] = InstanceView{
			InstanceID: c.InstanceID, IP: c.IP,
			WebSocket: c.WebSocket, ConnectedAt: c.CreatedAt,
		}
	}
	out := make([]InstanceView, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	return out
}

// InstanceView describes one connected client instance.
type InstanceView struct {
	InstanceID  string    `json:"instance_id"`
	IP          string    `json:"ip"`
	WebSocket   bool      `json:"websocket"`
	ConnectedAt time.Time `json:"connected_at"`
}

// Stats returns per-namespace connection counts and recent push info.
func (h *Hub) Stats(tenantID string) []domain.NamespaceStats {
	h.mu.Lock()
	defer h.mu.Unlock()
	counts := map[string]int{}
	for _, c := range h.conns {
		if c.TenantID == tenantID {
			counts[c.NamespaceID]++
		}
	}
	out := make([]domain.NamespaceStats, 0, len(counts))
	for ns, n := range counts {
		st := h.lastPush[tenantID+"/"+ns]
		st.NamespaceID = ns
		st.Connections = n
		out = append(out, st)
	}
	return out
}

// ReleaseStats returns delivered/total counts for the gray panel.
func (h *Hub) ReleaseStats(tenantID, namespaceID, releaseID string) (delivered, total int) {
	return h.tracker.Count(releaseID), len(h.ListInstances(tenantID, namespaceID))
}

func mustFrame(ev domain.PushEvent) []byte {
	b, err := json.Marshal(ev)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

var connCounter uint64

func newConnID() string {
	n := atomic.AddUint64(&connCounter, 1)
	return "conn-" + itoa(n)
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
