package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/web/service"
	"x-ui/web/session"

	"github.com/gin-gonic/gin"
)

type TunnelController struct {
	tunnelService service.TunnelService
	xrayService   service.XrayService
}

var deprecatedTunnelRequestFields = []string{"kcpHeaderType", "kcpSeed"}

func firstDeprecatedTunnelField(values url.Values) string {
	for _, field := range deprecatedTunnelRequestFields {
		if _, ok := values[field]; ok {
			return field
		}
	}
	return ""
}

func firstDeprecatedTunnelJSONField(body []byte) string {
	payload := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, field := range deprecatedTunnelRequestFields {
		if _, ok := payload[field]; ok {
			return field
		}
	}
	return ""
}

func rejectDeprecatedTunnelFields(body []byte, rawQuery string) error {
	if field := firstDeprecatedTunnelJSONField(bytes.TrimSpace(body)); field != "" {
		return errors.New("Xray 26 已移除 mKCP header/seed，不再接受字段: " + field)
	}
	if values, err := url.ParseQuery(string(body)); err == nil {
		if field := firstDeprecatedTunnelField(values); field != "" {
			return errors.New("Xray 26 已移除 mKCP header/seed，不再接受字段: " + field)
		}
	}
	if values, err := url.ParseQuery(rawQuery); err == nil {
		if field := firstDeprecatedTunnelField(values); field != "" {
			return errors.New("Xray 26 已移除 mKCP header/seed，不再接受字段: " + field)
		}
	}
	return nil
}

func rejectDeprecatedTunnelRequestFields(c *gin.Context) error {
	body, err := c.GetRawData()
	if err != nil {
		return err
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))
	return rejectDeprecatedTunnelFields(body, c.Request.URL.RawQuery)
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
	g.POST("/test/:id", a.testTunnel)
	g.POST("/apply", a.applyTunnels)
}

func (a *TunnelController) applySavedChange(c *gin.Context, action string, err error) {
	if err != nil {
		jsonMsg(c, action, err)
		return
	}
	applyErr := a.xrayService.RestartXray(false)
	if applyErr != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"msg":     action + "成功，但 Xray 应用失败: " + applyErr.Error(),
			"obj":     gin.H{"saved": true, "applied": false, "error": applyErr.Error()},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"msg":     action + "并应用成功",
		"obj":     gin.H{"saved": true, "applied": true},
	})
}

func (a *TunnelController) getTunnels(c *gin.Context) {
	started := time.Now()
	traceID := strconv.FormatInt(started.UnixNano(), 36)
	user := session.GetLoginUser(c)
	logger.Infof("[tunnel-trace] trace=%s event=list.start user_id=%d method=%s path=%s remote=%s", traceID, user.Id, c.Request.Method, c.Request.URL.Path, c.ClientIP())

	serviceStarted := time.Now()
	tunnels, err := a.tunnelService.GetTunnelsTraced(user.Id, traceID)
	serviceElapsed := time.Since(serviceStarted)
	c.Header("X-XUI-Tunnel-Trace", traceID)
	c.Header("Server-Timing", fmt.Sprintf("tunnel-service;dur=%.3f", float64(serviceElapsed.Microseconds())/1000))
	if err != nil {
		logger.Infof("[tunnel-trace] trace=%s event=list.end success=false service_ms=%.3f total_ms=%.3f error=%q", traceID, float64(serviceElapsed.Microseconds())/1000, float64(time.Since(started).Microseconds())/1000, err.Error())
		jsonMsg(c, "获取", err)
		return
	}
	logger.Infof("[tunnel-trace] trace=%s event=list.end success=true count=%d service_ms=%.3f total_ms=%.3f", traceID, len(tunnels), float64(serviceElapsed.Microseconds())/1000, float64(time.Since(started).Microseconds())/1000)
	jsonObj(c, tunnels, nil)
}

func (a *TunnelController) addTunnel(c *gin.Context) {
	if err := rejectDeprecatedTunnelRequestFields(c); err != nil {
		jsonMsg(c, "添加", err)
		return
	}
	tunnel := &model.Tunnel{}
	err := c.ShouldBind(tunnel)
	if err != nil {
		jsonMsg(c, "添加", err)
		return
	}
	user := session.GetLoginUser(c)
	tunnel.UserId = user.Id
	err = a.tunnelService.AddTunnel(tunnel)
	a.applySavedChange(c, "添加", err)
}

func (a *TunnelController) delTunnel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "删除", err)
		return
	}
	user := session.GetLoginUser(c)
	err = a.tunnelService.DelTunnel(id, user.Id)
	a.applySavedChange(c, "删除", err)
}

func (a *TunnelController) updateTunnel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "修改", err)
		return
	}
	if err := rejectDeprecatedTunnelRequestFields(c); err != nil {
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
	user := session.GetLoginUser(c)
	err = a.tunnelService.UpdateTunnel(tunnel, user.Id)
	a.applySavedChange(c, "修改", err)
}

func (a *TunnelController) testTunnel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "测试", err)
		return
	}
	user := session.GetLoginUser(c)
	tunnel, err := a.tunnelService.ProbeTunnel(id, user.Id)
	jsonMsgObj(c, "测试", tunnel, err)
}

func (a *TunnelController) applyTunnels(c *gin.Context) {
	err := a.xrayService.RestartXray(false)
	jsonMsg(c, "应用隧道配置", err)
}
