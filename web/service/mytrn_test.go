package service

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
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
	return mytrnTestCertWithValidity(t, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
}

func mytrnTestCertWithValidity(t *testing.T, notBefore, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: mytrnServerName},
		NotBefore:    notBefore, NotAfter: notAfter,
		DNSNames: []string{mytrnServerName}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
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
		Hy2UpMbps: 64, Hy2DownMbps: 64,
		CertificatePEM: cert, CertificateFingerprint: fingerprint,
	}
}

func testMyTRNOutboundsAndRules(t *testing.T, config *xray.Config, item *model.MyTRN) {
	t.Helper()
	var outbounds []map[string]interface{}
	if err := json.Unmarshal(config.OutboundConfigs, &outbounds); err != nil {
		t.Fatal(err)
	}
	if len(outbounds) != 3 {
		t.Fatalf("outbounds count %d != 3 (original 2 + MyTRN local UDP dial)", len(outbounds))
	}
	if outbounds[0]["protocol"] != "freedom" || outbounds[0]["tag"] != mytrnFreedomTag ||
		outbounds[1]["tag"] != "blocked" {
		t.Fatalf("existing freedom/blackhole was replaced: %#v", outbounds[:2])
	}
	if outbounds[2]["tag"] != mytrnDialTag {
		t.Fatalf("unexpected MyTRN tags: %#v", outbounds[2:])
	}
	dial := outbounds[2]
	settings := dial["settings"].(map[string]interface{})
	if settings["address"] != "127.0.0.1" || int(settings["port"].(float64)) != 41001 {
		t.Fatalf("Xray must use the fixed loopback UDP relay: %#v", settings)
	}
	if settings["reverse"].(map[string]interface{})["tag"] != mytrnInboundTag {
		t.Fatal("reverse-in is not the MyTRN data channel")
	}
	stream := dial["streamSettings"].(map[string]interface{})
	if stream["network"] != "hysteria" || stream["security"] != "tls" {
		t.Fatalf("wrong Hysteria2 transport: %#v", stream)
	}
	if _, present := stream["sockopt"]; present {
		t.Fatalf("legacy Xray dialerProxy path retained: %#v", stream)
	}
	if stream["hysteriaSettings"].(map[string]interface{})["auth"] != mytrnHy2Auth(item.ControlToken) {
		t.Fatal("Hysteria authentication is not derived from the shared control token")
	}
	if int(stream["hysteriaSettings"].(map[string]interface{})["version"].(float64)) != 2 {
		t.Fatal("not Hysteria2")
	}
	quic := stream["finalmask"].(map[string]interface{})["quicParams"].(map[string]interface{})
	if quic["congestion"] != "brutal" ||
		quic["brutalUp"] != fmt.Sprintf("%dmbps", item.Hy2UpMbps) ||
		quic["brutalDown"] != fmt.Sprintf("%dmbps", item.Hy2DownMbps) ||
		quic["disablePathMTUDiscovery"] != true || quic["maxIdleTimeout"] != float64(15) ||
		quic["keepAlivePeriod"] != float64(5) {
		t.Fatalf("wrong Brutal2 QUIC configuration: %#v", quic)
	}
	if _, hasKCP := stream["kcpSettings"]; hasKCP {
		t.Fatal("obsolete mKCP transport still present")
	}
	tls := stream["tlsSettings"].(map[string]interface{})
	if tls["allowInsecure"] != false || tls["disableSystemRoot"] != true {
		t.Fatalf("TLS verification weakened: %#v", tls)
	}
	if tls["alpn"].([]interface{})[0] != "h3" {
		t.Fatal("HTTP3 ALPN is mandatory for Hysteria2")
	}
	var routing struct {
		Rules []map[string]interface{} `json:"rules"`
	}
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
	if err := os.WriteFile(cert, []byte(item.CertificatePEM), 0600); err != nil {
		t.Fatal(err)
	}
	if err := mergeMyTRNConfig(config, item, cert); err != nil {
		t.Fatal(err)
	}
	testMyTRNOutboundsAndRules(t, config, item)
}

func TestMyTRNEndpointChangesLeaveEntireXrayConfigurationUnchanged(t *testing.T) {
	item := mytrnTestRecord(t)
	previous := mytrnTestConfig(t)
	if err := mergeMyTRNConfig(previous, item, "a-cert.pem"); err != nil {
		t.Fatal(err)
	}
	tuned := *item
	tuned.EndpointIP, tuned.EndpointPort = "119.98.144.219", 55001
	if !mytrnMatchesRunningConfig(&tuned, previous) {
		t.Fatal("changed public NAT mapping was incorrectly bound to active Xray configuration")
	}
	updated := mytrnTestConfig(t)
	if err := mergeMyTRNConfig(updated, &tuned, "a-cert.pem"); err != nil {
		t.Fatal(err)
	}
	if !previous.Equals(updated) {
		t.Fatal("public NAT IP/port change caused a different Xray configuration")
	}
	value, err := json.Marshal(updated)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(value, []byte(item.EndpointIP)) || bytes.Contains(value, []byte(tuned.EndpointIP)) {
		t.Fatal("A public IPv4 endpoint leaked into static Xray data plane configuration")
	}
}

func TestMyTRNHy2DefaultsAndDomainSeparatedAuth(t *testing.T) {
	item := mytrnTestRecord(t)
	// Exact Python mytrn.config.hysteria_auth known vector. Mismatched
	// derivation would pass both unit suites but fail the real A/B handshake.
	const pythonAuth = "ce851c7793923487b4a4de884a8a29744cd9fa369807898a264f918e048002d7"
	if mytrnHy2Auth(mytrnTestToken) != pythonAuth {
		t.Fatal("Go / Python Hysteria2 credentials disagree")
	}
	if mytrnHy2Auth(item.ControlToken) == item.ControlToken || len(mytrnHy2Auth(item.ControlToken)) != 64 {
		t.Fatal("Hy2 auth must be an independent, full-strength keyed digest")
	}
	if mytrnHy2Auth(item.ControlToken) == mytrnHy2Auth(item.ControlToken+"changed") {
		t.Fatal("Hy2 credentials did not change after token rotation")
	}
	oldRecord := &model.MyTRN{}
	normalizeMyTRNHy2(oldRecord)
	if oldRecord.Hy2UpMbps != 64 || oldRecord.Hy2DownMbps != 64 {
		t.Fatalf("new Hy2 columns did not get defaults: %#v", oldRecord)
	}
	config := mytrnTestConfig(t)
	if err := mergeMyTRNConfig(config, item, "a-cert.pem"); err != nil {
		t.Fatal(err)
	}
	if !mytrnMatchesRunningConfig(item, config) {
		t.Fatal("generated running MyTRN must match the new Hy2 defaults")
	}
}

func TestMyTRNHy2TuningChangesOnlyBOutboundAndApplicationState(t *testing.T) {
	item := mytrnTestRecord(t)
	config := mytrnTestConfig(t)
	if err := mergeMyTRNConfig(config, item, "a-cert.pem"); err != nil {
		t.Fatal(err)
	}
	tuned := *item
	tuned.Hy2UpMbps = 128
	tuned.Hy2DownMbps = 192
	if mytrnMatchesRunningConfig(&tuned, config) {
		t.Fatal("Hy2 parameter changes must not show as already applied")
	}
	if status, _ := mytrnStatus(&tuned, config, ""); status != "pending_apply" {
		t.Fatalf("changed Hy2 settings displayed as %q instead of pending_apply", status)
	}
	updated := mytrnTestConfig(t)
	if err := mergeMyTRNConfig(updated, &tuned, "a-cert.pem"); err != nil {
		t.Fatal(err)
	}
	testMyTRNOutboundsAndRules(t, updated, &tuned)
	if !mytrnMatchesRunningConfig(&tuned, updated) {
		t.Fatal("new Hy2 parameters were not recognized as applied")
	}
	var outbounds []map[string]interface{}
	if err := json.Unmarshal(updated.OutboundConfigs, &outbounds); err != nil {
		t.Fatal(err)
	}
	actual := outbounds[2]["streamSettings"].(map[string]interface{})["finalmask"].(map[string]interface{})["quicParams"].(map[string]interface{})
	for key, want := range map[string]interface{}{
		"congestion": "brutal", "brutalUp": "128mbps", "brutalDown": "192mbps",
		"disablePathMTUDiscovery": true, "maxIdleTimeout": float64(15), "keepAlivePeriod": float64(5),
	} {
		if actual[key] != want {
			t.Fatalf("Hy2 %s: got %#v, want %#v", key, actual[key], want)
		}
	}
	if len(actual) != 6 {
		t.Fatalf("unexpected Hy2 QUIC options emitted: %#v", actual)
	}
	if len(config.InboundConfigs) != len(updated.InboundConfigs) {
		t.Fatal("Hy2 adjustment unexpectedly changed B's existing Xray inbounds")
	}
}

func TestMyTRNHy2InputValidation(t *testing.T) {
	checks := []struct {
		name string
		edit func(*model.MyTRN)
	}{
		{"uplink zero", func(c *model.MyTRN) { c.Hy2UpMbps = 0 }},
		{"uplink overflow", func(c *model.MyTRN) { c.Hy2UpMbps = 10001 }},
		{"downlink zero", func(c *model.MyTRN) { c.Hy2DownMbps = 0 }},
		{"downlink overflow", func(c *model.MyTRN) { c.Hy2DownMbps = 10001 }},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			item := mytrnTestRecord(t)
			tc.edit(item)
			if err := validateMyTRNSettings(item); err == nil {
				t.Fatal("invalid Hy2 parameter was accepted")
			}
		})
	}
}

func TestMyTRNHy2SettingsPersistAndRequireRestartOnlyWhenChanged(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "mytrn-hy2.db")); err != nil {
		t.Fatal(err)
	}
	defer StopMyTRNControl()
	defer StopMyTRNRelay()
	service := &MyTRNService{}
	stored := mytrnTestRecord(t)
	stored.ControlPort = freeMyTRNPort(t)
	if err := database.GetDB().Save(stored).Error; err != nil {
		t.Fatal(err)
	}
	settings := MyTRNSettings{
		Enable: true, Remark: stored.Remark, UUID: stored.UUID,
		ControlListen: stored.ControlListen, ControlPort: stored.ControlPort,
		WarpHost: stored.WarpHost, WarpPort: stored.WarpPort,
		Hy2UpMbps: 128, Hy2DownMbps: 192,
	}
	changed, err := service.UpdateSettings(settings)
	if err != nil || !changed {
		t.Fatalf("Hy2 tuning must schedule B's Xray restart: changed=%t err=%v", changed, err)
	}
	reloaded, err := service.Get()
	if err != nil || reloaded.Hy2UpMbps != 128 || reloaded.Hy2DownMbps != 192 {
		t.Fatalf("Hy2 settings were not persisted: record=%#v err=%v", reloaded, err)
	}
	changed, err = service.UpdateSettings(settings)
	if err != nil || changed {
		t.Fatalf("saving the exact same Hy2 parameters must not restart Xray: changed=%t err=%v", changed, err)
	}
	settings.WarpPort = 40001
	changed, err = service.UpdateSettings(settings)
	if err != nil || changed {
		t.Fatalf("WARP SOCKS port must hot-reconfigure only the Go relay: changed=%t err=%v", changed, err)
	}
	settings.ControlToken = "mytrn-rotated-token-for-hysteria-9876543210"
	changed, err = service.UpdateSettings(settings)
	if err != nil || !changed {
		t.Fatalf("Hy2 credential rotation must restart Xray: changed=%t err=%v", changed, err)
	}
	settings.ControlToken = ""
	settings.Hy2UpMbps = 0
	if _, err := service.UpdateSettings(settings); err == nil {
		t.Fatal("invalid Hy2 Mbps must be rejected before updating the database")
	}
	reloaded, err = service.Get()
	if err != nil || reloaded.Hy2UpMbps != 128 {
		t.Fatalf("invalid settings modified the saved Hy2 Mbps: %v %v", reloaded, err)
	}
}

func TestMyTRNMergeDisabledAndUnknownEndpointAreNoOps(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		config := mytrnTestConfig(t)
		before, _ := json.Marshal(config)
		item := mytrnTestRecord(t)
		item.Enable = enabled
		if enabled {
			item.EndpointIP = ""
		}
		if err := mergeMyTRNConfig(config, item, ""); err != nil {
			t.Fatal(err)
		}
		after, _ := json.Marshal(config)
		if !bytes.Equal(before, after) {
			t.Fatal("disabled/unregistered MyTRN changed existing Xray")
		}
	}
}

func TestMyTRNMergeRejectsTagConflictAndWrongFreedom(t *testing.T) {
	item := mytrnTestRecord(t)
	for _, value := range []string{mytrnDialTag, "mytrn-warp", mytrnInboundTag} {
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
	for _, raw := range []string{"", "not-pem", cert + cert} {
		if _, err := parseACertificate(raw); err == nil {
			t.Fatalf("accepted bad PEM of length %d", len(raw))
		}
	}
	for _, value := range []string{"127.0.0.1", "192.168.1.1", "10.0.0.2", "100.64.0.5", "0.0.0.0", "224.0.0.1", "not-an-ip"} {
		if isPublicIPv4(value) {
			t.Fatalf("non-public address was accepted: %s", value)
		}
	}
	if !isPublicIPv4("119.98.144.218") {
		t.Fatal("public China Telecom NAT endpoint rejected")
	}
}

func freeMyTRNPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func postMyTRN(t *testing.T, url, token string, body interface{}) (int, []byte) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Control-Token", token)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

func TestMyTRNGoControlMatchesExistingPythonA(t *testing.T) {
	// Only the first trusted registration schedules an Xray restart. Later
	// endpoint changes must not disturb any Xray inbounds.
	// Restore the exact pre-test state so independent subscription/managed
	// health tests are not affected by this isolated HTTP integration case.
	beforeRestart := isNeedXrayRestart.Load()
	beforeHealthy := managedXrayHealthy.Load()
	defer func() {
		isNeedXrayRestart.Store(beforeRestart)
		managedXrayHealthy.Store(beforeHealthy)
	}()
	if err := database.InitDB(filepath.Join(t.TempDir(), "mytrn.db")); err != nil {
		t.Fatal(err)
	}
	defer StopMyTRNControl()
	defer StopMyTRNRelay()
	service := &MyTRNService{}
	port := freeMyTRNPort(t)
	settings := MyTRNSettings{
		Enable: true, Remark: "MyTRN", UUID: mytrnTestUUID,
		ControlToken: mytrnTestToken, ControlListen: "127.0.0.1", ControlPort: port,
		WarpHost: "127.0.0.1", WarpPort: 40000,
		Hy2UpMbps: 64, Hy2DownMbps: 64,
	}
	changed, err := service.UpdateSettings(settings)
	if err != nil || !changed {
		t.Fatalf("enable settings: changed=%v error=%v", changed, err)
	}
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
	if !isNeedXrayRestart.Load() {
		t.Fatal("initial trusted endpoint must install the new Xray reverse outbound")
	}
	isNeedXrayRestart.Store(false)
	stored, err := service.Get()
	if err != nil {
		t.Fatal(err)
	}
	if stored.EndpointIP != registration.IP || stored.EndpointPort != registration.Port || stored.CertificateFingerprint == "" {
		t.Fatalf("registration did not persist: %#v", stored.EndpointIP)
	}
	if stored.CertificatePEM != registration.Certificate {
		t.Fatal("registered public cert differs")
	}
	if status, data = postMyTRN(t, url, mytrnTestToken, registration); status != 200 || !bytes.Contains(data, []byte(`"changed":false`)) {
		t.Fatalf("same endpoint forced restart: HTTP=%d %s", status, data)
	}
	if view, err := service.View(); err != nil || view.Status != "pending_apply" || !view.ControlTokenConfigured || view.ControlToken != mytrnTestToken {
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
	if isNeedXrayRestart.Load() {
		t.Fatal("A endpoint port change incorrectly requested a global Xray restart")
	}
	if state, _ := mytrnRelayState(); state == "relay_unavailable" {
		t.Fatal("configured B relay did not start")
	}
	stored, err = service.Get()
	if err != nil || stored.EndpointPort != 55001 {
		t.Fatalf("new NAT mapping not persisted: %#v %v", stored, err)
	}
	settings.ControlToken = ""
	if changed, err = service.UpdateSettings(settings); err != nil || changed {
		t.Fatalf("identical UI save changed data: changed=%v err=%v", changed, err)
	}
	stored, _ = service.Get()
	if stored.ControlToken != mytrnTestToken {
		t.Fatal("blank UI token overwrote existing credential")
	}
	settings.ResetTrust = true
	if changed, err = service.UpdateSettings(settings); err != nil || !changed {
		t.Fatalf("reset trust did not require restart: %v %v", changed, err)
	}
	stored, _ = service.Get()
	if stored.CertificateFingerprint != "" || stored.EndpointIP != "" {
		t.Fatal("explicit trust reset left old cert/endpoint")
	}
}

func TestMyTRNGeneratedJSONAcceptedByRealXray26327(t *testing.T) {
	binary := os.Getenv("XUI_HEALTHCHECK_XRAY_BIN")
	if binary == "" {
		t.Skip("CI pinned Xray binary is not installed")
	}
	config := mytrnTestConfig(t)
	// CI builds Xray-core from source without external geoip.dat assets.
	// Only this isolated run -test fixture substitutes a literal private CIDR
	// for the existing template's geoip:private block. The production merger
	// and all other tests preserve the original geoip:private rule unchanged.
	config.RouterConfig = []byte(strings.ReplaceAll(string(config.RouterConfig),
		"geoip:private", "192.168.0.0/16"))
	item := mytrnTestRecord(t)
	certificate := filepath.Join(t.TempDir(), "mytrn-a-cert.pem")
	if err := os.WriteFile(certificate, []byte(item.CertificatePEM), 0600); err != nil {
		t.Fatal(err)
	}
	if err := mergeMyTRNConfig(config, item, certificate); err != nil {
		t.Fatal(err)
	}
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "xray.json")
	if err := os.WriteFile(configPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-test", "-config", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("real Xray 26.3.27 rejected MyTRN merged config: %v\n%s\n%s", err, output, payload)
	}
	if !strings.Contains(string(output), "Reading config") {
		t.Logf("Xray validation output: %s", output)
	}
}

func TestMyTRNRejectsCatchallRoutingBeforeReverseRule(t *testing.T) {
	item := mytrnTestRecord(t)
	for _, catchall := range []string{
		`{"type":"field","outboundTag":"blocked"}`,
		`{"type":"field","network":"tcp,udp","outboundTag":"blocked"}`,
		`{"type":"field","port":"1-65535","outboundTag":"blocked"}`,
		`{"type":"field","ip":["0.0.0.0/0"],"outboundTag":"blocked"}`,
		`{"type":"field","domain":["regexp:.*"],"outboundTag":"blocked"}`,
		`{"type":"field","inboundTag":["mytrn-data-in"],"outboundTag":"blocked"}`,
	} {
		config := mytrnTestConfig(t)
		var routing map[string]json.RawMessage
		if err := json.Unmarshal(config.RouterConfig, &routing); err != nil {
			t.Fatal(err)
		}
		var rules []json.RawMessage
		if err := json.Unmarshal(routing["rules"], &rules); err != nil {
			t.Fatal(err)
		}
		rules = append(rules, json.RawMessage(catchall))
		routing["rules"], _ = json.Marshal(rules)
		config.RouterConfig, _ = json.Marshal(routing)
		if err := mergeMyTRNConfig(config, item, "valid.pem"); err == nil {
			t.Fatalf("MyTRN accepted a shadowing route: %s", catchall)
		}
	}
	// An inbound-specific policy for the existing control VLESS must not
	// be mistaken for a rule that can shadow MyTRN's internal reverse tag.
	config := mytrnTestConfig(t)
	var routing map[string]json.RawMessage
	_ = json.Unmarshal(config.RouterConfig, &routing)
	var rules []json.RawMessage
	_ = json.Unmarshal(routing["rules"], &rules)
	rules = append(rules, json.RawMessage(`{"type":"field","inboundTag":["inbound-26417"],"outboundTag":"blocked"}`))
	routing["rules"], _ = json.Marshal(rules)
	config.RouterConfig, _ = json.Marshal(routing)
	if err := mergeMyTRNConfig(config, item, "valid.pem"); err != nil {
		t.Fatalf("rejected unrelated inbound-scoped policy: %v", err)
	}
}

func TestMyTRNStatusUsesRunningConfigurationNotSavedEndpoint(t *testing.T) {
	item := mytrnTestRecord(t)
	if status, _ := mytrnStatus(item, nil, ""); status != "pending_apply" {
		t.Fatalf("saved endpoint without running Xray is %q, want pending_apply", status)
	}
	if status, message := mytrnStatus(item, nil, "injected Xray startup error"); status != "apply_failed" ||
		!strings.Contains(message, "injected") {
		t.Fatalf("failed Xray start was not surfaced: %q %q", status, message)
	}
	config := mytrnTestConfig(t)
	if err := mergeMyTRNConfig(config, item, "a-cert.pem"); err != nil {
		t.Fatal(err)
	}
	if status, message := mytrnStatus(item, config, ""); status != "applied" || message != "" {
		t.Fatalf("running Xray with matching config: status=%q message=%q, want applied without unverified-traffic claim", status, message)
	}
	newEndpoint := *item
	newEndpoint.EndpointPort++
	if status, _ := mytrnStatus(&newEndpoint, config, ""); status != "applied" {
		t.Fatalf("NAT endpoint-only change must NOT invalidate the running Xray: status=%q", status)
	}
	rotated := *item
	rotated.CertificatePEM = mytrnTestCert(t)
	rotated.CertificateFingerprint, _ = parseACertificate(rotated.CertificatePEM)
	if status, _ := mytrnStatus(&rotated, config, ""); status != "pending_apply" {
		t.Fatalf("running Xray with previous trusted certificate is %q, want pending_apply", status)
	}
	otherData, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	other := &xray.Config{}
	if err := json.Unmarshal(otherData, other); err != nil {
		t.Fatal(err)
	}
	other.MyTRNCertFingerprint = rotated.CertificateFingerprint
	if config.Equals(other) {
		t.Fatal("different trusted A certificate fingerprint was ignored in Xray config equality")
	}
	data, err := json.Marshal(config)
	if err != nil || bytes.Contains(data, []byte("MyTRNCertFingerprint")) || bytes.Contains(data, []byte("mytrnCertFingerprint")) {
		t.Fatalf("process-local certificate fingerprint must not leak into Xray JSON: %v", err)
	}
}

func TestMyTRNFailedRestartIsRequeuedAndFailureCanClear(t *testing.T) {
	previousFlag := isNeedXrayRestart.Load()
	previousMessage := xrayApplyFailure()
	defer func() {
		isNeedXrayRestart.Store(previousFlag)
		lastXrayApplyError.Lock()
		lastXrayApplyError.message = previousMessage
		lastXrayApplyError.Unlock()
	}()
	isNeedXrayRestart.Store(false) // the cron scheduler has consumed it
	recordXrayApplyResult(fmt.Errorf("transient Xray run -test failure"))
	if !isNeedXrayRestart.Load() || !strings.Contains(xrayApplyFailure(), "transient") {
		t.Fatal("failed Xray restart was lost after cron consumed its flag")
	}
	recordXrayApplyResult(nil)
	if xrayApplyFailure() != "" {
		t.Fatal("successful Xray restart left stale failure message")
	}
}

func TestMyTRNInvalidCertificateDoesNotBlockOtherXrayInbounds(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "mytrn-invalid-cert.db")); err != nil {
		t.Fatal(err)
	}
	defer setMyTRNConfigIssue("")
	item := mytrnTestRecord(t)
	item.CertificatePEM = mytrnTestCertWithValidity(t, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	// Fingerprint was pinned when the now-expired certificate was valid.
	block, _ := pem.Decode([]byte(item.CertificatePEM))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.Raw)
	item.CertificateFingerprint = hex.EncodeToString(sum[:])
	if err := database.GetDB().Save(item).Error; err != nil {
		t.Fatal(err)
	}
	config := mytrnTestConfig(t)
	before, _ := json.Marshal(config)
	if err := (&MyTRNService{}).ApplyToXrayConfig(config); err != nil {
		t.Fatalf("expired A TLS cert must not prevent existing VLESS startup: %v", err)
	}
	after, _ := json.Marshal(config)
	if !bytes.Equal(before, after) {
		t.Fatal("expired MyTRN certificate modified unrelated existing Xray config")
	}
	view, err := (&MyTRNService{}).View()
	if err != nil || view.Status != "invalid_certificate" {
		t.Fatalf("expired A certificate should show explicit re-trust message: %#v, %v", view, err)
	}
}

func TestMyTRNControlBindAddressSamePortNeedsOnlyPanelRestart(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "mytrn-listener-switch.db")); err != nil {
		t.Fatal(err)
	}
	defer StopMyTRNControl()
	defer StopMyTRNRelay()
	service := &MyTRNService{}
	port := freeMyTRNPort(t)
	settings := MyTRNSettings{
		Enable: true, Remark: "MyTRN", UUID: mytrnTestUUID,
		ControlToken: mytrnTestToken, ControlListen: "127.0.0.1", ControlPort: port,
		WarpHost: "127.0.0.1", WarpPort: 40000,
		Hy2UpMbps: 64, Hy2DownMbps: 64,
	}
	if _, err := service.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	settings.ControlListen = "0.0.0.0"
	settings.ControlToken = "" // keep the existing token
	if _, err := service.UpdateSettings(settings); err != nil {
		t.Fatalf("same-port loopback -> wildcard change should be saved: %v", err)
	}
	view, err := service.View()
	if err != nil || !view.ControlListenerRestartRequired {
		t.Fatalf("panel restart requirement was not advertised: %#v %v", view, err)
	}
	item, err := service.Get()
	if err != nil || item.ControlListen != "0.0.0.0" {
		t.Fatalf("new listener address not persisted: %#v %v", item, err)
	}
	StopMyTRNControl() // model the user restarting only the x-ui panel
	if err := StartMyTRNControl(); err != nil {
		t.Fatalf("cold-start of saved wildcard listener failed: %v", err)
	}
	view, err = service.View()
	if err != nil || view.ControlListenerRestartRequired || view.ControlListenerMessage != "" {
		t.Fatalf("control listener still reports pending restart after it was rebound: %#v %v", view, err)
	}
}
