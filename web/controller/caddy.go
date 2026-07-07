package controller

import (
	"x-ui/web/service"

	"github.com/gin-gonic/gin"
)

type CaddyController struct {
	caddyService service.CaddyService
}

type caddyPathForm struct {
	Path string `json:"path" form:"path"`
}

type caddyContentForm struct {
	Content string `json:"content" form:"content"`
}

func NewCaddyController(g *gin.RouterGroup) *CaddyController {
	a := &CaddyController{}
	a.initRouter(g)
	return a
}

func (a *CaddyController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/caddy")

	g.POST("/config", a.getConfig)
	g.POST("/path", a.updatePath)
	g.POST("/validate", a.validate)
	g.POST("/save", a.save)
	g.POST("/reload", a.reload)
	g.POST("/saveReload", a.saveReload)
}

func (a *CaddyController) getConfig(c *gin.Context) {
	config, err := a.caddyService.GetConfig()
	jsonObj(c, config, err)
}

func (a *CaddyController) updatePath(c *gin.Context) {
	form := &caddyPathForm{}
	err := c.ShouldBind(form)
	if err != nil {
		jsonMsg(c, "保存 Caddy 路径", err)
		return
	}
	err = a.caddyService.SetPath(form.Path)
	jsonMsg(c, "保存 Caddy 路径", err)
}

func (a *CaddyController) validate(c *gin.Context) {
	form := &caddyContentForm{}
	err := c.ShouldBind(form)
	if err != nil {
		jsonMsg(c, "校验 Caddyfile", err)
		return
	}
	result, err := a.caddyService.Validate(form.Content)
	jsonMsgObj(c, "校验 Caddyfile", result, err)
}

func (a *CaddyController) save(c *gin.Context) {
	form := &caddyContentForm{}
	err := c.ShouldBind(form)
	if err != nil {
		jsonMsg(c, "保存 Caddyfile", err)
		return
	}
	config, err := a.caddyService.Save(form.Content)
	jsonMsgObj(c, "保存 Caddyfile", config, err)
}

func (a *CaddyController) reload(c *gin.Context) {
	result, err := a.caddyService.Reload()
	jsonMsgObj(c, "Reload Caddy", result, err)
}

func (a *CaddyController) saveReload(c *gin.Context) {
	form := &caddyContentForm{}
	err := c.ShouldBind(form)
	if err != nil {
		jsonMsg(c, "保存并 Reload Caddy", err)
		return
	}
	result, err := a.caddyService.SaveAndReload(form.Content)
	jsonMsgObj(c, "保存并 Reload Caddy", result, err)
}
