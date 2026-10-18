package controller

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"x-ui/rsxdiag"
	"x-ui/web/service"
)

// Registered on the existing authenticated /xui/reverse-socks group; no new
// public or unauthenticated management server is created.
func (a *ReverseSocksController) registerDiagnostics(g *gin.RouterGroup) {
	g = g.Group("/diagnostics")
	g.POST("/snapshot", a.diagnosticSnapshot)
	g.POST("/history", a.diagnosticHistory)
	g.POST("/settings", a.diagnosticSettings)
	g.POST("/task", a.diagnosticTask)
	g.POST("/cancel", a.diagnosticCancel)
	g.POST("/trace", a.diagnosticTrace)
	g.POST("/reports", a.diagnosticReports)
	g.POST("/delete-report", a.diagnosticDeleteReport)
	g.GET("/download/:id/:format", a.diagnosticDownload)
}
func (a *ReverseSocksController) diagnosticSnapshot(c *gin.Context) {
	d, err := a.service.Diagnostics()
	if err != nil {
		jsonObj(c, map[string]interface{}{"enabled": false, "error": err.Error()}, nil)
		return
	}
	jsonObj(c, d.Snapshot(), nil)
}
func (a *ReverseSocksController) diagnosticHistory(c *gin.Context) {
	d, err := a.service.Diagnostics()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	var request struct {
		Minutes int `json:"minutes"`
	}
	if err = c.ShouldBindJSON(&request); err != nil {
		jsonObj(c, nil, err)
		return
	}
	if request.Minutes < 1 || request.Minutes > 43200 {
		jsonObj(c, nil, fmt.Errorf("history range must be 1..43200 minutes"))
		return
	}
	result, err := d.History(time.Duration(request.Minutes) * time.Minute)
	jsonObj(c, result, err)
}
func (a *ReverseSocksController) diagnosticSettings(c *gin.Context) {
	d, err := a.service.Diagnostics()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	var settings service.DiagnosticsSettings
	if err = c.ShouldBindJSON(&settings); err != nil {
		jsonObj(c, nil, err)
		return
	}
	err = d.SetSettings(settings)
	jsonObj(c, d.Settings(), err)
}
func (a *ReverseSocksController) diagnosticTask(c *gin.Context) {
	d, err := a.service.Diagnostics()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	var spec rsxdiag.TaskSpec
	if err = c.ShouldBindJSON(&spec); err != nil {
		jsonObj(c, nil, err)
		return
	}
	task, err := d.StartTask(spec)
	jsonObj(c, task, err)
}
func (a *ReverseSocksController) diagnosticCancel(c *gin.Context) {
	d, err := a.service.Diagnostics()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	var request struct {
		ID string `json:"id"`
	}
	if err = c.ShouldBindJSON(&request); err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonMsg(c, "取消诊断任务", d.CancelTask(request.ID))
}
func (a *ReverseSocksController) diagnosticTrace(c *gin.Context) {
	d, err := a.service.Diagnostics()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	var request struct {
		Key string `json:"key"`
	}
	if err = c.ShouldBindJSON(&request); err != nil {
		jsonObj(c, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	result, err := d.TraceSession(ctx, request.Key)
	jsonObj(c, result, err)
}
func (a *ReverseSocksController) diagnosticReports(c *gin.Context) {
	d, err := a.service.Diagnostics()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	reports, err := rsxdiag.ListReports(d.Root())
	jsonObj(c, reports, err)
}
func (a *ReverseSocksController) diagnosticDeleteReport(c *gin.Context) {
	d, err := a.service.Diagnostics()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	var request struct {
		ID string `json:"id"`
	}
	if err = c.ShouldBindJSON(&request); err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonMsg(c, "删除原始报告", rsxdiag.DeleteReport(d.Root(), request.ID))
}
func (a *ReverseSocksController) diagnosticDownload(c *gin.Context) {
	d, err := a.service.Diagnostics()
	if err != nil {
		c.String(http.StatusServiceUnavailable, "diagnostics unavailable")
		return
	}
	path, err := rsxdiag.ReportPath(d.Root(), c.Param("id"), c.Param("format"))
	if err != nil {
		c.String(http.StatusNotFound, "report not found")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		c.String(http.StatusNotFound, "report not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"rsx3-%s.%s\"", c.Param("id"), c.Param("format")))
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), f)
}
