package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"x-ui/reverseexit"
)

func main() {
	protocol := flag.String("protocol", "rsx3", "RSX3 independent QUIC TCP streams (only supported protocol)")
	visitor := flag.String("visitor", "127.0.0.1:6000", "local frpc XTCP Visitor TCP port")
	directDDNS := flag.String("direct-ddns", "", "optional IPv6 DDNS hostname for direct QUIC mode; legacy FRP if empty")
	directPort := flag.Int("direct-port", 8080, "direct QUIC UDP port on Windows")
	directPin := flag.String("direct-cert-sha256", "", "SHA256 DER certificate pin of the Windows QUIC TLS certificate")
	nativeTarget := flag.Bool("native-egress", false, "explicit native VPS TCP/UDP egress (only in direct QUIC mode)")
	udpBind := flag.String("udp-bind", "127.0.0.1:6002", "UDP socket owned by Go (frpc datagram.peerAddr)")
	udpPeer := flag.String("udp-frpc", "127.0.0.1:6001", "local frpc datagram.listenAddr")
	warp := flag.String("warp-socks", "", "MANDATORY local WARP SOCKS5 endpoint")
	target := flag.String("target-socks", "", "optional local final-destination SOCKS5, e.g. Xray 127.0.0.1:40001; empty uses WARP")
	targetUser := flag.String("target-socks-user", "", "optional final-destination SOCKS5 username, independent from WARP")
	warpProbe := flag.String("warp-probe", "1.1.1.1:443", "WARP TCP CONNECT healthcheck destination")
	warpUser := flag.String("warp-user", "", "optional WARP SOCKS5 username")
	retry := flag.Duration("retry", 2*time.Second, "reconnect interval")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cfg := reverseexit.Config{
		Protocol:    *protocol,
		VisitorAddr: *visitor, UDPBindAddr: *udpBind, UDPPeerAddr: *udpPeer,
		WARPAddr: *warp, WARPUsername: *warpUser,
		TargetSOCKSAddr: *target, TargetSOCKSUsername: *targetUser,
		TargetSOCKSPassword: os.Getenv("RSX_TARGET_SOCKS_PASSWORD"),
		WARPProbe:           *warpProbe,
		WARPPassword:        os.Getenv("WARP_SOCKS_PASSWORD"),
		Token:               os.Getenv("REVERSE_SOCKS_TOKEN"),
		RetryInterval:       *retry,
	}
	if *directDDNS != "" {
		cfg.DirectQUIC = true
		cfg.DirectTarget = *nativeTarget
		err := reverseexit.RunDirect(ctx, reverseexit.DirectConfig{
			Hostname: *directDDNS, Port: *directPort, CertSHA256: *directPin, Agent: cfg,
		}, func(state, message string) {
			log.Printf("RSX3 direct QUIC state=%s message=%s", state, message)
		})
		if err != nil && err != context.Canceled {
			log.Fatal(err)
		}
		return
	}
	if err := reverseexit.Run(ctx, cfg, nil); err != nil &&
		err != context.Canceled {
		log.Fatal(err)
	}
}
