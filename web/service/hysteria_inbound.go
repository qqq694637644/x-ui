package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"x-ui/database/model"
)

// Hysteria2 is already implemented by Xray-core 26.3.27. The panel must
// validate and pass its native config through, never create a second QUIC
// implementation. A force-brutal profile must not silently fall back to BBR.
var hysteriaBrutalRate = regexp.MustCompile(`^([1-9][0-9]{0,4})(?:\.[0-9]{1,2})?mbps$`)

func validateHysteriaBrutalRate(rate string) error {
	rate = strings.ToLower(strings.TrimSpace(rate))
	if !hysteriaBrutalRate.MatchString(rate) {
		return fmt.Errorf("Brutal 速率必须是 1mbps 到 99999.99mbps，例如 16mbps")
	}
	number, err := strconv.ParseFloat(strings.TrimSuffix(rate, "mbps"), 64)
	if err != nil || number < 1 || number >= 100000 {
		return fmt.Errorf("Brutal 速率必须在 1 到 99999.99 Mbps 之间")
	}
	return nil
}

func validateHysteria2Inbound(inbound *model.Inbound) error {
	if inbound.Port <= 0 || inbound.Port > 65535 {
		return fmt.Errorf("Hysteria2 UDP 端口无效")
	}

	var account struct {
		Version int `json:"version"`
		Clients []struct {
			Auth string `json:"auth"`
		} `json:"clients"`
	}
	if err := json.Unmarshal([]byte(inbound.Settings), &account); err != nil {
		return fmt.Errorf("Hysteria2 客户端认证配置无效: %w", err)
	}
	if account.Version != 2 || len(account.Clients) == 0 {
		return fmt.Errorf("Hysteria2 需要 version=2 和至少一个认证客户端")
	}
	secrets := make(map[string]bool, len(account.Clients))
	for _, client := range account.Clients {
		if len(client.Auth) < 16 || len(client.Auth) > 1024 {
			return fmt.Errorf("Hysteria2 每个客户端需要 16 到 1024 字节的强随机 auth")
		}
		if secrets[client.Auth] {
			return fmt.Errorf("Hysteria2 客户端的 auth 不能重复")
		}
		secrets[client.Auth] = true
	}

	var stream struct {
		Network  string `json:"network"`
		Security string `json:"security"`
		TLS      struct {
			ALPN         []string `json:"alpn"`
			Certificates []struct {
				CertificateFile string   `json:"certificateFile"`
				KeyFile         string   `json:"keyFile"`
				Certificate     []string `json:"certificate"`
				Key             []string `json:"key"`
			} `json:"certificates"`
		} `json:"tlsSettings"`
		Hysteria struct {
			Version int    `json:"version"`
			Auth    string `json:"auth"`
		} `json:"hysteriaSettings"`
		FinalMask struct {
			QuicParams struct {
				Congestion string `json:"congestion"`
				BrutalUp   string `json:"brutalUp"`
				BrutalDown string `json:"brutalDown"`
			} `json:"quicParams"`
		} `json:"finalmask"`
	}
	if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
		return fmt.Errorf("Hysteria2 传输配置无效: %w", err)
	}
	if stream.Network != "hysteria" || stream.Security != "tls" || stream.Hysteria.Version != 2 {
		return fmt.Errorf("Hysteria2 必须使用 network=hysteria、security=tls、hysteriaSettings.version=2")
	}
	if stream.Hysteria.Auth != "" {
		return fmt.Errorf("Hysteria2 请仅在 settings.clients 配置 auth，避免两份认证密钥分叉")
	}
	if len(stream.TLS.ALPN) != 1 || stream.TLS.ALPN[0] != "h3" {
		return fmt.Errorf("Hysteria2 TLS 必须使用 alpn=[\"h3\"]")
	}
	if len(stream.TLS.Certificates) == 0 {
		return fmt.Errorf("Hysteria2 TLS 必须配置证书和私钥")
	}
	for _, cert := range stream.TLS.Certificates {
		withFiles := strings.TrimSpace(cert.CertificateFile) != "" && strings.TrimSpace(cert.KeyFile) != ""
		withContent := len(cert.Certificate) > 0 && len(cert.Key) > 0
		if !withFiles && !withContent {
			return fmt.Errorf("Hysteria2 TLS 证书和私钥必须成对配置")
		}
	}
	if stream.FinalMask.QuicParams.Congestion != "force-brutal" {
		return fmt.Errorf("Hysteria2 直连仅支持 finalmask.quicParams.congestion=force-brutal，不允许回退 BBR")
	}
	for _, entry := range []struct{ name, rate string }{
		{"brutalUp", stream.FinalMask.QuicParams.BrutalUp},
		{"brutalDown", stream.FinalMask.QuicParams.BrutalDown},
	} {
		if err := validateHysteriaBrutalRate(entry.rate); err != nil {
			return fmt.Errorf("%s: %w", entry.name, err)
		}
	}
	return nil
}
