package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"configcenter/internal/push"
	"configcenter/internal/service"
	"configcenter/internal/store"
)

type harness struct {
	router *gin.Engine
	svc    *service.Service
	bus    *push.MemoryBus
	hub    *push.Hub
	srv    *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("LONG_POLL_TIMEOUT_SECONDS", "2")
	st := store.NewMemory()
	bus := push.NewMemoryBus()
	hub := push.NewHub(push.NewMemTracker())
	svc := service.New(st, bus, hub)
	r := NewRouter(svc)
	srv := httptest.NewServer(r)

	// mirror the production event consumer
	go func() {
		events, unsub, _ := bus.Subscribe(context.Background())
		defer func() { _ = unsub() }()
		gate := svc.NewGate()
		for ev := range events {
			hub.Dispatch(ev, gate)
		}
	}()

	t.Cleanup(srv.Close)
	return &harness{router: r, svc: svc, bus: bus, hub: hub, srv: srv}
}

func doJSON(t *testing.T, method, url string, headers map[string]string, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func seedWorld(t *testing.T, h *harness) string {
	if code, _ := doJSON(t, "POST", h.srv.URL+"/api/tenants", nil, `{"id":"t1","name":"T1"}`); code != 201 {
		t.Fatalf("create tenant %d", code)
	}
	hdr := map[string]string{"X-Tenant-ID": "t1"}
	doJSON(t, "POST", h.srv.URL+"/api/namespaces", hdr, `{"id":"ns","name":"NS"}`)
	doJSON(t, "POST", h.srv.URL+"/api/namespaces/ns/groups", hdr, `{"id":"g","name":"G"}`)
	code, body := doJSON(t, "POST", h.srv.URL+"/api/namespaces/ns/groups/g/items", hdr,
		`{"key":"k","format":"properties"}`)
	if code != 201 {
		t.Fatalf("create item: %d %v", code, body)
	}
	return body["id"].(string)
}

func getTenant(t *testing.T, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Tenant-ID", "t1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A poll arriving with a stale version gets the current snapshot immediately.
func TestPollImmediateWhenNewer(t *testing.T) {
	h := newHarness(t)
	id := seedWorld(t, h)
	hdr := map[string]string{"X-Tenant-ID": "t1"}
	doJSON(t, "POST", h.srv.URL+"/api/items/"+id+"/values", hdr, `{"env":"dev","value":"k=v1\n"}`)

	resp := getTenant(t, h.srv.URL+"/api/poll?namespace=ns&group=g&env=dev&version=0")
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["changed"] != true {
		t.Fatalf("expected immediate change, got %v", body)
	}
}

// A poll with the current version suspends and is woken by a subsequent commit.
func TestPollWakesOnCommit(t *testing.T) {
	h := newHarness(t)
	id := seedWorld(t, h)
	hdr := map[string]string{"X-Tenant-ID": "t1"}
	doJSON(t, "POST", h.srv.URL+"/api/items/"+id+"/values", hdr, `{"env":"dev","value":"k=v1\n"}`)

	type result struct {
		body map[string]any
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		req, _ := http.NewRequest("GET", h.srv.URL+"/api/poll?namespace=ns&group=g&env=dev&version=1&instance_id=poller-1", nil)
		req.Header.Set("X-Tenant-ID", "t1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resCh <- result{body: body}
	}()

	time.Sleep(150 * time.Millisecond) // let the long-poll register
	doJSON(t, "POST", h.srv.URL+"/api/items/"+id+"/values", hdr,
		`{"env":"dev","value":"k=v2\n","expected_version":1}`)

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.body["changed"] != true {
			t.Fatalf("expected woken with change, got %v", r.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long-poll was not woken by the commit")
	}
}

// No change within the window produces a timeout response asking to reconnect.
func TestPollTimesOut(t *testing.T) {
	h := newHarness(t)
	id := seedWorld(t, h)
	hdr := map[string]string{"X-Tenant-ID": "t1"}
	doJSON(t, "POST", h.srv.URL+"/api/items/"+id+"/values", hdr, `{"env":"dev","value":"k=v1\n"}`)

	start := time.Now()
	resp := getTenant(t, h.srv.URL+"/api/poll?namespace=ns&group=g&env=dev&version=1")
	defer resp.Body.Close()
	elapsed := time.Since(start)
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["changed"] != false {
		t.Fatalf("expected timeout unchanged response, got %v", body)
	}
	// server timeout is 30s in production but 1s in this test; assert we
	// actually suspended before getting the reconnect hint.
	if elapsed < 1800*time.Millisecond {
		t.Fatalf("poll returned before suspending: %s", elapsed)
	}
	if !strings.Contains(body["message"].(string), "reconnect") {
		t.Fatalf("timeout must ask to reconnect, got %v", body["message"])
	}
}

// WebSocket clients receive an initial snapshot then a frame on change.
func TestWebSocketPush(t *testing.T) {
	h := newHarness(t)
	id := seedWorld(t, h)
	hdr := map[string]string{"X-Tenant-ID": "t1"}
	doJSON(t, "POST", h.srv.URL+"/api/items/"+id+"/values", hdr, `{"env":"dev","value":"k=v1\n"}`)

	url := strings.Replace(h.srv.URL, "http://", "ws://", 1) +
		"/api/ws?namespace=ns&group=g&env=dev"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Tenant-ID", "t1")
	dialer := websocket.Dialer{}
	conn, _, err := dialer.Dial(url, req.Header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// first frame = snapshot
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "snapshot") {
		t.Fatalf("expected initial snapshot, got %s", first)
	}

	doJSON(t, "POST", h.srv.URL+"/api/items/"+id+"/values", hdr,
		`{"env":"dev","value":"k=v2\n","expected_version":1}`)

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, pushed, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected a pushed event: %v", err)
	}
	if !strings.Contains(string(pushed), `"version":2`) {
		t.Fatalf("push did not carry new version: %s", pushed)
	}
}

// Cross-tenant access is rejected at the API boundary.
func TestAPITenantGuard(t *testing.T) {
	h := newHarness(t)
	seedWorld(t, h)
	code, body := doJSON(t, "GET", h.srv.URL+"/api/namespaces",
		map[string]string{"X-Tenant-ID": "other"}, "")
	if code != 404 {
		t.Fatalf("unknown tenant must 404, got %d %v", code, body)
	}
	code, _ = doJSON(t, "GET", h.srv.URL+"/api/namespaces", nil, "")
	if code != 400 {
		t.Fatalf("missing tenant header must 400, got %d", code)
	}
}
