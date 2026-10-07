package controller

import (
	"strconv"

	"x-ui/logger"
	"x-ui/web/entity"
	"x-ui/web/global"
	"x-ui/web/service"
	"x-ui/web/session"

	"github.com/gin-gonic/gin"
)

type EndpointController struct {
	endpointService service.EndpointService
	settingService  service.SettingService
}

type publishForm struct {
	Publish bool `json:"publish" form:"publish"`
}

func NewEndpointController(g *gin.RouterGroup) *EndpointController {
	a := &EndpointController{}
	a.initRouter(g)
	a.startTask()
	return a
}

func (a *EndpointController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/endpoint")
	g.POST("/list", a.list)
	g.POST("/settings", a.settings)
	g.POST("/settings/update", a.updateSettings)
	g.POST("/token/regenerate", a.regenerateToken)
	g.POST("/init-batch", a.initializeBatch)
	g.POST("/publish/:id", a.setPublish)
	g.POST("/link/:id", a.link)
	g.POST("/rotate-all", a.rotateAll)
	g.POST("/retire/:id", a.retire)
}

func (a *EndpointController) startTask() {
	webServer := global.GetWebServer()
	if webServer == nil {
		return
	}
	if err := a.endpointService.StartupReconcile(); err != nil {
		logger.Warning("managed endpoint startup reconcile failed; subscription and rotation are fail-closed:", err)
	}
	_, err := webServer.GetCron().AddFunc("@every 1m", func() {
		if err := a.endpointService.RetireExpired(); err != nil {
			logger.Warning("retire expired endpoints failed:", err)
		}
	})
	if err != nil {
		logger.Warning("register endpoint retire task failed:", err)
	}
}

func (a *EndpointController) list(c *gin.Context) {
	user := session.GetLoginUser(c)
	rows, err := a.endpointService.List(user.Id)
	jsonObj(c, rows, err)
}

func (a *EndpointController) settings(c *gin.Context) {
	settings, err := a.settingService.GetEndpointSettings()
	jsonObj(c, settings, err)
}

func (a *EndpointController) updateSettings(c *gin.Context) {
	form := &entity.EndpointSettings{}
	if err := c.ShouldBind(form); err != nil {
		jsonMsg(c, "保存订阅与入口设置", err)
		return
	}
	err := a.endpointService.UpdateSettings(form, c.Request.Host)
	jsonMsg(c, "保存订阅与入口设置", err)
}

func (a *EndpointController) regenerateToken(c *gin.Context) {
	token, err := a.settingService.RegenerateSubscriptionToken()
	jsonObj(c, token, err)
}

func (a *EndpointController) initializeBatch(c *gin.Context) {
	form := &service.EndpointBatchInit{}
	if err := c.ShouldBind(form); err != nil {
		jsonMsg(c, "批量初始化公网入口", err)
		return
	}
	user := session.GetLoginUser(c)
	result, err := a.endpointService.InitializeBatch(user.Id, form)
	jsonMsgObj(c, "批量初始化公网入口", result, err)
}

func (a *EndpointController) setPublish(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "修改订阅发布状态", err)
		return
	}
	form := &publishForm{}
	if err := c.ShouldBind(form); err != nil {
		jsonMsg(c, "修改订阅发布状态", err)
		return
	}
	user := session.GetLoginUser(c)
	err = a.endpointService.SetPublish(user.Id, id, form.Publish)
	jsonMsg(c, "修改订阅发布状态", err)
}

func (a *EndpointController) link(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "生成分享链接", err)
		return
	}
	user := session.GetLoginUser(c)
	link, err := a.endpointService.GetLink(user.Id, id)
	jsonObj(c, link, err)
}

func (a *EndpointController) rotateAll(c *gin.Context) {
	user := session.GetLoginUser(c)
	result, err := a.endpointService.RotateAll(user.Id)
	jsonMsgObj(c, "全部随机一次", result, err)
}

func (a *EndpointController) retire(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "下线旧入口", err)
		return
	}
	user := session.GetLoginUser(c)
	err = a.endpointService.Retire(user.Id, id)
	jsonMsg(c, "下线旧入口", err)
}
