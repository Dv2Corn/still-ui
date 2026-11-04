package controller

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// ReachProbeController is the admin side of external reachability probes:
// /panel/api/probes. The agents themselves talk to ReachProbeAgentController.
type ReachProbeController struct {
	probeService service.ReachProbeService
	xrayService  service.XrayService
}

func NewReachProbeController(g *gin.RouterGroup) *ReachProbeController {
	a := &ReachProbeController{}
	g.GET("/list", a.list)
	g.GET("/history/:id", a.history)
	g.POST("/add", a.add)
	g.POST("/update/:id", a.update)
	g.POST("/del/:id", a.del)
	g.POST("/rotateToken/:id", a.rotateToken)
	return a
}

func (a *ReachProbeController) list(c *gin.Context) {
	probes, err := a.probeService.List()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, probes, nil)
}

func (a *ReachProbeController) history(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	points, err := a.probeService.History(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, points, nil)
}

func (a *ReachProbeController) add(c *gin.Context) {
	var in service.ReachProbeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	created, needRestart, err := a.probeService.Create(in)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	if needRestart || err == nil {
		notifyClientsChanged()
	}
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	c.Header("Cache-Control", "no-store")
	jsonMsgObj(c, I18nWeb(c, "pages.probes.toasts.saved"), created, nil)
}

func (a *ReachProbeController) update(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	var in service.ReachProbeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	needRestart, err := a.probeService.Update(id, in)
	if needRestart {
		a.xrayService.SetToNeedRestart()
		notifyClientsChanged()
	}
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.probes.toasts.saved"), nil)
}

func (a *ReachProbeController) del(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	needRestart, err := a.probeService.Delete(id)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	if needRestart || err == nil {
		notifyClientsChanged()
	}
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.probes.toasts.deleted"), nil)
}

func (a *ReachProbeController) rotateToken(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	token, needRestart, err := a.probeService.RotateToken(id)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	if needRestart || err == nil {
		notifyClientsChanged()
	}
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	c.Header("Cache-Control", "no-store")
	jsonObj(c, gin.H{"token": token}, nil)
}
