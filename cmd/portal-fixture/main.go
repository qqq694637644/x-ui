package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"x-ui/database/model"
	"x-ui/web/service"
)

func main() {
	output := flag.String("output", "", "output JSON path; stdout when empty")
	businessPort := flag.Int("business-port", 18081, "A-side business listen port")
	portalPort := flag.Int("portal-port", 40000, "A-side Portal mKCP UDP port")
	targetPort := flag.Int("target-port", 19090, "B-side target port carried by dokodemo-door")
	uuid := flag.String("uuid", "my-home-xray", "VMess UUID or Xray-compatible legacy short ID")
	network := flag.String("network", "tcp,udp", "business network: tcp, udp or tcp,udp")
	flag.Parse()

	tunnel := &model.Tunnel{
		Id:                  1,
		Enable:              true,
		Mode:                service.TunnelModePortal,
		Remark:              "portal-smoke",
		Listen:              "127.0.0.1",
		ListenPort:          *businessPort,
		Network:             *network,
		TargetAddress:       "127.0.0.1",
		TargetPort:          *targetPort,
		RemotePort:          *portalPort,
		Protocol:            "vmess",
		UUID:                *uuid,
		KcpFinalMaskType:    "header-srtp",
		KcpMtu:              1350,
		KcpTti:              20,
		KcpUplinkCapacity:   5,
		KcpDownlinkCapacity: 20,
		KcpCongestion:       false,
		KcpReadBufferSize:   2,
		KcpWriteBufferSize:  2,
	}
	config, err := service.BuildTunnelFixtureConfig(tunnel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data = append(data, '\n')
	if *output == "" {
		_, err = os.Stdout.Write(data)
	} else {
		err = os.WriteFile(*output, data, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
