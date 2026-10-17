package reverseexit

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

type Config struct {
	VisitorAddr   string        `json:"visitorAddr"`
	UDPBindAddr   string        `json:"udpBindAddr"`
	UDPPeerAddr   string        `json:"udpPeerAddr"`
	WARPAddr      string        `json:"warpAddr"`
	WARPProbe     string        `json:"warpProbe"`
	WARPUsername  string        `json:"warpUsername"`
	WARPPassword  string        `json:"warpPassword"`
	Token         string        `json:"token"`
	RetryInterval time.Duration `json:"-"`
}

func DefaultConfig() Config {
	return Config{
		VisitorAddr:   "127.0.0.1:6000",
		UDPBindAddr:   "127.0.0.1:6002",
		UDPPeerAddr:   "127.0.0.1:6001",
		WARPProbe:     "1.1.1.1:443",
		RetryInterval: 2 * time.Second,
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
	for _, addr := range []string{c.VisitorAddr, c.UDPBindAddr, c.UDPPeerAddr, c.WARPAddr} {
		if err := localhostAddr(addr); err != nil {
			return err
		}
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
		return errors.New("RSX2 token must contain at least 16 characters")
	}
	if len(c.WARPUsername) > 255 || len(c.WARPPassword) > 255 ||
		(c.WARPUsername == "") != (c.WARPPassword == "") {
		return errors.New("WARP SOCKS5 username/password must be both set or both empty (max 255 bytes)")
	}
	if c.RetryInterval <= 0 {
		return errors.New("invalid retry interval")
	}
	return nil
}
