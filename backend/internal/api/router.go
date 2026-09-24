// Package api exposes the HTTP/WebSocket interface of the platform.
package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"configcenter/internal/service"
	"configcenter/internal/store"
)

// Server bundles dependencies for the HTTP layer.
type Server struct {
	svc             *service.Service
	longPollTimeout time.Duration
}

// longPollTimeout is 30 seconds by default and can be shortened via
// LONG_POLL_TIMEOUT_SECONDS (used by tests; clients always treat it as <=30s).
func defaultLongPollTimeout() time.Duration {
	if v := os.Getenv("LONG_POLL_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 30 * time.Second
}

// NewRouter constructs the gin engine.
func NewRouter(svc *service.Service) *gin.Engine {
	s := &Server{svc: svc, longPollTimeout: defaultLongPollTimeout()}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(cors.New(cors.Config{
		AllowAllOrigins:  true,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"*"},
		ExposeHeaders:    []string{"X-Tenant-ID", "X-Operator"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}))
	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })

	api := r.Group("/api")

	// tenant provisioning
	api.GET("/tenants", s.listTenants)
	api.POST("/tenants", s.createTenant)
	api.PUT("/tenants/:id/quota", s.updateQuota)

	cfg := api.Group("", s.tenantRequired)
	{
		cfg.GET("/namespaces", s.listNamespaces)
		cfg.POST("/namespaces", s.createNamespace)
		cfg.GET("/namespaces/:ns/groups", s.listGroups)
		cfg.POST("/namespaces/:ns/groups", s.createGroup)
		cfg.GET("/namespaces/:ns/groups/:g/items", s.listItems)
		cfg.POST("/namespaces/:ns/groups/:g/items", s.createItem)

		cfg.GET("/items/:id", s.getItem)
		cfg.PUT("/items/:id/schema", s.updateSchema)
		cfg.GET("/items/:id/versions", s.listVersions)
		cfg.POST("/items/:id/values", s.commitValue)
		cfg.POST("/items/:id/rollback", s.rollback)
		cfg.GET("/items/:id/diff", s.diff)

		cfg.GET("/releases", s.listReleases)
		cfg.GET("/releases/:id", s.getRelease)
		cfg.POST("/releases/:id/promote", s.promote)
		cfg.POST("/releases/:id/rollback-gray", s.grayRollback)

		cfg.GET("/effective", s.effective)
		cfg.GET("/poll", s.poll)
		cfg.GET("/ws", s.ws)
		cfg.GET("/stats", s.stats)
		cfg.GET("/instances", s.instances)
	}
	return r
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// ---------- context helpers ----------

func tenantID(c *gin.Context) string { return c.GetHeader("X-Tenant-ID") }

func operator(c *gin.Context) string {
	if v := c.GetHeader("X-Operator"); v != "" {
		return v
	}
	return "anonymous"
}

func (s *Server) tenantRequired(c *gin.Context) {
	if tenantID(c) == "" {
		c.JSON(400, gin.H{"error": "missing X-Tenant-ID header"})
		c.Abort()
		return
	}
	if _, err := s.svc.GetTenant(c.Request.Context(), tenantID(c)); err != nil {
		c.JSON(404, gin.H{"error": "tenant not found"})
		c.Abort()
		return
	}
	c.Next()
}

func fail(c *gin.Context, err error) {
	switch {
	case isErr(err, store.ErrNotFound):
		c.JSON(404, gin.H{"error": err.Error()})
	case isErr(err, store.ErrQuota):
		c.JSON(402, gin.H{"error": err.Error(), "code": "quota_exceeded"})
	case isErr(err, store.ErrConflict):
		c.JSON(409, gin.H{"error": err.Error(), "code": "version_conflict"})
	case isErr(err, store.ErrDuplicate):
		c.JSON(409, gin.H{"error": err.Error(), "code": "duplicate"})
	default:
		if vf, ok := err.(*service.ValidationFailure); ok {
			c.JSON(422, gin.H{"error": "validation failed", "errors": vf.Errors})
			return
		}
		c.JSON(400, gin.H{"error": err.Error()})
	}
}

func isErr(err, target error) bool {
	return strings.Contains(err.Error(), target.Error()) || err == target
}

func atoiDefault(s string, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}
