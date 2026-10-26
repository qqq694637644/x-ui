package reverseexit

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
	"x-ui/rsxdiag"
)

type Config struct {
	// DirectQUIC is used only by the separate IPv6/DDNS runtime. It never
	// changes the historical XTCP/WARP configuration or its validation.
	DirectQUIC bool `json:"-"`
	// DirectTarget enables explicit native TCP/UDP egress only for DirectQUIC.
	// Legacy FRP deployments keep their historical SOCKS-only routing.
	DirectTarget bool `json:"-"`
	// RSX3 is the sole supported wire protocol; saved legacy configurations
	// are rejected rather than silently downgraded or reinterpreted.
	Protocol    string `json:"protocol,omitempty"`
	VisitorAddr string `json:"visitorAddr"`
	UDPBindAddr string `json:"udpBindAddr"`
	UDPPeerAddr string `json:"udpPeerAddr"`
	WARPAddr    string `json:"warpAddr"`
	// TargetSOCKSAddr is the final application TCP/UDP egress proxy. Empty
	// preserves the original all-WARP behavior. When specified, it can be
	// an Xray loopback SOCKS5 inbound that applies the user's routing rules.
	// It NEVER changes frpc/STUN/XTCP's mandatory WARP transport.
	TargetSOCKSAddr       string                                 `json:"targetSocksAddr,omitempty"`
	TargetSOCKSUsername   string                                 `json:"targetSocksUsername,omitempty"`
	TargetSOCKSPassword   string                                 `json:"targetSocksPassword,omitempty"`
	WARPProbe             string                                 `json:"warpProbe"`
	WARPUsername          string                                 `json:"warpUsername"`
	WARPPassword          string                                 `json:"warpPassword"`
	Token                 string                                 `json:"token"`
	RetryInterval         time.Duration                          `json:"-"`
	DiagnosticsEnabled    bool                                   `json:"diagnosticsEnabled"`
	FRPCDiagnosticsAddr   string                                 `json:"frpcDiagnosticsAddr,omitempty"`
	Meter                 *rsxdiag.Meter                         `json:"-"`
	DiagnosticPeerChanged func(*DiagnosticPeer, *DiagnosticPeer) `json:"-"`
}

func DefaultConfig() Config {
	return Config{
		Protocol:            "rsx3",
		VisitorAddr:         "127.0.0.1:6000",
		UDPBindAddr:         "127.0.0.1:6002",
		UDPPeerAddr:         "127.0.0.1:6001",
		WARPProbe:           "1.1.1.1:443",
		RetryInterval:       2 * time.Second,
		FRPCDiagnosticsAddr: "127.0.0.1:9321",
	}
}

func localhostAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("address %q must be 127.0.0.1:port", addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid local port in %q", addr)
	}
	return nil
}

// Validate deliberately rejects all non-loopback egress proxy endpoints.
// There is no direct egress path or fallback to the VPS public IP.
func (c Config) Validate() error {
	if c.DirectQUIC {
		if c.Protocol != "rsx3" || len(c.Token) < 16 {
			return errors.New("direct QUIC requires RSX3 and a 16+ character token")
		}
		if c.DirectTarget {
			if c.TargetSOCKSAddr != "" || c.TargetSOCKSUsername != "" || c.TargetSOCKSPassword != "" {
				return errors.New("native target must not have a SOCKS5 address or credentials")
			}
		} else {
			if c.TargetSOCKSAddr == "" {
				return errors.New("direct QUIC SOCKS5 egress requires an explicit loopback proxy")
			}
			if err := localhostAddr(c.TargetSOCKSAddr); err != nil {
				return err
			}
		}
		if (c.TargetSOCKSUsername == "") != (c.TargetSOCKSPassword == "") || len(c.TargetSOCKSUsername) > 255 || len(c.TargetSOCKSPassword) > 255 {
			return errors.New("target SOCKS5 username/password must both be set or both empty (max 255 bytes)")
		}
		if c.DiagnosticsEnabled {
			return errors.New("direct QUIC Diagnostics is not yet supported")
		}
		if c.RetryInterval <= 0 {
			return errors.New("invalid retry interval")
		}
		return nil
	}
	if c.DiagnosticsEnabled {
		if err := localhostAddr(c.FRPCDiagnosticsAddr); err != nil {
			return err
		}
	}
	if c.Protocol != "rsx3" {
		return errors.New("reverse SOCKS protocol must be RSX3; legacy protocols are no longer supported")
	}
	for _, addr := range []string{c.VisitorAddr, c.UDPBindAddr, c.UDPPeerAddr, c.WARPAddr} {
		if err := localhostAddr(addr); err != nil {
			return err
		}
	}
	if c.TargetSOCKSAddr != "" {
		if err := localhostAddr(c.TargetSOCKSAddr); err != nil {
			return fmt.Errorf("target SOCKS5 endpoint must be local Xray or WARP: %w", err)
		}
	} else if c.TargetSOCKSUsername != "" || c.TargetSOCKSPassword != "" {
		return errors.New("target SOCKS5 credentials require an explicit targetSocksAddr")
	}
	if c.UDPBindAddr == c.UDPPeerAddr {
		return errors.New("UDP bind and frpc peer ports must differ")
	}
	probeHost, probePort, err := net.SplitHostPort(c.WARPProbe)
	if err != nil || probeHost == "" {
		return errors.New("WARP TCP probe must be host:port")
	}
	probePortNum, err := strconv.Atoi(probePort)
	if err != nil || probePortNum < 1 || probePortNum > 65535 {
		return errors.New("WARP TCP probe requires valid port")
	}
	if len(c.Token) < 16 {
		return errors.New("RSX token must contain at least 16 characters")
	}
	if len(c.WARPUsername) > 255 || len(c.WARPPassword) > 255 ||
		(c.WARPUsername == "") != (c.WARPPassword == "") {
		return errors.New("WARP SOCKS5 username/password must be both set or both empty (max 255 bytes)")
	}
	if len(c.TargetSOCKSUsername) > 255 || len(c.TargetSOCKSPassword) > 255 ||
		(c.TargetSOCKSUsername == "") != (c.TargetSOCKSPassword == "") {
		return errors.New("target SOCKS5 username/password must be both set or both empty (max 255 bytes)")
	}
	if c.RetryInterval <= 0 {
		return errors.New("invalid retry interval")
	}
	return nil
}
