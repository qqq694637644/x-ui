package service

import (
	"fmt"
	"net"
	"strconv"
	"sync"

	"x-ui/database/model"
	"x-ui/mytrnrelay"
)

// The relay belongs to the x-ui process, not the Xray runtime. Never restart
// Xray for A endpoint changes. The only path to the remote A public endpoint
// is this loopback SOCKS5 UDP ASSOCIATE.
var mytrnRelayRuntime struct {
	sync.Mutex
	relay *mytrnrelay.Relay
}

func syncMyTRNRelay(cfg *model.MyTRN) error {
	mytrnRelayRuntime.Lock()
	defer mytrnRelayRuntime.Unlock()
	if !cfg.Enable {
		if mytrnRelayRuntime.relay != nil {
			mytrnRelayRuntime.relay.Close()
			mytrnRelayRuntime.relay = nil
		}
		return nil
	}
	relay := mytrnRelayRuntime.relay
	if relay == nil {
		addr := net.JoinHostPort(cfg.WarpHost, strconv.Itoa(cfg.WarpPort))
		var err error
		relay, err = mytrnrelay.New(mytrnrelay.ListenAddress, addr)
		if err != nil {
			return fmt.Errorf("MyTRN UDP local relay unavailable: %w", err)
		}
		mytrnRelayRuntime.relay = relay
	} else if err := relay.UpdateWARP(cfg.WarpHost, cfg.WarpPort); err != nil {
		return err
	}
	ip, port := "", 0
	if isPublicIPv4(cfg.EndpointIP) && validPort(cfg.EndpointPort) &&
		cfg.CertificateFingerprint != "" {
		fingerprint, err := parseACertificate(cfg.CertificatePEM)
		if err == nil && fingerprint == cfg.CertificateFingerprint {
			ip, port = cfg.EndpointIP, cfg.EndpointPort
		}
	}
	return relay.UpdateTarget(ip, port)
}

func StartMyTRNRelay() error {
	cfg, err := (&MyTRNService{}).Get()
	if err != nil {
		return err
	}
	if cfg.Enable {
		if err := validateMyTRNSettings(cfg); err != nil {
			return err
		}
	}
	return syncMyTRNRelay(cfg)
}

func StopMyTRNRelay() {
	mytrnRelayRuntime.Lock()
	defer mytrnRelayRuntime.Unlock()
	if mytrnRelayRuntime.relay != nil {
		mytrnRelayRuntime.relay.Close()
		mytrnRelayRuntime.relay = nil
	}
}

func mytrnRelayUpdateEndpoint(ip string, port int) {
	mytrnRelayRuntime.Lock()
	defer mytrnRelayRuntime.Unlock()
	if mytrnRelayRuntime.relay != nil {
		_ = mytrnRelayRuntime.relay.UpdateTarget(ip, port)
	}
}

func mytrnRelayState() (string, string) {
	mytrnRelayRuntime.Lock()
	defer mytrnRelayRuntime.Unlock()
	if mytrnRelayRuntime.relay == nil {
		return "relay_unavailable", "MyTRN UDP 127.0.0.1:41001 未运行；检查本机端口占用和 x-ui 日志"
	}
	state := mytrnRelayRuntime.relay.Status()
	if !state.Running {
		return "relay_unavailable", "MyTRN UDP 中继已停止"
	}
	if state.Target == "" {
		return "waiting_endpoint", "MyTRN 本地中继运行中，等待 A 注册公网映射"
	}
	if state.Clients == 0 {
		return "relay_listening", "本地 UDP 中继已监听 127.0.0.1:41001，等待 Xray 发起 QUIC（尚无 SOCKS5 UDP ASSOCIATE）"
	}
	if !state.Association {
		msg := "MyTRN WARP SOCKS5 UDP ASSOCIATE 尚未就绪，已关闭所有原生公网直连回退"
		if state.Error != "" {
			msg += "：" + state.Error
		}
		return "warp_unavailable", msg
	}
	if state.Associations != state.Clients {
		return "warp_degraded", fmt.Sprintf("MyTRN WARP UDP 会话部分失效（%d/%d 就绪），未启用直连兜底",
			state.Associations, state.Clients)
	}
	return "applied", ""
}
