package controller

import (
	"strconv"
	"time"
	"x-ui/logger"

	"github.com/gin-gonic/gin"
)

type XUIController struct {
	BaseController

	inboundController  *InboundController
	tunnelController   *TunnelController
	caddyController    *CaddyController
	settingController  *SettingController
	endpointController *EndpointController
	mytrnController    *MyTRNController
}

func NewXUIController(g *gin.RouterGroup) *XUIController {
	a := &XUIController{}
	a.initRouter(g)
	return a
}

func (a *XUIController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/xui")
	g.Use(a.checkLogin)

	g.GET("/", a.index)
	g.GET("/inbounds", a.inbounds)
	g.GET("/tunnels", a.tunnels)
	g.GET("/caddy", a.caddy)
	g.GET("/subscription", a.subscription)
	g.GET("/setting", a.setting)

	a.inboundController = NewInboundController(g)
	a.tunnelController = NewTunnelController(g)
	a.caddyController = NewCaddyController(g)
	a.settingController = NewSettingController(g)
	a.endpointController = NewEndpointController(g)
	a.mytrnController = NewMyTRNController(g)
}

func (a *XUIController) index(c *gin.Context) {
	html(c, "index.html", "系统状态", nil)
}

func (a *XUIController) inbounds(c *gin.Context) {
	html(c, "inbounds.html", "入站列表", nil)
}

func (a *XUIController) tunnels(c *gin.Context) {
	started := time.Now()
	traceID := strconv.FormatInt(started.UnixNano(), 36)
	logger.Infof("[tunnel-trace] trace=%s event=page.start method=%s path=%s remote=%s", traceID, c.Request.Method, c.Request.URL.Path, c.ClientIP())
	defer func() {
		logger.Infof("[tunnel-trace] trace=%s event=page.end status=%d elapsed_ms=%.3f", traceID, c.Writer.Status(), float64(time.Since(started).Microseconds())/1000)
	}()
	html(c, "tunnels.html", "隧道列表", nil)
}

func (a *XUIController) caddy(c *gin.Context) {
	html(c, "caddy.html", "Caddy 配置", nil)
}

func (a *XUIController) subscription(c *gin.Context) {
	html(c, "subscription.html", "订阅 / 公网入口", nil)
}

func (a *XUIController) setting(c *gin.Context) {
	html(c, "setting.html", "设置", nil)
}
