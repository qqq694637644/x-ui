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
	retry := flag.Duration("retry", 2*time.Second, "reconnect interval")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := reverseexit.Run(ctx, *visitor, os.Getenv("REVERSE_SOCKS_TOKEN"), *retry); err != nil &&
		err != context.Canceled {
		log.Fatal(err)
	}
}
