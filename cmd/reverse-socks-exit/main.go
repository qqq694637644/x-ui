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
	visitor := flag.String("visitor", "127.0.0.1:6000", "local frpc XTCP Visitor TCP port")
	udpBind := flag.String("udp-bind", "127.0.0.1:6002", "UDP socket owned by Go (frpc datagram.peerAddr)")
	udpPeer := flag.String("udp-frpc", "127.0.0.1:6001", "local frpc datagram.listenAddr")
	warp := flag.String("warp-socks", "", "MANDATORY local WARP SOCKS5 endpoint")
	warpProbe := flag.String("warp-probe", "1.1.1.1:443", "WARP TCP CONNECT healthcheck destination")
	warpUser := flag.String("warp-user", "", "optional WARP SOCKS5 username")
	retry := flag.Duration("retry", 2*time.Second, "reconnect interval")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cfg := reverseexit.Config{
		VisitorAddr: *visitor, UDPBindAddr: *udpBind, UDPPeerAddr: *udpPeer,
		WARPAddr: *warp, WARPUsername: *warpUser,
		WARPProbe:     *warpProbe,
		WARPPassword:  os.Getenv("WARP_SOCKS_PASSWORD"),
		Token:         os.Getenv("REVERSE_SOCKS_TOKEN"),
		RetryInterval: *retry,
	}
	if err := reverseexit.Run(ctx, cfg, nil); err != nil &&
		err != context.Canceled {
		log.Fatal(err)
	}
}
