package service

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"x-ui/database/model"
)

func TestBuildEndpointHealthCheckConfigUsesSameXHTTPPathAndPublicTLSHost(t *testing.T) {
	spec := &managedInboundSpec{
		ClientID: "11111111-1111-1111-1111-111111111111",
		Path:     "/fixed-xhttp",
		Mode:     "auto",
	}
	endpoint := &model.PublicEndpoint{Host: "healthcheck.asdasdasdas.shop", Port: 443}
	data, err := buildEndpointHealthCheckConfig(spec, endpoint, 18080)
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
		t.Fatalf("unexpected health-check stream settings: %#v", stream)
	}
	xhttp := stream["xhttpSettings"].(map[string]interface{})
	if xhttp["path"] != spec.Path || xhttp["host"] != endpoint.Host || xhttp["mode"] != spec.Mode {
		t.Fatalf("unexpected health-check xhttp settings: %#v", xhttp)
	}
	tlsSettings := stream["tlsSettings"].(map[string]interface{})
	if tlsSettings["serverName"] != endpoint.Host {
		t.Fatalf("health-check TLS serverName = %#v, want %s", tlsSettings["serverName"], endpoint.Host)
	}
	alpn, ok := tlsSettings["alpn"].([]interface{})
	if !ok || len(alpn) != 1 || alpn[0] != "http/1.1" {
		t.Fatalf("health-check TLS alpn = %#v, want [http/1.1]", tlsSettings["alpn"])
	}
}

func TestEndpointHealthCheckConfigAcceptedByConfiguredXray(t *testing.T) {
	if os.Getenv("XUI_HEALTHCHECK_XRAY_BIN") == "" {
		t.Skip("set XUI_HEALTHCHECK_XRAY_BIN to an Xray build that supports managed XHTTP")
	}
	spec := &managedInboundSpec{
		ClientID: "11111111-1111-1111-1111-111111111111",
		Path:     "/fixed-xhttp",
		Mode:     "auto",
	}
	endpoint := &model.PublicEndpoint{Host: "healthcheck.asdasdasdas.shop", Port: 443}
	data, err := buildEndpointHealthCheckConfig(spec, endpoint, 18080)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/healthcheck.json"
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := resolveXrayBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-test", "-config", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bundled Xray rejected endpoint health-check config: %v\n%s", err, string(output))
	}
}
