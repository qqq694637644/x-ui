// mockfrpc is a test-only replacement for the custom frpc binary.
// It accepts exactly "verify -c <file>" or "-c <file>" so tests exercise
// the production exec/verify/PID/signal lifecycle, not a mocked interface.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	args := os.Args[1:]
	if len(args) == 3 && args[0] == "verify" && args[1] == "-c" {
		if os.Getenv("MOCK_FRPC_LAX_VERIFY") == "1" {
			return
		}
		contents, err := os.ReadFile(args[2])
		if err != nil {
			os.Exit(3)
		}
		src := string(contents)
		for _, required := range []string{
			"# Managed by x-ui reverse SOCKS5.",
			"transport.wireProtocol = \"v1\"",
			"transport.proxyURL = \"socks5://",
			"[visitors.natTraversal]",
			"udpProxyURL = \"socks5://",
			"disableAssistedAddrs = true",
			"[visitors.datagram]",
		} {
			if !strings.Contains(src, required) {
				fmt.Fprintln(os.Stderr, "custom private XTCP configuration invalid")
				os.Exit(2)
			}
		}
		return
	}
	if len(args) == 2 && args[0] == "-c" {
		if os.Getenv("MOCK_FRPC_EMIT_LOGS") == "1" {
			fmt.Fprintln(os.Stdout, "mock frpc startup stdout")
			fmt.Fprintln(os.Stderr, "mock frpc startup stderr")
			if secret := os.Getenv("MOCK_FRPC_LOG_SECRET"); secret != "" {
				fmt.Fprintln(os.Stderr, "private credential:", secret)
			}
		}
		signalCh := make(chan os.Signal, 1)
		signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
		<-signalCh
		return
	}
	fmt.Fprintln(os.Stderr, "invalid custom frpc arguments")
	os.Exit(2)
}
