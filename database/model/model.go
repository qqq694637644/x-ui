package model

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"x-ui/util/json_util"
	"x-ui/xray"
)

type Protocol string

const (
	VMess       Protocol = "vmess"
	VLESS       Protocol = "vless"
	Dokodemo    Protocol = "Dokodemo-door"
	Http        Protocol = "http"
	Trojan      Protocol = "trojan"
	Shadowsocks Protocol = "shadowsocks"
)

type User struct {
	Id       int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type Inbound struct {
	Id         int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	UserId     int    `json:"-"`
	Up         int64  `json:"up" form:"up"`
	Down       int64  `json:"down" form:"down"`
	Total      int64  `json:"total" form:"total"`
	Remark     string `json:"remark" form:"remark"`
	Enable     bool   `json:"enable" form:"enable"`
	ExpiryTime int64  `json:"expiryTime" form:"expiryTime"`

	// config part
	Listen         string   `json:"listen" form:"listen"`
	Port           int      `json:"port" form:"port"`
	Protocol       Protocol `json:"protocol" form:"protocol"`
	Settings       string   `json:"settings" form:"settings"`
	StreamSettings string   `json:"streamSettings" form:"streamSettings"`
	Tag            string   `json:"tag" form:"tag" gorm:"unique"`
	Sniffing       string   `json:"sniffing" form:"sniffing"`
}

type Tunnel struct {
	Id     int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	UserId int    `json:"-"`
	Enable bool   `json:"enable" form:"enable"`
	Mode   string `json:"mode" form:"mode"`

	Remark string `json:"remark" form:"remark"`

	Listen     string `json:"listen" form:"listen"`
	ListenPort int    `json:"listenPort" form:"listenPort"`
	Network    string `json:"network" form:"network"`

	TargetAddress string `json:"targetAddress" form:"targetAddress"`
	TargetPort    int    `json:"targetPort" form:"targetPort"`

	RemoteAddress string `json:"remoteAddress" form:"remoteAddress"`
	RemotePort    int    `json:"remotePort" form:"remotePort"`

	Protocol string `json:"protocol" form:"protocol"`
	UUID     string `json:"uuid" form:"uuid"`

	KcpFinalMaskType    string `json:"kcpFinalMaskType" form:"kcpFinalMaskType" gorm:"column:kcp_final_mask_type"`
	KcpMtu              int    `json:"kcpMtu" form:"kcpMtu"`
	KcpTti              int    `json:"kcpTti" form:"kcpTti"`
	KcpUplinkCapacity   int    `json:"kcpUplinkCapacity" form:"kcpUplinkCapacity"`
	KcpDownlinkCapacity int    `json:"kcpDownlinkCapacity" form:"kcpDownlinkCapacity"`
	KcpCongestion       bool   `json:"kcpCongestion" form:"kcpCongestion"`
	KcpReadBufferSize   int    `json:"kcpReadBufferSize" form:"kcpReadBufferSize"`
	KcpWriteBufferSize  int    `json:"kcpWriteBufferSize" form:"kcpWriteBufferSize"`

	Status        string `json:"status" form:"-" gorm:"-"`
	StatusMessage string `json:"statusMessage" form:"-" gorm:"-"`
	ProbeTime     string `json:"probeTime" form:"-" gorm:"-"`
}

func NormalizeUUID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if length := len([]byte(value)); length >= 1 && length <= 30 {
		hash := sha1.New()
		_, _ = hash.Write(make([]byte, 16))
		_, _ = hash.Write([]byte(value))
		bytes := hash.Sum(nil)[:16]
		bytes[6] = (bytes[6] & 0x0f) | (5 << 4)
		bytes[8] = (bytes[8] & (0xff >> 2)) | (0x02 << 6)
		return formatUUIDBytes(bytes), nil
	}

	value = strings.ToLower(value)
	compact := value
	if len(value) == 36 {
		for _, index := range []int{8, 13, 18, 23} {
			if value[index] != '-' {
				return "", fmt.Errorf("UUID 必须是 1-30 字节旧 ID、32 位十六进制或标准 36 位格式")
			}
		}
		compact = strings.ReplaceAll(value, "-", "")
	}
	if len(compact) != 32 {
		return "", fmt.Errorf("UUID 必须是 1-30 字节旧 ID、32 位十六进制或标准 36 位格式")
	}
	decoded := make([]byte, 16)
	if _, err := hex.Decode(decoded, []byte(compact)); err != nil {
		return "", fmt.Errorf("UUID 包含非十六进制字符: %w", err)
	}
	return formatUUIDBytes(decoded), nil
}

func formatUUIDBytes(value []byte) string {
	encoded := hex.EncodeToString(value)
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32])
}

func (t *Tunnel) InboundTag() string {
	return fmt.Sprintf("tunnel-in-%v", t.Id)
}

func (t *Tunnel) OutboundTag() string {
	return fmt.Sprintf("tunnel-out-%v", t.Id)
}

func (t *Tunnel) PortalInboundTag() string {
	return fmt.Sprintf("tunnel-portal-in-%v", t.Id)
}

func (t *Tunnel) PortalTag() string {
	return fmt.Sprintf("tunnel-portal-%v", t.Id)
}

func (t *Tunnel) ReverseDomain() string {
	normalized, err := NormalizeUUID(t.UUID)
	if err == nil {
		return fmt.Sprintf("reverse-%s.xui.internal", normalized)
	}
	return fmt.Sprintf("reverse-%s.xui.internal", strings.ToLower(strings.TrimSpace(t.UUID)))
}

func (i *Inbound) GenXrayInboundConfig() *xray.InboundConfig {
	listen := i.Listen
	if listen != "" {
		listen = fmt.Sprintf("\"%v\"", listen)
	}
	return &xray.InboundConfig{
		Listen:         json_util.RawMessage(listen),
		Port:           i.Port,
		Protocol:       string(i.Protocol),
		Settings:       json_util.RawMessage(i.Settings),
		StreamSettings: json_util.RawMessage(i.StreamSettings),
		Tag:            i.Tag,
		Sniffing:       json_util.RawMessage(i.Sniffing),
	}
}

type Setting struct {
	Id    int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Key   string `json:"key" form:"key"`
	Value string `json:"value" form:"value"`
}
