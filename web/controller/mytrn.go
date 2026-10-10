package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/gin-gonic/gin"
	"x-ui/web/service"
)

// MyTRN is displayed in the existing inbound table, but is not an Xray
// inbound: this controller owns its separate single-instance service model.
type MyTRNController struct {
	mytrnService service.MyTRNService
	xrayService  service.XrayService
}

func NewMyTRNController(g *gin.RouterGroup) *MyTRNController {
	a := &MyTRNController{}
	routes := g.Group("/mytrn")
	routes.POST("/get", a.get)
	routes.POST("/save", a.save)
	return a
}

func (a *MyTRNController) get(c *gin.Context) {
	view, err := a.mytrnService.View()
	jsonObj(c, view, err)
}

func (a *MyTRNController) save(c *gin.Context) {
	// Explicit JSON-only input avoids accidentally accepting form-encoded
	// fields that can clobber the server-maintained endpoint/certificate.
	if c.ContentType() != "application/json" {
		jsonMsg(c, "保存 MyTRN", errors.New("需要 application/json"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, 16*1024+1))
	if err != nil || len(data) > 16*1024 {
		jsonMsg(c, "保存 MyTRN", errors.New("请求体过长或无法读取"))
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config service.MyTRNSettings
	if err := decoder.Decode(&config); err != nil {
		jsonMsg(c, "保存 MyTRN", err)
		return
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		jsonMsg(c, "保存 MyTRN", errors.New("额外 JSON 数据"))
		return
	}
	needsRestart, err := a.mytrnService.UpdateSettings(config)
	if err == nil && needsRestart {
		a.xrayService.SetToNeedRestart()
	}
	jsonMsg(c, "保存 MyTRN", err)
}
