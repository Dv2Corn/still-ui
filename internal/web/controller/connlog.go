package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/connlog"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

const (
	// A query that matches nothing has to read every day in its range before it can say so.
	connLogQueryTimeout = time.Minute
	// No stored value is longer, so a longer filter can only waste a scan.
	connLogFilterTextBytes = 255
)

// ConnLogController serves the connection log under /panel/api/connlog. Neither route is on
// a restricted token's allowlist: only a logged-in admin or an admin token reaches them.
type ConnLogController struct {
	connLogService service.ConnLogService
}

func NewConnLogController(g *gin.RouterGroup) *ConnLogController {
	a := &ConnLogController{}
	g.POST("/list", a.list)
	g.POST("/export", a.export)
	return a
}

// filter binds and checks the request, then writes who asked for what to the panel log:
// reading who connected where is itself worth a record.
func (a *ConnLogController) filter(c *gin.Context, action string) (connlog.ConnFilter, bool) {
	var f connlog.ConnFilter
	err := c.ShouldBindJSON(&f)
	if err == nil && (f.From < 0 || f.To < 0 || (f.To > 0 && f.From > f.To)) {
		err = common.NewError("the time range is not valid")
	}
	if err == nil && (f.Limit < 0 || f.Limit > connlog.MaxPageRows) {
		err = common.NewErrorf("limit must be between 1 and %d", connlog.MaxPageRows)
	}
	for _, text := range []string{f.Client, f.Inbound, f.Host, f.Source, f.Before} {
		if err == nil && len(text) > connLogFilterTextBytes {
			err = common.NewErrorf("a filter value is longer than %d bytes", connLogFilterTextBytes)
		}
	}
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return f, false
	}
	who := "api token"
	if user := session.GetLoginUser(c); user != nil {
		who = user.Username
	}
	asked, _ := json.Marshal(f)
	logger.Infof("connection log: %s by %s from %s: %s", action, who, getRemoteIp(c), asked)
	return f, true
}

func (a *ConnLogController) list(c *gin.Context) {
	f, ok := a.filter(c, "query")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), connLogQueryTimeout)
	defer cancel()
	page, err := a.connLogService.Query(ctx, f)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	c.Header("Cache-Control", "no-store")
	jsonObj(c, page, nil)
}

func (a *ConnLogController) export(c *gin.Context) {
	f, ok := a.filter(c, "export")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), connLogQueryTimeout)
	defer cancel()
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="connections.csv"`)
	c.Status(http.StatusOK)
	// Rows already sent cannot be taken back: a failure ends the file short and is logged.
	if err := a.connLogService.Export(ctx, c.Writer, f); err != nil {
		logger.Warning("connection log: export failed:", err)
	}
}
