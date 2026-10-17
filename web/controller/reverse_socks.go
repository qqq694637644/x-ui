package controller

import (
	"x-ui/web/service"

	"github.com/gin-gonic/gin"
)

type ReverseSocksController struct {
	service *service.ReverseSocksService
}

func NewReverseSocksController(g *gin.RouterGroup, agent *service.ReverseSocksService) *ReverseSocksController {
	a := &ReverseSocksController{service: agent}
	g = g.Group("/reverse-socks")
	g.POST("/config", a.get)
	g.POST("/save", a.save)
	g.POST("/start", a.start)
	g.POST("/stop", a.stop)
	g.POST("/verify-frpc", a.verifyFRPC)
	g.POST("/restart", a.restart)
	return a
}

func (a *ReverseSocksController) get(c *gin.Context) {
	view, err := a.service.Get()
	jsonObj(c, view, err)
}

func (a *ReverseSocksController) save(c *gin.Context) {
	var form service.ReverseSocksForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, "保存反向 SOCKS5", err)
		return
	}
	view, err := a.service.Save(form)
	jsonMsgObj(c, "保存反向 SOCKS5", view, err)
}

func (a *ReverseSocksController) start(c *gin.Context) {
	view, err := a.service.SetEnabled(true)
	jsonMsgObj(c, "启用反向 SOCKS5", view, err)
}

func (a *ReverseSocksController) stop(c *gin.Context) {
	view, err := a.service.SetEnabled(false)
	jsonMsgObj(c, "停止反向 SOCKS5", view, err)
}

func (a *ReverseSocksController) verifyFRPC(c *gin.Context) {
	jsonMsg(c, "校验自定义 frpc", a.service.VerifyFRPC())
}

func (a *ReverseSocksController) restart(c *gin.Context) {
	view, err := a.service.Restart()
	jsonMsgObj(c, "重启自定义 frpc 和出口 Agent", view, err)
}
