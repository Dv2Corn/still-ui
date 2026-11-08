package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/nodeenroll"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// listInvites lists the node invitations (Nodes → Add with a command).
func (a *NodeController) listInvites(c *gin.Context) {
	rows, err := a.enrollService.ListInvites()
	jsonObj(c, rows, err)
}

// createInvite makes a one-time invitation; its secret is in this answer only.
func (a *NodeController) createInvite(c *gin.Context) {
	var in service.NodeInviteInput
	if err := c.ShouldBindJSON(&in); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.toasts.inviteCreate"), err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	created, err := a.enrollService.CreateInvite(ctx, in)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.toasts.inviteCreate"), err)
		return
	}
	c.Header("Cache-Control", "no-store")
	jsonObj(c, created, nil)
}

func (a *NodeController) revokeInvite(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.nodes.toasts.inviteRevoke"), a.enrollService.RevokeInvite(id))
}

const (
	nodeEnrollMaxBody = 16 << 10
	// A node checks its invitation once and registers once; failed attempts
	// give the token back, so a handful of requests per window is plenty.
	nodeEnrollRequestsPerInvite = 20
	nodeEnrollFailuresPerIP     = 10
)

// NodeEnrollAgentController serves new nodes under {basePath}enroll/v1. An
// invitation token opens only this route and nothing under /panel/api.
type NodeEnrollAgentController struct {
	enrollService service.NodeEnrollService
	limiter       *agentLimiter
}

func NewNodeEnrollAgentController(g *gin.RouterGroup) *NodeEnrollAgentController {
	a := &NodeEnrollAgentController{limiter: newAgentLimiter(time.Now, nodeEnrollFailuresPerIP, nodeEnrollRequestsPerInvite)}
	g.GET("/node", a.check)
	g.POST("/node", a.enroll)
	return a
}

func enrollError(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, nodeenroll.ErrorResponse{Error: msg})
}

// authorize checks the token before the per-address limit, as the probe agents
// do: only failures count, so others behind one address never block a valid one.
// It returns the token and the invitation check that check answers with.
func (a *NodeEnrollAgentController) authorize(c *gin.Context) (string, *nodeenroll.CheckResponse, bool) {
	c.Header("Cache-Control", "no-store")
	ip := getRemoteIp(c)
	token, _ := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	resp, row, err := a.enrollService.CheckInvite(token)
	if err != nil {
		if service.NodeEnrollHTTPStatus(err) == http.StatusUnauthorized {
			blocked := !a.limiter.allowIP(ip)
			a.limiter.failIP(ip)
			if blocked {
				enrollError(c, http.StatusTooManyRequests, "too many failed attempts")
				return "", nil, false
			}
		}
		a.fail(c, err)
		return "", nil, false
	}
	if !a.limiter.allowKey(row.Id) {
		enrollError(c, http.StatusTooManyRequests, "too many requests")
		return "", nil, false
	}
	return token, resp, true
}

func (a *NodeEnrollAgentController) fail(c *gin.Context, err error) {
	status := service.NodeEnrollHTTPStatus(err)
	if status == http.StatusInternalServerError {
		logger.Warning("node enrollment:", err)
		enrollError(c, status, "internal error")
		return
	}
	enrollError(c, status, err.Error())
}

func (a *NodeEnrollAgentController) check(c *gin.Context) {
	_, resp, ok := a.authorize(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (a *NodeEnrollAgentController) enroll(c *gin.Context) {
	token, _, ok := a.authorize(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, nodeEnrollMaxBody)
	var req nodeenroll.RegisterRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			enrollError(c, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		enrollError(c, http.StatusBadRequest, "malformed request")
		return
	}
	ip := getRemoteIp(c)
	if ip == "unknown" {
		ip = ""
	}
	resp, err := a.enrollService.Enroll(c.Request.Context(), token, &req, ip)
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}
