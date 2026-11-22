package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

// auditSniffBytes is how much of a response is kept to read its "success" flag; a longer
// answer is judged by its status code alone.
const auditSniffBytes = 64 << 10

// auditNodePushPOST lists state-changing POST routes (relative to /panel/api) that a master
// pushes every few seconds; they skip the journal only under a node token's scope.
var auditNodePushPOST = map[string]struct{}{
	"/inbounds/pushClientTraffics": {},
	"/server/clientIps":            {},
}

// auditReadOnlyPOST lists POST routes (relative to /panel/api) that change nothing; recording
// them, some polled every few seconds, would bury the admin actions.
var auditReadOnlyPOST = map[string]struct{}{
	"/server/logs/:count":          {},
	"/server/xraylogs/:count":      {},
	"/server/amneziawglogs/:count": {},
	"/server/getCertHash":          {},
	"/server/getRemoteCertHash":    {},
	"/clients/onlines":             {},
	"/clients/onlinesByGuid":       {},
	"/clients/clientIpsByGuid":     {},
	"/clients/activeInbounds":      {},
	"/clients/lastOnline":          {},
	"/clients/renewalPreview":      {},
	"/clients/ips/:email":          {},
	"/nodes/inbounds":              {},
	"/nodes/certFingerprint":       {},
	"/setting/all":                 {},
	"/setting/defaultSettings":     {},
	"/setting/factoryDefaults":     {},
	"/setting/validateRegex":       {},
	"/setting/tgBotCapabilities":   {},
	"/xray/":                       {},
	"/xray/balancerStatus":         {},
	"/xray/routeTest":              {},
	"/xray/geodata/validate":       {},
	"/xray/outbound-subs/parse":    {},
}

// auditSecretParam matches route parameter names that could carry a credential or a
// subscription id; their values never reach the journal.
var auditSecretParam = regexp.MustCompile(`(?i)pass|secret|token|key|sub`)

type auditWriter struct {
	gin.ResponseWriter
	head bytes.Buffer
}

func (w *auditWriter) keep(p []byte) {
	if room := auditSniffBytes + 1 - w.head.Len(); room > 0 {
		w.head.Write(p[:min(room, len(p))])
	}
}

func (w *auditWriter) Write(p []byte) (int, error) {
	w.keep(p)
	return w.ResponseWriter.Write(p)
}

func (w *auditWriter) WriteString(s string) (int, error) {
	w.keep([]byte(s))
	return w.ResponseWriter.WriteString(s)
}

// outcome reads failure from the status code or from the envelope's "success": false, which
// most handlers answer with HTTP 200.
func (w *auditWriter) outcome() string {
	if w.Status() >= http.StatusBadRequest {
		return service.AuditFailure
	}
	var msg struct {
		Success *bool `json:"success"`
	}
	if w.head.Len() <= auditSniffBytes && json.Unmarshal(w.head.Bytes(), &msg) == nil && msg.Success != nil && !*msg.Success {
		return service.AuditFailure
	}
	return service.AuditSuccess
}

func auditActor(c *gin.Context) string {
	if name := c.GetString("api_token_name"); name != "" {
		return "token:" + name
	}
	if scope := c.GetString("api_token_scope"); scope != "" {
		return "mtls:" + scope
	}
	if user := session.GetLoginUser(c); user != nil {
		return user.Username
	}
	return ""
}

func auditTarget(c *gin.Context) string {
	parts := make([]string, 0, len(c.Params))
	for _, p := range c.Params {
		if !auditSecretParam.MatchString(p.Key) {
			parts = append(parts, p.Key+"="+p.Value)
		}
	}
	return strings.Join(parts, " ")
}

// recordAudit journals every state-changing request that passed authentication. It sits on
// the /panel/api group, so a route added there is recorded without further wiring.
func (a *APIController) recordAudit(c *gin.Context) {
	rel := relAPIPath(c.FullPath())
	if isSafeHTTPMethod(c.Request.Method) || auditSkipsPOST(c, rel) {
		c.Next()
		return
	}
	w := &auditWriter{ResponseWriter: c.Writer}
	c.Writer = w
	c.Next()
	err := a.auditService.Record(model.AuditLog{
		Actor:   auditActor(c),
		IP:      getRemoteIp(c),
		Action:  c.Request.Method + " /panel/api" + rel,
		Target:  auditTarget(c),
		Outcome: w.outcome(),
		Detail:  "HTTP " + strconv.Itoa(w.Status()),
	})
	if err != nil {
		logger.Warning("audit log: record failed:", err)
	}
}

// auditSkipsPOST reads the scope checkAPIAuth set, so an admin session or admin token pushing
// node telemetry is still journaled.
func auditSkipsPOST(c *gin.Context, rel string) bool {
	if c.Request.Method != http.MethodPost {
		return false
	}
	if _, readOnly := auditReadOnlyPOST[rel]; readOnly {
		return true
	}
	_, nodePush := auditNodePushPOST[rel]
	return nodePush && model.IsNodeSyncApiScope(c.GetString("api_token_scope"))
}

func isSafeHTTPMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// AuditController serves the journal under /panel/api/audit. No route is on a restricted
// token's allowlist, and recordAudit journals every call, reads included.
type AuditController struct {
	auditService service.AuditService
}

func NewAuditController(g *gin.RouterGroup) *AuditController {
	a := &AuditController{}
	g.POST("/list", a.list)
	g.POST("/export", a.export)
	g.POST("/verify", a.verify)
	return a
}

func (a *AuditController) filter(c *gin.Context) (service.AuditFilter, bool) {
	var f service.AuditFilter
	err := c.ShouldBindJSON(&f)
	if err == nil && (f.From < 0 || f.To < 0 || (f.To > 0 && f.From > f.To)) {
		err = common.NewError("the time range is not valid")
	}
	if err == nil && f.Outcome != "" && f.Outcome != service.AuditSuccess && f.Outcome != service.AuditFailure {
		err = common.NewError("outcome must be success or failure")
	}
	for _, text := range []string{f.Actor, f.Action} {
		if err == nil && len(text) > connLogFilterTextBytes {
			err = common.NewErrorf("a filter value is longer than %d bytes", connLogFilterTextBytes)
		}
	}
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return f, false
	}
	return f, true
}

func (a *AuditController) list(c *gin.Context) {
	f, ok := a.filter(c)
	if !ok {
		return
	}
	page, err := a.auditService.Query(f)
	c.Header("Cache-Control", "no-store")
	jsonObj(c, page, err)
}

func (a *AuditController) export(c *gin.Context) {
	f, ok := a.filter(c)
	if !ok {
		return
	}
	var csv bytes.Buffer
	if err := a.auditService.Export(&csv, f); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", `attachment; filename="audit-log.csv"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", csv.Bytes())
}

func (a *AuditController) verify(c *gin.Context) {
	res, err := a.auditService.Verify()
	jsonObj(c, res, err)
}
