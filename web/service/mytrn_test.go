package service

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/xray"
)

const mytrnTestUUID = "123e4567-e89b-42d3-a456-426614174000"
const mytrnTestToken = "mytrn-test-token-98765432109876543210"

func mytrnTestCert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{CommonName: mytrnServerName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		DNSNames: []string{mytrnServerName}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func mytrnTestConfig(t *testing.T) *xray.Config {
	t.Helper()
	data, err := os.ReadFile("config.json")
	if err != nil {
		t.Fatal(err)
	}
	config := &xray.Config{}
	if err := json.Unmarshal(data, config); err != nil {
		t.Fatal(err)
	}
	return config
}

func mytrnTestRecord(t *testing.T) *model.MyTRN {
	t.Helper()
	cert := mytrnTestCert(t)
	fingerprint, err := parseACertificate(cert)
	if err != nil {
		t.Fatal(err)
	}
	return &model.MyTRN{
		Id: 1, Enable: true, Remark: "MyTRN", UUID: mytrnTestUUID,
		ControlToken: mytrnTestToken, ControlListen: "127.0.0.1", ControlPort: 18080,
		WarpHost: "127.0.0.1", WarpPort: 40000, EndpointIP: "119.98.144.218", EndpointPort: 57197,
		CertificatePEM: cert, CertificateFingerprint: fingerprint,
	}
}

func testMyTRNOutboundsAndRules(t *testing.T, config *xray.Config, item *model.MyTRN) {
	t.Helper()
	var outbounds []map[string]interface{}
	if err := json.Unmarshal(config.OutboundConfigs, &outbounds); err != nil {
		t.Fatal(err)
	}
	if len(outbounds) != 4 {
		t.Fatalf("outbounds count %d != 4 (original 2 + MyTRN dial and SOCKS)", len(outbounds))
	}
	if outbounds[0]["protocol"] != "freedom" || outbounds[0]["tag"] != mytrnFreedomTag ||
		outbounds[1]["tag"] != "blocked" {
		t.Fatalf("existing freedom/blackhole was replaced: %#v", outbounds[:2])
	}
	if outbounds[2]["tag"] != mytrnDialTag || outbounds[3]["tag"] != mytrnWarpTag {
		t.Fatalf("unexpected MyTRN tags: %#v", outbounds[2:])
	}
	dial := outbounds[2]
	settings := dial["settings"].(map[string]interface{})
	if settings["address"] != item.EndpointIP || int(settings["port"].(float64)) != item.EndpointPort {
		t.Fatalf("wrong A endpoint: %#v", settings)
	}
	if settings["reverse"].(map[string]interface{})["tag"] != mytrnInboundTag {
		t.Fatal("reverse-in is not the MyTRN data channel")
	}
	stream := dial["streamSettings"].(map[string]interface{})
	if stream["network"] != "kcp" || stream["security"] != "tls" ||
		stream["sockopt"].(map[string]interface{})["dialerProxy"] != mytrnWarpTag {
		t.Fatalf("wrong mKCP / WARP dialer: %#v", stream)
	}
	tls := stream["tlsSettings"].(map[string]interface{})
	if tls["allowInsecure"] != false || tls["disableSystemRoot"] != true {
		t.Fatalf("TLS verification weakened: %#v", tls)
	}
	warp := outbounds[3]["settings"].(map[string]interface{})["servers"].([]interface{})[0].(map[string]interface{})
	if warp["address"] != "127.0.0.1" || int(warp["port"].(float64)) != 40000 {
		t.Fatalf("WARP must only be the existing SOCKS5 port: %#v", warp)
	}
	var routing struct { Rules []map[string]interface{} `json:"rules"` }
	if err := json.Unmarshal(config.RouterConfig, &routing); err != nil {
		t.Fatal(err)
	}
	if len(routing.Rules) != 4 {
		t.Fatalf("routing rules count %d != original 3 + MyTRN 1", len(routing.Rules))
	}
	if routing.Rules[0]["outboundTag"] != "api" {
		t.Fatal("original API routing was altered")
	}
	if routing.Rules[1]["outboundTag"] != "blocked" || routing.Rules[2]["outboundTag"] != "blocked" {
		t.Fatal("original private/bittorrent blocks were removed")
	}
	last := routing.Rules[3]
	if last["outboundTag"] != mytrnFreedomTag ||
		last["inboundTag"].([]interface{})[0] != mytrnInboundTag {
		t.Fatalf("MyTRN traffic incorrectly routed: %#v", last)
	}
	if len(config.InboundConfigs) != 1 || config.InboundConfigs[0].Tag != "api" {
		t.Fatalf("MyTRN must not create a fake inbound: %#v", config.InboundConfigs)
	}
}

func TestMyTRNMergeKeepsExistingXrayAndNoSecondFreedom(t *testing.T) {
	config := mytrnTestConfig(t)
	item := mytrnTestRecord(t)
	cert := filepath.Join(t.TempDir(), "a-cert.pem")
	if err := os.WriteFile(cert, []byte(item.CertificatePEM), 0600); err != nil { t.Fatal(err) }
	if err := mergeMyTRNConfig(config, item, cert); err != nil { t.Fatal(err) }
	testMyTRNOutboundsAndRules(t, config, item)
}

func TestMyTRNMergeDisabledAndUnknownEndpointAreNoOps(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		config := mytrnTestConfig(t)
		before, _ := json.Marshal(config)
		item := mytrnTestRecord(t)
		item.Enable = enabled
		if enabled { item.EndpointIP = "" }
		if err := mergeMyTRNConfig(config, item, ""); err != nil { t.Fatal(err) }
		after, _ := json.Marshal(config)
		if !bytes.Equal(before, after) { t.Fatal("disabled/unregistered MyTRN changed existing Xray") }
	}
}

func TestMyTRNMergeRejectsTagConflictAndWrongFreedom(t *testing.T) {
	item := mytrnTestRecord(t)
	for _, value := range []string{mytrnDialTag, mytrnWarpTag, mytrnInboundTag} {
		config := mytrnTestConfig(t)
		var outbounds []map[string]interface{}
		_ = json.Unmarshal(config.OutboundConfigs, &outbounds)
		outbounds[1]["tag"] = value
		data, _ := json.Marshal(outbounds)
		config.OutboundConfigs = data
		if err := mergeMyTRNConfig(config, item, "valid.pem"); err == nil {
			t.Fatalf("tag conflict %s was accepted", value)
		}
	}
	config := mytrnTestConfig(t)
	var outbounds []map[string]interface{}
	_ = json.Unmarshal(config.OutboundConfigs, &outbounds)
	outbounds[0]["protocol"] = "blackhole"
	data, _ := json.Marshal(outbounds)
	config.OutboundConfigs = data
	if err := mergeMyTRNConfig(config, item, "valid.pem"); err == nil {
		t.Fatal("changed default freedom was not rejected")
	}
}

func TestMyTRNCertificateTrustAndPublicAddressValidation(t *testing.T) {
	cert := mytrnTestCert(t)
	if fingerprint, err := parseACertificate(cert); err != nil || len(fingerprint) != 64 {
		t.Fatalf("valid A PEM rejected: fingerprint=%s err=%v", fingerprint, err)
	}
	for _, raw := range []string{"", "not-pem", cert+cert} {
		if _, err := parseACertificate(raw); err == nil { t.Fatalf("accepted bad PEM of length %d", len(raw)) }
	}
	for _, value := range []string{"127.0.0.1", "192.168.1.1", "10.0.0.2", "100.64.0.5", "0.0.0.0", "224.0.0.1", "not-an-ip"} {
		if isPublicIPv4(value) { t.Fatalf("non-public address was accepted: %s", value) }
	}
	if !isPublicIPv4("119.98.144.218") { t.Fatal("public China Telecom NAT endpoint rejected") }
}

func freeMyTRNPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func postMyTRN(t *testing.T, url, token string, body interface{}) (int, []byte) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil { t.Fatal(err) }
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil { t.Fatal(err) }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Control-Token", token)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil { t.Fatal(err) }
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil { t.Fatal(err) }
	return resp.StatusCode, data
}

func TestMyTRNGoControlMatchesExistingPythonA(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "mytrn.db")); err != nil { t.Fatal(err) }
	defer StopMyTRNControl()
	service := &MyTRNService{}
	port := freeMyTRNPort(t)
	settings := MyTRNSettings{
		Enable: true, Remark: "MyTRN", UUID: mytrnTestUUID,
		ControlToken: mytrnTestToken, ControlListen: "127.0.0.1", ControlPort: port,
		WarpHost: "127.0.0.1", WarpPort: 40000,
	}
	changed, err := service.UpdateSettings(settings)
	if err != nil || !changed { t.Fatalf("enable settings: changed=%v error=%v", changed, err) }
	url := fmt.Sprintf("http://127.0.0.1:%d/control/mapping", port)
	registration := MyTRNRegistration{Node: "a", IP: "119.98.144.218", Port: 57197, Certificate: mytrnTestCert(t)}
	if status, _ := postMyTRN(t, url, "wrong", registration); status != 403 {
		t.Fatalf("unauthorized registration HTTP=%d", status)
	}
	if status, _ := postMyTRN(t, url, mytrnTestToken, MyTRNRegistration{Node: "a", IP: "127.0.0.1", Port: 57197, Certificate: registration.Certificate}); status != 400 {
		t.Fatalf("private endpoint accepted HTTP=%d", status)
	}
	status, data := postMyTRN(t, url, mytrnTestToken, registration)
	if status != 200 || !bytes.Contains(data, []byte(`"changed":true`)) {
		t.Fatalf("first registration HTTP=%d %s", status, data)
	}
	stored, err := service.Get()
	if err != nil { t.Fatal(err) }
	if stored.EndpointIP != registration.IP || stored.EndpointPort != registration.Port || stored.CertificateFingerprint == "" {
		t.Fatalf("registration did not persist: %#v", stored.EndpointIP)
	}
	if stored.CertificatePEM != registration.Certificate { t.Fatal("registered public cert differs") }
	if status, data = postMyTRN(t, url, mytrnTestToken, registration); status != 200 || !bytes.Contains(data, []byte(`"changed":false`)) {
		t.Fatalf("same endpoint forced restart: HTTP=%d %s", status, data)
	}
	if view, err := service.View(); err != nil || view.Status != "configured" || !view.ControlTokenConfigured {
		t.Fatalf("incorrect UI status: view=%#v err=%v", view, err)
	}
	other := registration
	other.Certificate = mytrnTestCert(t)
	if status, _ := postMyTRN(t, url, mytrnTestToken, other); status != 409 {
		t.Fatalf("A TLS certificate change accepted HTTP=%d", status)
	}
	newEndpoint := registration
	newEndpoint.Port = 55001
	if status, data = postMyTRN(t, url, mytrnTestToken, newEndpoint); status != 200 || !bytes.Contains(data, []byte(`"changed":true`)) {
		t.Fatalf("mapping change rejected HTTP=%d %s", status, data)
	}
	stored, err = service.Get()
	if err != nil || stored.EndpointPort != 55001 { t.Fatalf("new NAT mapping not persisted: %#v %v", stored, err) }
	settings.ControlToken = ""
	if changed, err = service.UpdateSettings(settings); err != nil || changed {
		t.Fatalf("identical UI save changed data: changed=%v err=%v", changed, err)
	}
	stored, _ = service.Get()
	if stored.ControlToken != mytrnTestToken { t.Fatal("blank UI token overwrote existing credential") }
	settings.ResetTrust = true
	if changed, err = service.UpdateSettings(settings); err != nil || !changed { t.Fatalf("reset trust did not require restart: %v %v", changed, err) }
	stored, _ = service.Get()
	if stored.CertificateFingerprint != "" || stored.EndpointIP != "" { t.Fatal("explicit trust reset left old cert/endpoint") }
}

func TestMyTRNGeneratedJSONAcceptedByRealXray26327(t *testing.T) {
	binary := os.Getenv("XUI_HEALTHCHECK_XRAY_BIN")
	if binary == "" { t.Skip("CI pinned Xray binary is not installed") }
	config := mytrnTestConfig(t)
	item := mytrnTestRecord(t)
	certificate := filepath.Join(t.TempDir(), "mytrn-a-cert.pem")
	if err := os.WriteFile(certificate, []byte(item.CertificatePEM), 0600); err != nil { t.Fatal(err) }
	if err := mergeMyTRNConfig(config, item, certificate); err != nil { t.Fatal(err) }
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil { t.Fatal(err) }
	configPath := filepath.Join(t.TempDir(), "xray.json")
	if err := os.WriteFile(configPath, payload, 0600); err != nil { t.Fatal(err) }
	cmd := exec.Command(binary, "run", "-test", "-config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil { t.Fatalf("real Xray 26.3.27 rejected MyTRN merged config: %v\n%s\n%s", err, output, payload) }
	if !strings.Contains(string(output), "Reading config") { t.Logf("Xray validation output: %s", output) }
}
