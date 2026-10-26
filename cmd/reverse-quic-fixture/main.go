package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"x-ui/web/service"
)

// This fixture is CI-only: use the exact production Bridge config generator
// and write its output for a real Xray 26.3.27 process.
func main() {
	profile := flag.String("profile", "", "JSON ReverseQUICBridgeConfig")
	output := flag.String("output", "", "output Xray config path")
	flag.Parse()
	if *profile == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "--profile and --output are required")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*profile)
	if err != nil {
		exit(err)
	}
	var cfg service.ReverseQUICBridgeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		exit(err)
	}
	if !cfg.Enabled {
		exit(fmt.Errorf("Bridge profile must be enabled for smoke test"))
	}
	generated, err := service.BuildReverseQUICFixtureConfig(cfg)
	if err != nil {
		exit(err)
	}
	data, err := json.MarshalIndent(generated, "", "  ")
	if err != nil {
		exit(err)
	}
	if err := os.WriteFile(*output, append(data, '\n'), 0600); err != nil {
		exit(err)
	}
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
