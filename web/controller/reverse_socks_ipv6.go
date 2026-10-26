package controller

import (
	"github.com/gin-gonic/gin"
	"x-ui/web/service"
)

// Existing reverse-socks controller and routes are intentionally untouched.
type DirectSocksController struct {
	svc *service.DirectSocksService
}

func NewDirectSocksController(g *gin.RouterGroup, svc *service.DirectSocksService) *DirectSocksController {
	a := &DirectSocksController{svc: svc}
	g = g.Group("/reverse-socks-ipv6")
	g.POST("/config", a.get)
	g.POST("/save", a.save)
	g.POST("/start", a.start)
	g.POST("/stop", a.stop)
	g.POST("/restart", a.restart)
	return a
}
func (a *DirectSocksController) get(c *gin.Context) {
	v, err := a.svc.Get()
	jsonObj(c, v, err)
}
func (a *DirectSocksController) save(c *gin.Context) {
	var cfg service.DirectSocksSettings
	if err := c.ShouldBind(&cfg); err != nil {
		jsonMsg(c, "保存 IPv6 DDNS 反向 SOCKS5", err)
		return
	}
	v, err := a.svc.Save(cfg)
	jsonMsgObj(c, "保存 IPv6 DDNS 反向 SOCKS5", v, err)
}
func (a *DirectSocksController) start(c *gin.Context) {
	v, err := a.svc.SetEnabled(true)
	jsonMsgObj(c, "启用 IPv6 DDNS 反向 SOCKS5", v, err)
}
func (a *DirectSocksController) stop(c *gin.Context) {
	v, err := a.svc.SetEnabled(false)
	jsonMsgObj(c, "停用 IPv6 DDNS 反向 SOCKS5", v, err)
}
func (a *DirectSocksController) restart(c *gin.Context) {
	v, err := a.svc.Restart()
	jsonMsgObj(c, "重连 IPv6 DDNS 反向 SOCKS5", v, err)
}
