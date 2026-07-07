package controller

import (
	"strconv"
	"x-ui/database/model"
	"x-ui/web/service"
	"x-ui/web/session"

	"github.com/gin-gonic/gin"
)

type TunnelController struct {
	tunnelService service.TunnelService
	xrayService   service.XrayService
}

func NewTunnelController(g *gin.RouterGroup) *TunnelController {
	a := &TunnelController{}
	a.initRouter(g)
	return a
}

func (a *TunnelController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/tunnel")

	g.POST("/list", a.getTunnels)
	g.POST("/add", a.addTunnel)
	g.POST("/del/:id", a.delTunnel)
	g.POST("/update/:id", a.updateTunnel)
}

func (a *TunnelController) getTunnels(c *gin.Context) {
	user := session.GetLoginUser(c)
	tunnels, err := a.tunnelService.GetTunnels(user.Id)
	if err != nil {
		jsonMsg(c, "获取", err)
		return
	}
	jsonObj(c, tunnels, nil)
}

func (a *TunnelController) addTunnel(c *gin.Context) {
	tunnel := &model.Tunnel{}
	err := c.ShouldBind(tunnel)
	if err != nil {
		jsonMsg(c, "添加", err)
		return
	}
	user := session.GetLoginUser(c)
	tunnel.UserId = user.Id
	tunnel.Enable = true
	err = a.tunnelService.AddTunnel(tunnel)
	jsonMsg(c, "添加", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}

func (a *TunnelController) delTunnel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "删除", err)
		return
	}
	err = a.tunnelService.DelTunnel(id)
	jsonMsg(c, "删除", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}

func (a *TunnelController) updateTunnel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "修改", err)
		return
	}
	tunnel := &model.Tunnel{
		Id: id,
	}
	err = c.ShouldBind(tunnel)
	if err != nil {
		jsonMsg(c, "修改", err)
		return
	}
	err = a.tunnelService.UpdateTunnel(tunnel)
	jsonMsg(c, "修改", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}
