package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const (
	reachAgentMaxBody       = 64 << 10
	reachAgentWindow        = 10 * time.Minute
	reachAgentFailuresPerIP = 20
	// A probe asks for its job and posts one report per interval (60 s at
	// least), so 30 requests in ten minutes leave room for retries only.
	reachAgentRequestsPerProbe = 30
	reachAgentMaxTracked       = 10000
)

// ReachProbeAgentController serves the probe agents under {basePath}probe/v1.
// A probe token reaches only these two routes and nothing under /panel/api.
type ReachProbeAgentController struct {
	probeService service.ReachProbeService
	limiter      *reachAgentLimiter
}

func NewReachProbeAgentController(g *gin.RouterGroup) *ReachProbeAgentController {
	a := &ReachProbeAgentController{limiter: newReachAgentLimiter(time.Now)}
	g.Use(a.authenticate)
	g.GET("/job", a.job)
	g.POST("/report", a.report)
	return a
}

func agentError(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

// authenticate checks the token (by its SHA-256 hash) before the per-address limit, which counts only
// failures: a valid token is never held back by others on its address (CGNAT, a proxy, the VPN exit).
func (a *ReachProbeAgentController) authenticate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ip := getRemoteIp(c)
	var probe *model.ReachProbe
	err := service.ErrReachProbeUnauthorized
	if token, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer "); ok {
		probe, err = a.probeService.Authenticate(token)
	}
	if err != nil {
		if !errors.Is(err, service.ErrReachProbeUnauthorized) {
			logger.Warning("reach probe: authenticate:", err)
			agentError(c, http.StatusInternalServerError, "internal error")
			return
		}
		blocked := !a.limiter.allowIP(ip)
		a.limiter.failIP(ip)
		if blocked {
			agentError(c, http.StatusTooManyRequests, "too many failed attempts")
			return
		}
		agentError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !a.limiter.allowProbe(probe.Id) {
		agentError(c, http.StatusTooManyRequests, "too many requests")
		return
	}
	c.Set("reach_probe", probe)
	c.Next()
}

func agentProbe(c *gin.Context) *model.ReachProbe {
	v, _ := c.Get("reach_probe")
	p, _ := v.(*model.ReachProbe)
	return p
}

func (a *ReachProbeAgentController) job(c *gin.Context) {
	job, err := a.probeService.BuildJob(agentProbe(c))
	if errors.Is(err, service.ErrReachProbeNoHost) {
		agentError(c, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		logger.Warning("reach probe: build job:", err)
		agentError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, job)
}

func (a *ReachProbeAgentController) report(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, reachAgentMaxBody)
	var report service.ReachReport
	dec := json.NewDecoder(c.Request.Body)
	if err := dec.Decode(&report); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			agentError(c, http.StatusRequestEntityTooLarge, "report too large")
			return
		}
		agentError(c, http.StatusBadRequest, "malformed report")
		return
	}
	stored, err := a.probeService.Ingest(agentProbe(c), &report)
	if err != nil {
		if errors.Is(err, service.ErrReachProbeNoHost) {
			agentError(c, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, service.ErrReachReportTooLarge) {
			agentError(c, http.StatusBadRequest, err.Error())
			return
		}
		logger.Warning("reach probe: ingest:", err)
		agentError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"stored": stored})
}

// reachAgentLimiter counts in fixed windows: failed authentications per
// address, and requests per probe. Both maps are bounded.
type reachAgentLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	ipFails  map[string]*reachWindow
	probeReq map[int]*reachWindow
}

type reachWindow struct {
	start time.Time
	count int
}

func newReachAgentLimiter(now func() time.Time) *reachAgentLimiter {
	return &reachAgentLimiter{now: now, ipFails: map[string]*reachWindow{}, probeReq: map[int]*reachWindow{}}
}

func windowFor[K comparable](m map[K]*reachWindow, key K, now time.Time) *reachWindow {
	w := m[key]
	if w == nil || now.Sub(w.start) >= reachAgentWindow {
		if w == nil && len(m) >= reachAgentMaxTracked {
			for k, old := range m {
				if now.Sub(old.start) >= reachAgentWindow {
					delete(m, k)
				}
			}
			if len(m) >= reachAgentMaxTracked {
				return nil
			}
		}
		w = &reachWindow{start: now}
		m[key] = w
	}
	return w
}

func (l *reachAgentLimiter) allowIP(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.ipFails[ip]
	return w == nil || l.now().Sub(w.start) >= reachAgentWindow || w.count < reachAgentFailuresPerIP
}

func (l *reachAgentLimiter) failIP(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := windowFor(l.ipFails, ip, l.now()); w != nil {
		w.count++
	}
}

func (l *reachAgentLimiter) allowProbe(id int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := windowFor(l.probeReq, id, l.now())
	if w == nil {
		return false
	}
	w.count++
	return w.count <= reachAgentRequestsPerProbe
}
