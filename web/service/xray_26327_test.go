package service

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"x-ui/database"
	"x-ui/database/model"
)

func TestXray26327SavedTemplateGetsNativeStatsAPIWithoutChangingRouting(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "xray-26327.db")); err != nil {
		t.Fatal(err)
	}
	// This is the user's custom template shape: API with StatsService,
	// existing WARP domain routing, and no deprecated API inbound.
	raw := `{
		"api":{"tag":"api","services":["HandlerService","LoggerService","StatsService"]},
		"outbounds":[{"protocol":"freedom"},{"tag":"warp","protocol":"socks","settings":{"servers":[{"address":"127.0.0.1","port":40000}]}}],
		"routing":{"rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"},{"type":"field","domain":["domain:deepseek.com"],"outboundTag":"warp"}]},
		"policy":{"system":{"statsInboundDownlink":true,"statsInboundUplink":true}},
		"stats":{}
	}`
	if err := database.GetDB().Create(&model.Setting{Key: "xrayTemplateConfig", Value: raw}).Error; err != nil {
		t.Fatal(err)
	}
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	port, err := cfg.StatsAPIPort()
	if err != nil || port != 62789 {
		t.Fatalf("generated Xray 26.3.27 StatsService port: %d %v", port, err)
	}
	for _, inbound := range cfg.InboundConfigs {
		if inbound.Tag == "api" {
			t.Fatal("x-ui added old dokodemo-door API inbound to the generated config")
		}
	}
	var api struct {
		Listen string `json:"listen"`
	}
	if err := json.Unmarshal(cfg.API, &api); err != nil || api.Listen != "127.0.0.1:62789" {
		t.Fatalf("native API not configured: %v %+v", err, api)
	}
	if !strings.Contains(string(cfg.RouterConfig), "domain:deepseek.com") ||
		!strings.Contains(string(cfg.OutboundConfigs), "40000") {
		t.Fatal("Xray API provisioning changed the user's WARP vs freedom domain routing")
	}
	// The dynamically generated api.listen must NOT overwrite the template.
	stored, err := (&SettingService{}).GetXrayConfigTemplate()
	if err != nil || stored != raw {
		t.Fatalf("stored user Xray routing template was overwritten: %v", err)
	}
}
