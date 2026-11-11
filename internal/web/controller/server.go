package controller

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/netsafe"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"

	"github.com/gin-gonic/gin"
)

var filenameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-.]+$`)

// ServerController handles server management and status-related operations.
type ServerController struct {
	BaseController

	serverService      service.ServerService
	settingService     service.SettingService
	panelService       panel.PanelService
	xrayMetricsService service.XrayMetricsService
	torrentBlock       service.TorrentBlockService
	egressFilter       service.EgressFilterService
	portHop            service.PortHopService
	xrayService        service.XrayService
}

// NewServerController creates a new ServerController, initializes routes, and starts background tasks.
func NewServerController(g *gin.RouterGroup) *ServerController {
	a := &ServerController{}
	service.RestoreSystemMetrics()
	a.initRouter(g)
	a.startTask()
	return a
}

// initRouter sets up the routes for server status, Xray management, and utility endpoints.
func (a *ServerController) initRouter(g *gin.RouterGroup) {
	g.GET("/status", a.status)
	g.GET("/cpuHistory/:bucket", a.getCpuHistoryBucket)
	g.GET("/history/:metric/:bucket", a.getMetricHistoryBucket)
	g.GET("/xrayMetricsState", a.getXrayMetricsState)
	g.GET("/xrayMetricsHistory/:metric/:bucket", a.getXrayMetricsHistoryBucket)
	g.GET("/xrayObservatory", a.getXrayObservatory)
	g.GET("/xrayObservatoryHistory/:tag/:bucket", a.getXrayObservatoryHistoryBucket)
	g.GET("/getXrayVersion", a.getXrayVersion)
	g.GET("/getPanelUpdateInfo", a.getPanelUpdateInfo)
	g.GET("/getUpdateStatus", a.getUpdateStatus)
	g.GET("/getConfigJson", a.getConfigJson)
	g.GET("/getDb", a.getDb)
	g.GET("/localBackups", a.getLocalBackups)
	g.GET("/getMigration", a.getMigration)
	g.GET("/getNewUUID", a.getNewUUID)
	g.GET("/getWebCertFiles", a.getWebCertFiles)
	g.GET("/descendants", a.descendants)
	g.GET("/getNewX25519Cert", a.getNewX25519Cert)
	g.GET("/getNewmldsa65", a.getNewmldsa65)
	g.GET("/getNewmlkem768", a.getNewmlkem768)
	g.GET("/getNewVlessEnc", a.getNewVlessEnc)
	g.GET("/getNewAwgSignaturePacket", a.getNewAwgSignaturePacket)
	g.GET("/clientIps", a.getClientIps)
	g.GET("/fail2banStatus", a.getFail2banStatus)
	g.GET("/torrentBlock", a.getTorrentBlock)
	g.GET("/egressFilter", a.getEgressFilter)
	g.GET("/portHop", a.getPortHop)

	g.POST("/stopXrayService", a.stopXrayService)
	g.POST("/restartXrayService", a.restartXrayService)
	g.POST("/installXray/:version", a.installXray)
	g.POST("/updatePanel", a.updatePanel)
	g.POST("/setUpdateChannel", a.setUpdateChannel)
	g.POST("/setTorrentBlock", a.setTorrentBlock)
	g.POST("/setEgressFilter", a.setEgressFilter)
	g.POST("/applyManagedEgressFilter", a.applyManagedEgressFilter)
	g.POST("/updateGeofile", a.updateGeofile)
	g.POST("/updateGeofile/:fileName", a.updateGeofile)
	g.POST("/logs/:count", a.getLogs)
	g.POST("/xraylogs/:count", a.getXrayLogs)
	g.POST("/amneziawglogs/:count", a.getAmneziaWGLogs)
	g.POST("/importDB", a.importDB)
	g.POST("/importPanel", a.importPanel)
	g.POST("/localBackup", a.runLocalBackup)
	g.POST("/getNewEchCert", a.getNewEchCert)
	g.POST("/getCertHash", a.getCertHash)
	g.POST("/getRemoteCertHash", a.getRemoteCertHash)
	g.POST("/scanRealityTarget", a.scanRealityTarget)
	g.POST("/scanRealityTargets", a.scanRealityTargets)
	g.POST("/clientIps", a.setClientIps)
}

// startTask registers the @2s ticker that refreshes server status, samples
// xray metrics, and pushes the new snapshot to all websocket subscribers.
// State + sampling live in ServerService; the controller only orchestrates
// the cross-service side effects (xrayMetrics sample + websocket broadcast).
func (a *ServerController) startTask() {
	c := global.GetWebServer().GetCron()
	_, _ = c.AddFunc("@every 2s", func() {
		status := a.serverService.RefreshStatus()
		if status == nil {
			return
		}
		a.xrayMetricsService.Sample(time.Now())
		websocket.BroadcastStatus(status)
	})
	_, _ = c.AddFunc("@every 1m", func() {
		if err := service.PersistSystemMetrics(); err != nil {
			logger.Warning("persist system metrics failed:", err)
		}
	})
}

// status returns the current server status information.
func (a *ServerController) status(c *gin.Context) { jsonObj(c, a.serverService.CurrentStatus(), nil) }

func (a *ServerController) getFail2banStatus(c *gin.Context) {
	jsonObj(c, a.serverService.GetFail2banStatus(), nil)
}

// getPortHop reports whether this host can redirect Hysteria2 hop ranges and what it redirects now.
func (a *ServerController) getPortHop(c *gin.Context) {
	jsonObj(c, a.portHop.Status(), nil)
}

func (a *ServerController) getTorrentBlock(c *gin.Context) {
	jsonObj(c, a.torrentBlock.Status(), nil)
}

type setTorrentBlockForm struct {
	Enable bool `json:"enable" form:"enable"`
}

// setTorrentBlock flips "Block torrents for clients"; the routing rule and sniffing
// live in the generated config, so the running core is reconciled right away.
func (a *ServerController) setTorrentBlock(c *gin.Context) {
	form := &setTorrentBlockForm{}
	if err := c.ShouldBind(form); err != nil {
		jsonMsg(c, "invalid data", err)
		return
	}
	status, err := a.torrentBlock.SetEnabled(form.Enable)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.toasts.modifySettings"), err)
		return
	}
	if restartErr := a.xrayService.RestartXray(false); restartErr != nil {
		logger.Warning("apply torrent block change failed:", restartErr)
	}
	jsonObj(c, status, nil)
}

func (a *ServerController) getEgressFilter(c *gin.Context) {
	jsonObj(c, a.egressFilter.Status(), nil)
}

// setEgressFilter stores this panel's own egress filter, hands it to the chosen nodes
// and reconciles the running core, whose routing carries the Xray half.
func (a *ServerController) setEgressFilter(c *gin.Context) {
	form := service.EgressFilterSettings{}
	if err := c.ShouldBindJSON(&form); err != nil {
		jsonMsg(c, "invalid data", err)
		return
	}
	status, err := a.egressFilter.SetLocal(form)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.toasts.modifySettings"), err)
		return
	}
	if restartErr := a.xrayService.RestartXray(false); restartErr != nil {
		logger.Warning("apply egress filter change failed:", restartErr)
	}
	jsonObj(c, status, nil)
}

// applyManagedEgressFilter is how a central panel sets (or releases) this node's filter.
func (a *ServerController) applyManagedEgressFilter(c *gin.Context) {
	req := service.ManagedEgressFilterRequest{}
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, "invalid data", err)
		return
	}
	changed, err := a.egressFilter.ApplyManaged(req)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.toasts.modifySettings"), err)
		return
	}
	if changed {
		if restartErr := a.xrayService.RestartXray(false); restartErr != nil {
			logger.Warning("apply managed egress filter failed:", restartErr)
		}
	}
	jsonObj(c, a.egressFilter.Status(), nil)
}

func parseHistoryBucket(c *gin.Context) (int, bool) {
	bucket, err := strconv.Atoi(c.Param("bucket"))
	if err != nil || bucket <= 0 || !service.IsAllowedHistoryBucket(bucket) {
		jsonMsg(c, "invalid bucket", fmt.Errorf("unsupported bucket"))
		return 0, false
	}
	return bucket, true
}

// getCpuHistoryBucket retrieves aggregated CPU usage history based on the specified time bucket.
// Kept for back-compat; new callers should use /history/cpu/:bucket which
// returns {"t","v"} (uniform across all metrics) instead of {"t","cpu"}.
func (a *ServerController) getCpuHistoryBucket(c *gin.Context) {
	bucket, ok := parseHistoryBucket(c)
	if !ok {
		return
	}
	jsonObj(c, a.serverService.AggregateCpuHistory(bucket, 60), nil)
}

// getMetricHistoryBucket returns up to 60 buckets of history for a single
// system metric (cpu, mem, netUp, netDown, online, load1/5/15). The
// SystemHistoryModal calls one endpoint per active tab.
func (a *ServerController) getMetricHistoryBucket(c *gin.Context) {
	metric := c.Param("metric")
	if !slices.Contains(service.SystemMetricKeys, metric) {
		jsonMsg(c, "invalid metric", fmt.Errorf("unknown metric"))
		return
	}
	bucket, ok := parseHistoryBucket(c)
	if !ok {
		return
	}
	jsonObj(c, a.serverService.AggregateSystemMetric(metric, bucket, 60), nil)
}

func (a *ServerController) getXrayMetricsState(c *gin.Context) {
	jsonObj(c, a.xrayMetricsService.State(), nil)
}

func (a *ServerController) getXrayMetricsHistoryBucket(c *gin.Context) {
	metric := c.Param("metric")
	if !slices.Contains(service.XrayMetricKeys, metric) {
		jsonMsg(c, "invalid metric", fmt.Errorf("unknown metric"))
		return
	}
	bucket, ok := parseHistoryBucket(c)
	if !ok {
		return
	}
	jsonObj(c, a.xrayMetricsService.AggregateMetric(metric, bucket, 60), nil)
}

func (a *ServerController) getXrayObservatory(c *gin.Context) {
	jsonObj(c, a.xrayMetricsService.ObservatorySnapshot(), nil)
}

func (a *ServerController) getXrayObservatoryHistoryBucket(c *gin.Context) {
	tag := c.Param("tag")
	if !a.xrayMetricsService.HasObservatoryTag(tag) {
		jsonMsg(c, "invalid tag", fmt.Errorf("unknown observatory tag"))
		return
	}
	bucket, ok := parseHistoryBucket(c)
	if !ok {
		return
	}
	jsonObj(c, a.xrayMetricsService.AggregateObservatory(tag, bucket, 60), nil)
}

func (a *ServerController) getXrayVersion(c *gin.Context) {
	versions, err := a.serverService.GetXrayVersionsCached()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "getVersion"), err)
		return
	}
	jsonObj(c, versions, nil)
}

// getPanelUpdateInfo retrieves the current and latest panel version.
func (a *ServerController) getPanelUpdateInfo(c *gin.Context) {
	info, err := a.panelService.GetUpdateInfo()
	if err != nil {
		logger.Debug("panel update check failed:", err)
		c.JSON(http.StatusOK, entity.Msg{Success: false})
		return
	}
	jsonObj(c, info, nil)
}

// installXray installs or updates Xray to the specified version.
func (a *ServerController) installXray(c *gin.Context) {
	version := c.Param("version")
	err := a.serverService.UpdateXray(version)
	jsonMsg(c, I18nWeb(c, "pages.index.xraySwitchVersionPopover"), err)
}

// updatePanel starts a self-update to the latest release and returns its runId for getUpdateStatus;
// dev=true (an older master's node updater) is refused, as still-ui publishes no dev builds.
func (a *ServerController) updatePanel(c *gin.Context) {
	devParam := c.PostForm("dev")
	var runID int64
	var err error
	if devParam == "" {
		runID, err = a.panelService.StartUpdate()
	} else {
		dev, perr := strconv.ParseBool(devParam)
		if perr != nil {
			jsonMsg(c, "invalid data", perr)
			return
		}
		runID, err = a.panelService.StartUpdateChannel(dev)
	}
	var obj any
	if err == nil {
		obj = gin.H{"runId": strconv.FormatInt(runID, 10)}
	}
	jsonMsgObj(c, I18nWeb(c, "pages.index.panelUpdateStartedPopover"), obj, err)
}

// getUpdateStatus reports the outcome of the most recently launched panel
// self-update (see updatePanel). Compare the returned runId against the one
// updatePanel returned to tell this run's result apart from a stale one.
func (a *ServerController) getUpdateStatus(c *gin.Context) {
	jsonObj(c, a.panelService.GetUpdateStatus(), nil)
}

// setUpdateChannel stores the update channel: only stable, as still-ui publishes no dev builds;
// turning off a dev channel an older release left on still works.
func (a *ServerController) setUpdateChannel(c *gin.Context) {
	dev, err := strconv.ParseBool(c.PostForm("dev"))
	if err != nil {
		jsonMsg(c, "invalid data", err)
		return
	}
	if dev {
		jsonMsg(c, I18nWeb(c, "pages.index.updateChannelChanged"), panel.ErrDevChannelUnavailable)
		return
	}
	err = a.settingService.SetDevChannelEnable(false)
	jsonMsg(c, I18nWeb(c, "pages.index.updateChannelChanged"), err)
}

// updateGeofile updates the specified geo file for Xray.
func (a *ServerController) updateGeofile(c *gin.Context) {
	fileName := c.Param("fileName")

	if fileName != "" && !a.serverService.IsValidGeofileName(fileName) {
		jsonMsg(c, I18nWeb(c, "pages.index.geofileUpdatePopover"),
			fmt.Errorf("invalid filename: contains unsafe characters or path traversal patterns"))
		return
	}

	err := a.serverService.UpdateGeofile(fileName)
	jsonMsg(c, I18nWeb(c, "pages.index.geofileUpdatePopover"), err)
}

// stopXrayService stops the Xray service.
func (a *ServerController) stopXrayService(c *gin.Context) {
	err := a.serverService.StopXrayService()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.xray.stopError"), err)
		websocket.BroadcastXrayState("error", err.Error())
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.xray.stopSuccess"), err)
	websocket.BroadcastXrayState("stop", "")
	websocket.BroadcastNotification(
		I18nWeb(c, "pages.xray.stopSuccess"),
		"Xray service has been stopped",
		"warning",
	)
}

// restartXrayService restarts the Xray service.
func (a *ServerController) restartXrayService(c *gin.Context) {
	err := a.serverService.RestartXrayService()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.xray.restartError"), err)
		websocket.BroadcastXrayState("error", err.Error())
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.xray.restartSuccess"), err)
	websocket.BroadcastXrayState("running", "")
	websocket.BroadcastNotification(
		I18nWeb(c, "pages.xray.restartSuccess"),
		"Xray service has been restarted successfully",
		"success",
	)
}

// getLogs retrieves the application logs based on count, level, and syslog filters.
func (a *ServerController) getLogs(c *gin.Context) {
	logs := a.serverService.GetLogs(c.Param("count"), c.PostForm("level"), c.PostForm("syslog"))
	jsonObj(c, logs, nil)
}

// getXrayLogs retrieves Xray logs with filtering options for direct, blocked, and proxy traffic.
func (a *ServerController) getXrayLogs(c *gin.Context) {
	freedoms, blackholes := a.serverService.GetDefaultLogOutboundTags()
	logs := a.serverService.GetXrayLogs(
		c.Param("count"),
		c.PostForm("filter"),
		c.PostForm("showDirect"),
		c.PostForm("showBlocked"),
		c.PostForm("showProxy"),
		freedoms,
		blackholes,
	)
	jsonObj(c, logs, nil)
}

// getAmneziaWGLogs retrieves the live AmneziaWG peer activity and the panel's
// own AmneziaWG event lines, optionally narrowed by a free-text filter.
func (a *ServerController) getAmneziaWGLogs(c *gin.Context) {
	logs := a.serverService.GetAmneziaWGLogs(c.Param("count"), c.PostForm("filter"))
	jsonObj(c, logs, nil)
}

// getConfigJson retrieves the Xray configuration as JSON.
func (a *ServerController) getConfigJson(c *gin.Context) {
	configJson, err := a.serverService.GetConfigJson()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.index.getConfigError"), err)
		return
	}
	jsonObj(c, configJson, nil)
}

// getLocalBackups lists the scheduled local backups and the outcome of the last one.
func (a *ServerController) getLocalBackups(c *gin.Context) {
	status, err := (&service.LocalBackupService{}).Status()
	jsonObj(c, status, err)
}

// runLocalBackup takes a local backup now, with the saved backup settings.
func (a *ServerController) runLocalBackup(c *gin.Context) {
	res, err := (&service.LocalBackupService{}).Run()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.backupRunFailed"), err)
		return
	}
	jsonObj(c, res, nil)
}

// getDb downloads the database file.
func (a *ServerController) getDb(c *gin.Context) {
	db, err := a.serverService.GetDb()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.index.getDatabaseError"), err)
		return
	}

	filename := a.serverService.BackupFilename(c.Request.Host)
	if !filenameRegex.MatchString(filename) {
		_ = c.AbortWithError(http.StatusBadRequest, fmt.Errorf("invalid filename"))
		return
	}

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename="+filename)
	_, _ = c.Writer.Write(db)
}

// getMigration downloads a cross-engine migration file: a .dump on SQLite or a
// .db SQLite database on PostgreSQL, so the data can seed the other backend.
func (a *ServerController) getMigration(c *gin.Context) {
	data, filename, err := a.serverService.GetMigration()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.index.getDatabaseError"), err)
		return
	}
	if !filenameRegex.MatchString(filename) {
		_ = c.AbortWithError(http.StatusBadRequest, fmt.Errorf("invalid filename"))
		return
	}

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename="+filename)
	_, _ = c.Writer.Write(data)
}

// importDB imports a database file and restarts the Xray service.
func (a *ServerController) importDB(c *gin.Context) {
	file, _, err := c.Request.FormFile("db")
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.index.readDatabaseError"), err)
		return
	}
	defer file.Close()
	// Absent field keeps this machine's own listen addresses, certificates and
	// node identity: the safe default for the common case of moving a config to
	// a new host. Send keepHostSettings=false to clone a machine wholesale.
	keepHostSettings := c.Request.FormValue("keepHostSettings") != "false"
	if err := a.serverService.ImportDB(file, keepHostSettings); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.index.importDatabaseError"), err)
		return
	}
	// Startup-registered routes (subPath) must match the restored DB, and the
	// browser's restartPanel follow-up can 401 once the imported users land (#6446).
	_ = a.panelService.RestartPanel(3 * time.Second)
	jsonObj(c, I18nWeb(c, "pages.index.importDatabaseSuccess"), nil)
}

// maxPanelImportBytes bounds the uploaded source database and Xray config together.
const maxPanelImportBytes = 512 << 20

// importPanel reports what an uploaded Marzban or x-ui database would bring into this
// panel and, with apply=true, imports it; the upload itself is only read.
func (a *ServerController) importPanel(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPanelImportBytes)
	file, _, err := c.Request.FormFile("db")
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.index.readDatabaseError"), err)
		return
	}
	defer file.Close()
	var xrayConfig []byte
	if cfg, _, err := c.Request.FormFile("config"); err == nil {
		xrayConfig, err = io.ReadAll(cfg)
		cfg.Close()
		if err != nil {
			jsonMsg(c, I18nWeb(c, "pages.index.readDatabaseError"), err)
			return
		}
	}
	apply := c.Request.FormValue("apply") == "true"
	opts := service.PanelImportOptions{
		DryRun: !apply, Merge: c.Request.FormValue("merge") == "true", UserId: session.GetLoginUser(c).Id,
	}
	report, err := (&service.PanelImportService{}).RunUpload(c.Request.FormValue("source"), file, xrayConfig, opts)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.importFailed"), err)
		return
	}
	if report.Applied {
		a.xrayService.SetToNeedRestart()
		notifyClientsChanged()
	}
	jsonObj(c, report, nil)
}

// descendants publishes read-only summaries of the nodes this panel manages so
// a parent panel can surface them as transitive sub-nodes in a chained
// topology. Called by the parent via the node's API token (#4983).
func (a *ServerController) descendants(c *gin.Context) {
	data, err := (&service.NodeService{}).LocalDescendants()
	jsonObj(c, data, err)
}

// getWebCertFiles returns this panel's own web TLS certificate and key file
// paths. The central panel calls it on a node (via the node's API token) so
// "Set Cert from Panel" can fill a node-assigned inbound with paths that exist
// on the node's filesystem instead of the central panel's — see issue #4854.
func (a *ServerController) getWebCertFiles(c *gin.Context) {
	certFile, err := a.settingService.GetCertFile()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	keyFile, err := a.settingService.GetKeyFile()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, gin.H{"webCertFile": certFile, "webKeyFile": keyFile}, nil)
}

// getNewX25519Cert generates a new X25519 certificate.
func (a *ServerController) getNewX25519Cert(c *gin.Context) {
	cert, err := a.serverService.GetNewX25519Cert()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.getNewX25519CertError"), err)
		return
	}
	jsonObj(c, cert, nil)
}

// getNewmldsa65 generates a new ML-DSA-65 key.
func (a *ServerController) getNewmldsa65(c *gin.Context) {
	cert, err := a.serverService.GetNewmldsa65()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.getNewmldsa65Error"), err)
		return
	}
	jsonObj(c, cert, nil)
}

// getNewEchCert generates a new ECH certificate for the given SNI.
func (a *ServerController) getNewEchCert(c *gin.Context) {
	cert, err := a.serverService.GetNewEchCert(c.PostForm("sni"))
	if err != nil {
		jsonMsg(c, "get ech certificate", err)
		return
	}
	jsonObj(c, cert, nil)
}

// getCertHash returns the hex SHA-256 of the given certificate (file path or
// inline content) so the panel can fill the pinned-cert field.
func (a *ServerController) getCertHash(c *gin.Context) {
	hashes, err := a.serverService.GetCertHash(c.PostForm("certFile"), c.PostForm("certContent"))
	if err != nil {
		jsonMsg(c, "get cert hash", err)
		return
	}
	jsonObj(c, hashes, nil)
}

// getRemoteCertHash runs `xray tls ping` against the given server and returns
// its live certificate SHA-256 hash(es) for pinning.
func (a *ServerController) getRemoteCertHash(c *gin.Context) {
	allowPrivate := c.PostForm("allowPrivate") == "true"
	hashes, err := a.serverService.GetRemoteCertHash(c.PostForm("server"), allowPrivate)
	if errors.Is(err, netsafe.ErrPrivateAddressBlocked) {
		jsonMsgObj(c, "get remote cert hash", gin.H{"privateTarget": true}, err)
		return
	}
	if err != nil {
		jsonMsg(c, "get remote cert hash", err)
		return
	}
	jsonObj(c, hashes, nil)
}

// scanRealityTarget probes the candidate REALITY target with the given sni and
// returns a feasibility verdict; allowPrivate is the panel's confirmed opt-in.
func (a *ServerController) scanRealityTarget(c *gin.Context) {
	xver, _ := strconv.Atoi(c.PostForm("xver"))
	allowPrivate := c.PostForm("allowPrivate") == "true"
	res, err := a.serverService.ScanRealityTarget(c.PostForm("target"), c.PostForm("sni"), xver, allowPrivate)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.scanRealityTargetError"), err)
		return
	}
	jsonObj(c, res, nil)
}

// scanRealityTargets probes the supplied comma-separated targets, or the
// realityScanCandidates setting when empty; neighbours adds the server's own /24.
func (a *ServerController) scanRealityTargets(c *gin.Context) {
	res, err := a.serverService.ScanRealityTargets(c.PostForm("targets"), c.PostForm("neighbours") == "true")
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.scanRealityTargetError"), err)
		return
	}
	jsonObj(c, res, nil)
}

// getNewVlessEnc generates a new VLESS encryption key.
func (a *ServerController) getNewVlessEnc(c *gin.Context) {
	out, err := a.serverService.GetNewVlessEnc()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.getNewVlessEncError"), err)
		return
	}
	jsonObj(c, out, nil)
}

// getNewAwgSignaturePacket builds an AmneziaWG I1 packet shaped like the preset's protocol.
func (a *ServerController) getNewAwgSignaturePacket(c *gin.Context) {
	out, err := a.serverService.GetNewAwgSignaturePacket(c.Query("preset"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.getNewAwgSignaturePacketError"), err)
		return
	}
	jsonObj(c, out, nil)
}

// getNewUUID generates a new UUID.
func (a *ServerController) getNewUUID(c *gin.Context) {
	uuidResp, err := a.serverService.GetNewUUID()
	if err != nil {
		jsonMsg(c, "Failed to generate UUID", err)
		return
	}
	jsonObj(c, uuidResp, nil)
}

// getNewmlkem768 generates a new ML-KEM-768 key.
func (a *ServerController) getNewmlkem768(c *gin.Context) {
	out, err := a.serverService.GetNewmlkem768()
	if err != nil {
		jsonMsg(c, "Failed to generate mlkem768 keys", err)
		return
	}
	jsonObj(c, out, nil)
}

func (a *ServerController) getClientIps(c *gin.Context) {
	ips, err := (&service.InboundService{}).GetAllInboundClientIps()
	jsonObj(c, ips, err)
}

func (a *ServerController) setClientIps(c *gin.Context) {
	var ips []model.InboundClientIps
	if err := c.ShouldBindJSON(&ips); err != nil {
		jsonMsg(c, "invalid data", err)
		return
	}
	err := (&service.InboundService{}).MergeInboundClientIps(ips)
	jsonMsg(c, "Client IPs merged", err)
}
