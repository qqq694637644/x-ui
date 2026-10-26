package controller

import (
	"x-ui/web/service"

	"github.com/gin-gonic/gin"
)

// ReverseQUICController owns a separate authenticated UI/API for the overseas
// Xray Bridge. It does not touch the existing FRP reverse-SOCKS endpoints.
type ReverseQUICController struct {
	service *service.ReverseQUICBridgeService
}

func NewReverseQUICController(g *gin.RouterGroup) *ReverseQUICController {
	a := &ReverseQUICController{service: &service.ReverseQUICBridgeService{}}
	g = g.Group("/reverse-quic")
	g.POST("/config", a.get)
	g.POST("/save", a.save)
	return a
}

func (a *ReverseQUICController) get(c *gin.Context) {
	cfg, err := a.service.Get()
	jsonObj(c, cfg, err)
}

func (a *ReverseQUICController) save(c *gin.Context) {
	var cfg service.ReverseQUICBridgeConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		jsonMsg(c, "读取 QUIC Brutal 反向 Bridge 表单", err)
		return
	}
	saved, err := a.service.Save(cfg)
	jsonMsgObj(c, "保存并应用境外 QUIC Brutal Bridge", saved, err)
}
