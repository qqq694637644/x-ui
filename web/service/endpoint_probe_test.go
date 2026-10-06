package service

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"x-ui/database/model"
)

func TestBuildEndpointProbeConfigUsesSameXHTTPPathAndPublicTLSHost(t *testing.T) {
	spec := &managedInboundSpec{
		ClientID: "11111111-1111-1111-1111-111111111111",
		Path:     "/fixed-xhttp",
		Mode:     "auto",
	}
	endpoint := &model.PublicEndpoint{Host: "probe.asdasdasdas.shop", Port: 443}
	data, err := buildEndpointProbeConfig(spec, endpoint, 18080)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	outbounds := config["outbounds"].([]interface{})
	outbound := outbounds[0].(map[string]interface{})
	stream := outbound["streamSettings"].(map[string]interface{})
	if stream["network"] != "xhttp" || stream["security"] != "tls" {
		t.Fatalf("unexpected probe stream settings: %#v", stream)
	}
	xhttp := stream["xhttpSettings"].(map[string]interface{})
	if xhttp["path"] != spec.Path || xhttp["host"] != endpoint.Host || xhttp["mode"] != spec.Mode {
		t.Fatalf("unexpected probe xhttp settings: %#v", xhttp)
	}
	tlsSettings := stream["tlsSettings"].(map[string]interface{})
	if tlsSettings["serverName"] != endpoint.Host {
		t.Fatalf("probe TLS serverName = %#v, want %s", tlsSettings["serverName"], endpoint.Host)
	}
}

func TestEndpointProbeConfigAcceptedByBundledXray(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("bundled probe config validation runs on linux/amd64 CI")
	}
	spec := &managedInboundSpec{
		ClientID: "11111111-1111-1111-1111-111111111111",
		Path:     "/fixed-xhttp",
		Mode:     "auto",
	}
	endpoint := &model.PublicEndpoint{Host: "probe.asdasdasdas.shop", Port: 443}
	data, err := buildEndpointProbeConfig(spec, endpoint, 18080)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/probe.json"
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := resolveXrayBinaryPath()
	if err != nil {
		t.Skipf("Xray validation binary is unavailable in this job: %v", err)
	}
	cmd := exec.Command(binary, "run", "-test", "-config", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bundled Xray rejected endpoint probe config: %v\n%s", err, string(output))
	}
}
