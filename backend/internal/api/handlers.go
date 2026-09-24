package api

import (
	"encoding/json"
	"strconv"

	"github.com/gin-gonic/gin"

	"configcenter/internal/domain"
	"configcenter/internal/push"
	"configcenter/internal/service"
)

// ---------- tenants ----------

type tenantReq struct {
	ID   string `json:"id"`
	Name string `json:"name" binding:"required"`
	*service.Quota
}

func (s *Server) createTenant(c *gin.Context) {
	var req tenantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	t, err := s.svc.CreateTenant(c.Request.Context(), req.ID, req.Name, req.Quota)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(201, t)
}

func (s *Server) listTenants(c *gin.Context) {
	ts, err := s.svc.ListTenants(c.Request.Context())
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"tenants": ts})
}

func (s *Server) updateQuota(c *gin.Context) {
	var q service.Quota
	if err := c.ShouldBindJSON(&q); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := s.svc.UpdateQuota(c.Request.Context(), c.Param("id"), q); err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

// ---------- namespaces / groups ----------

type idNameReq struct {
	ID   string `json:"id" binding:"required"`
	Name string `json:"name"`
}

func (s *Server) createNamespace(c *gin.Context) {
	var req idNameReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	n, err := s.svc.CreateNamespace(c.Request.Context(), tenantID(c), req.ID, req.Name)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(201, n)
}

func (s *Server) listNamespaces(c *gin.Context) {
	out, err := s.svc.ListNamespaces(c.Request.Context(), tenantID(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"namespaces": out})
}

func (s *Server) createGroup(c *gin.Context) {
	var req idNameReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	g, err := s.svc.CreateGroup(c.Request.Context(), tenantID(c), c.Param("ns"), req.ID, req.Name)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(201, g)
}

func (s *Server) listGroups(c *gin.Context) {
	out, err := s.svc.ListGroups(c.Request.Context(), tenantID(c), c.Param("ns"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"groups": out})
}

// ---------- items ----------

type itemReq struct {
	Key    string        `json:"key" binding:"required"`
	Format domain.Format `json:"format" binding:"required"`
	Schema string        `json:"schema"`
}

func (s *Server) createItem(c *gin.Context) {
	var req itemReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	item, err := s.svc.CreateItem(c.Request.Context(), service.CreateItemParams{
		TenantID:    tenantID(c),
		NamespaceID: c.Param("ns"),
		GroupID:     c.Param("g"),
		Key:         req.Key,
		Format:      req.Format,
		Schema:      req.Schema,
	})
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(201, item)
}

func (s *Server) listItems(c *gin.Context) {
	out, err := s.svc.ListItems(c.Request.Context(), tenantID(c), c.Param("ns"), c.Param("g"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"items": out})
}

func (s *Server) getItem(c *gin.Context) {
	out, err := s.svc.GetItem(c.Request.Context(), tenantID(c), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, out)
}

type schemaReq struct {
	Schema string `json:"schema"`
}

func (s *Server) updateSchema(c *gin.Context) {
	var req schemaReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := s.svc.UpdateSchema(c.Request.Context(), tenantID(c), c.Param("id"), req.Schema); err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

// ---------- versions ----------

func (s *Server) listVersions(c *gin.Context) {
	env := c.Query("env")
	if env == "" {
		c.JSON(400, gin.H{"error": "env query param is required"})
		return
	}
	limit := int(atoiDefault(c.Query("limit"), 100))
	out, err := s.svc.ListVersions(c.Request.Context(), tenantID(c), c.Param("id"), env, limit)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"versions": out})
}

type valueReq struct {
	Env             string               `json:"env" binding:"required"`
	Value           string               `json:"value" binding:"required"`
	ExpectedVersion int64                `json:"expected_version"`
	Gray            *service.GrayRequest `json:"gray,omitempty"`
}

func (s *Server) commitValue(c *gin.Context) {
	var req valueReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	out, err := s.svc.Commit(c.Request.Context(), service.CommitParams{
		TenantID: tenantID(c), ItemID: c.Param("id"), Env: req.Env,
		Value: req.Value, Operator: operator(c),
		ExpectedVersion: req.ExpectedVersion, Gray: req.Gray,
	})
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(201, out)
}

type rollbackReq struct {
	Env     string `json:"env" binding:"required"`
	Version int64  `json:"version" binding:"required"`
}

func (s *Server) rollback(c *gin.Context) {
	var req rollbackReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	out, err := s.svc.Rollback(c.Request.Context(), tenantID(c), c.Param("id"),
		req.Env, req.Version, operator(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(201, out)
}

func (s *Server) diff(c *gin.Context) {
	env := c.Query("env")
	from := atoiDefault(c.Query("from"), 0)
	to := atoiDefault(c.Query("to"), 0)
	if env == "" || from == 0 || to == 0 {
		c.JSON(400, gin.H{"error": "env, from and to query params are required"})
		return
	}
	rows, a, b, err := s.svc.Diff(c.Request.Context(), tenantID(c), c.Param("id"), env, from, to)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"from": a, "to": b, "lines": rows})
}

// ---------- reference graph ----------

func (s *Server) itemRefs(c *gin.Context) {
	out, err := s.svc.ItemGraph(c.Request.Context(), tenantID(c), c.Param("id"), c.Query("env"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"references": out})
}

func (s *Server) itemImpact(c *gin.Context) {
	env := c.Query("env")
	if env == "" {
		c.JSON(400, gin.H{"error": "env query param is required"})
		return
	}
	out, err := s.svc.Impact(c.Request.Context(), tenantID(c), c.Param("id"), env)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, out)
}

// ---------- gray releases ----------

func (s *Server) listReleases(c *gin.Context) {
	ns := c.Query("namespace")
	if ns == "" {
		c.JSON(400, gin.H{"error": "namespace query param is required"})
		return
	}
	limit := int(atoiDefault(c.Query("limit"), 50))
	out, err := s.svc.ListReleases(c.Request.Context(), tenantID(c), ns, limit)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"releases": out})
}

func (s *Server) getRelease(c *gin.Context) {
	out, err := s.svc.GetRelease(c.Request.Context(), tenantID(c), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, out)
}

func (s *Server) promote(c *gin.Context) {
	out, err := s.svc.Promote(c.Request.Context(), tenantID(c), c.Param("id"), operator(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, out)
}

func (s *Server) grayRollback(c *gin.Context) {
	r, v, err := s.svc.GrayRollback(c.Request.Context(), tenantID(c), c.Param("id"), operator(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"release": r, "version": v})
}

// ---------- effective / poll / websocket ----------

func instanceContext(c *gin.Context) service.InstanceContext {
	// Clients behind a NAT/proxy may declare their real IP; otherwise fall
	// back to X-Forwarded-For and then the direct peer address.
	return service.InstanceContext{
		InstanceID: c.Query("instance_id"),
		IP:         clientIP(c),
	}
}

func clientIP(c *gin.Context) string {
	if declared := c.Query("ip"); declared != "" {
		return declared
	}
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		if i := indexByte(xff, ','); i > 0 {
			return xff[:i]
		}
		return xff
	}
	return c.RemoteIP()
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func snapshot(c *gin.Context, s *Server) (*service.EffectiveSnapshot, bool) {
	snap, err := s.svc.Effective(c.Request.Context(), tenantID(c),
		c.Query("namespace"), c.Query("group"), c.Query("env"), instanceContext(c))
	if err != nil {
		fail(c, err)
		return nil, false
	}
	return snap, true
}

func (s *Server) effective(c *gin.Context) {
	if c.Query("namespace") == "" {
		c.JSON(400, gin.H{"error": "namespace query param is required"})
		return
	}
	snap, ok := snapshot(c, s)
	if !ok {
		return
	}
	c.JSON(200, snap)
}

// poll implements long-polling:
//   - newer snapshot than client_version  -> respond immediately
//   - otherwise suspend up to 30s and respond on change; on timeout tell the
//     client to reconnect.
func (s *Server) poll(c *gin.Context) {
	ns := c.Query("namespace")
	env := c.Query("env")
	if ns == "" {
		c.JSON(400, gin.H{"error": "namespace query param is required"})
		return
	}
	clientVersion := atoiDefault(c.Query("version"), 0)
	groupID := c.Query("group")
	ic := instanceContext(c)

	snap, err := s.svc.Effective(c.Request.Context(), tenantID(c), ns, groupID, env, ic)
	if err != nil {
		fail(c, err)
		return
	}
	if snap.Version > clientVersion {
		c.JSON(200, gin.H{"changed": true, "snapshot": snap})
		return
	}

	conn := s.svc.Hub().Register(push.ConnMeta{
		InstanceID:  c.Query("instance_id"),
		TenantID:    tenantID(c),
		NamespaceID: ns,
		GroupID:     groupID,
		Env:         env,
		IP:          ic.IP,
	})
	defer s.svc.Hub().Unregister(conn)

	// Re-check immediately in case the event landed between snapshot and register.
	snap, err = s.svc.Effective(c.Request.Context(), tenantID(c), ns, groupID, env, ic)
	if err != nil {
		fail(c, err)
		return
	}
	if snap.Version > clientVersion {
		c.JSON(200, gin.H{"changed": true, "snapshot": snap})
		return
	}

	if woken := s.svc.Hub().Wait(conn, s.longPollTimeout); woken {
		snap, err = s.svc.Effective(c.Request.Context(), tenantID(c), ns, groupID, env, ic)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, gin.H{"changed": snap.Version > clientVersion, "snapshot": snap})
		return
	}
	c.JSON(200, gin.H{"changed": false, "message": "timeout, please reconnect"})
}

func (s *Server) ws(c *gin.Context) {
	ns := c.Query("namespace")
	if ns == "" {
		c.JSON(400, gin.H{"error": "namespace query param is required"})
		return
	}
	env := c.Query("env")
	groupID := c.Query("group")
	ic := instanceContext(c)

	socket, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	conn := s.svc.Hub().Register(push.ConnMeta{
		InstanceID:  c.Query("instance_id"),
		TenantID:    tenantID(c),
		NamespaceID: ns,
		GroupID:     groupID,
		Env:         env,
		IP:          ic.IP,
		Socket:      socket,
	})
	// Initial snapshot so a freshly connected client is immediately in sync.
	// It is queued through the same channel as pushed events so the
	// writePump stays the single writer on the socket (gorilla/websocket
	// forbids concurrent writers, which would interleave frames).
	snap, err := s.svc.Effective(c.Request.Context(), tenantID(c), ns, groupID, env, ic)
	if err == nil {
		if b, jerr := json.Marshal(gin.H{"type": "snapshot", "snapshot": snap}); jerr == nil {
			conn.Enqueue(b)
		}
	}
	// Register already starts read/write pumps and unregisters on disconnect.
	_ = conn
}

// ---------- dashboard ----------

func (s *Server) stats(c *gin.Context) {
	out := s.svc.Hub().Stats(tenantID(c))
	c.JSON(200, gin.H{"namespaces": out})
}

func (s *Server) instances(c *gin.Context) {
	ns := c.Query("namespace")
	if ns == "" {
		c.JSON(400, gin.H{"error": "namespace query param is required"})
		return
	}
	out := s.svc.Hub().ListInstances(tenantID(c), ns)
	c.JSON(200, gin.H{"instances": out, "total": len(out)})
}

var _ = strconv.Itoa
