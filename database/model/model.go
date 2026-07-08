package model

import (
	"fmt"
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
	Port           int      `json:"port" form:"port" gorm:"unique"`
	Protocol       Protocol `json:"protocol" form:"protocol"`
	Settings       string   `json:"settings" form:"settings"`
	StreamSettings string   `json:"streamSettings" form:"streamSettings"`
	Tag            string   `json:"tag" form:"tag" gorm:"unique"`
	Sniffing       string   `json:"sniffing" form:"sniffing"`
}

type Tunnel struct {
	Id     int  `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	UserId int  `json:"-"`
	Enable bool `json:"enable" form:"enable"`

	Remark string `json:"remark" form:"remark"`

	Listen     string `json:"listen" form:"listen"`
	ListenPort int    `json:"listenPort" form:"listenPort" gorm:"unique"`
	Network    string `json:"network" form:"network"`

	TargetAddress string `json:"targetAddress" form:"targetAddress"`
	TargetPort    int    `json:"targetPort" form:"targetPort"`

	RemoteAddress string `json:"remoteAddress" form:"remoteAddress"`
	RemotePort    int    `json:"remotePort" form:"remotePort"`

	Protocol string `json:"protocol" form:"protocol"`
	UUID     string `json:"uuid" form:"uuid"`

	KcpFinalMask        string `json:"kcpFinalMask" form:"kcpFinalMask" gorm:"column:kcp_final_mask"`
	KcpMtu              int    `json:"kcpMtu" form:"kcpMtu"`
	KcpTti              int    `json:"kcpTti" form:"kcpTti"`
	KcpUplinkCapacity   int    `json:"kcpUplinkCapacity" form:"kcpUplinkCapacity"`
	KcpDownlinkCapacity int    `json:"kcpDownlinkCapacity" form:"kcpDownlinkCapacity"`
	KcpCongestion       bool   `json:"kcpCongestion" form:"kcpCongestion"`
	KcpReadBufferSize   int    `json:"kcpReadBufferSize" form:"kcpReadBufferSize"`
	KcpWriteBufferSize  int    `json:"kcpWriteBufferSize" form:"kcpWriteBufferSize"`
}

func (t *Tunnel) InboundTag() string {
	return fmt.Sprintf("tunnel-in-%v", t.Id)
}

func (t *Tunnel) OutboundTag() string {
	return fmt.Sprintf("tunnel-out-%v", t.Id)
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
