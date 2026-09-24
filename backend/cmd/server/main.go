// Command server runs the configuration governance platform backend.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"

	"configcenter/internal/api"
	"configcenter/internal/domain"
	"configcenter/internal/push"
	"configcenter/internal/service"
	"configcenter/internal/store"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dsn := env("DATABASE_DSN",
		"postgres://config:config@postgres:5432/configdb?sslmode=disable")
	redisAddr := env("REDIS_ADDR", "redis:6379")
	addr := env("HTTP_ADDR", ":8080")
	useRedis := os.Getenv("USE_REDIS") != "false"

	// ---- persistence ----
	var st store.Store
	var pg *store.Postgres
	if os.Getenv("MEMORY_STORE") == "true" {
		log.Println("MEMORY_STORE=true: using in-memory store")
		st = store.NewMemory()
	} else {
		var err error
		pg, err = openPostgresWithRetry(dsn, 30)
		if err != nil {
			log.Fatalf("connect postgres: %v", err)
		}
		if err := applySchema(pg, env("SCHEMA_DIR", "/app/db")); err != nil {
			log.Fatalf("apply schema: %v", err)
		}
		st = pg
		log.Println("postgres ready")
	}

	// ---- event bus + hub ----
	var bus push.EventBus
	var tracker push.Tracker
	if useRedis {
		rc := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{redisAddr}})
		if err := rc.Ping(ctx).Err(); err != nil {
			log.Fatalf("connect redis: %v", err)
		}
		bus = push.NewRedisBus(rc)
		tracker = push.NewRedisTrackerSync(rc)
		log.Println("redis ready")
	} else {
		bus = push.NewMemoryBus()
		tracker = push.NewMemTracker()
	}
	hub := push.NewHub(tracker)
	svc := service.New(st, bus, hub)

	// consume cross-node events and dispatch to local connections
	go consumeEvents(ctx, bus, hub, svc)

	// ---- seed demo tenant (idempotent) ----
	seed(ctx, svc)

	// ---- http ----
	r := api.NewRouter(svc)
	srv := &http.Server{Addr: addr, Handler: r, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		log.Printf("config center listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancel()
	shutdownCtx, sh := context.WithTimeout(context.Background(), 5*time.Second)
	defer sh()
	_ = srv.Shutdown(shutdownCtx)
	if pg != nil {
		_ = pg.Close()
	}
	_ = bus.Close()
}

func consumeEvents(ctx context.Context, bus push.EventBus, hub *push.Hub, svc *service.Service) {
	events, unsub, err := bus.Subscribe(ctx)
	if err != nil {
		log.Fatalf("subscribe events: %v", err)
	}
	defer func() { _ = unsub() }()
	gate := svc.NewGate()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			n := hub.Dispatch(ev, gate)
			log.Printf("event %s/%s key=%s v%d -> %d conns",
				ev.TenantID, ev.NamespaceID, ev.Key, ev.Version, n)
		}
	}
}

func openPostgresWithRetry(dsn string, attempts int) (*store.Postgres, error) {
	var last error
	for i := 0; i < attempts; i++ {
		p, err := store.NewPostgres(dsn)
		if err == nil {
			return p, nil
		}
		last = err
		time.Sleep(time.Second)
	}
	return nil, last
}

func applySchema(pg *store.Postgres, dir string) error {
	paths := []string{
		filepath.Join(dir, "001_init.sql"),
		"/deploy/db/001_init.sql",
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if _, err := pg.DB().Exec(string(b)); err != nil {
			return err
		}
		log.Printf("schema applied from %s", p)
		return nil
	}
	return os.ErrNotExist
}

// seed creates a demo tenant plus one namespace/group with a sample layered
// config so the console has something to show. It never overwrites data.
func seed(ctx context.Context, svc *service.Service) {
	const tenantID = "demo"
	if _, err := svc.GetTenant(ctx, tenantID); err == nil {
		return
	}
	q := service.Quota{MaxNamespaces: 20, MaxItemsPerGroup: 200, VersionRetention: 100}
	if _, err := svc.CreateTenant(ctx, tenantID, "演示租户", &q); err != nil {
		log.Printf("seed tenant: %v", err)
		return
	}
	if _, err := svc.CreateNamespace(ctx, tenantID, "payment", "支付业务线"); err != nil {
		log.Printf("seed namespace: %v", err)
		return
	}
	if _, err := svc.CreateGroup(ctx, tenantID, "payment", "gateway", "支付网关"); err != nil {
		log.Printf("seed group: %v", err)
	}

	mustItem := func(ns, grp, key string, f domain.Format, value string) {
		item, err := svc.CreateItem(ctx, service.CreateItemParams{
			TenantID: tenantID, NamespaceID: ns, GroupID: grp, Key: key, Format: f,
		})
		if err != nil {
			log.Printf("seed item %s: %v", key, err)
			return
		}
		_, _ = svc.Commit(ctx, service.CommitParams{
			TenantID: tenantID, ItemID: item.ID, Env: "dev",
			Value: value, Operator: "system",
		})
	}

	mustItem(domain.PublicNamespaceID, domain.PublicNamespaceID, "app",
		domain.FormatJSON, "{\n  \"name\": \"demo\",\n  \"timeout\": 1000,\n  \"log\": {\"level\": \"info\"}\n}\n")
	mustItem("payment", domain.DefaultGroupID, "app",
		domain.FormatJSON, "{\n  \"timeout\": 2000,\n  \"log\": {\"format\": \"json\"}\n}\n")
	mustItem("payment", "gateway", "app",
		domain.FormatJSON, "{\n  \"name\": \"payment-gateway\"\n}\n")

	// Reference demo: a shared host name in the public layer is embedded into
	// the group's database connection string at effective time.
	mustItem(domain.PublicNamespaceID, domain.PublicNamespaceID, "db_host",
		domain.FormatProperties, "host=shared-postgres.internal\n")
	mustItem("payment", "gateway", "db_conn",
		domain.FormatProperties, "url=jdbc:postgresql://@{db_host}:5432/pay\npool=16\n")
	log.Println("seed data ready (tenant=demo)")
}
