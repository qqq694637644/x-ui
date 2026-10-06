package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"x-ui/database/model"
)

type LinkService struct{}

type inboundTransport struct {
	Network      string
	Path         string
	Host         string
	Mode         string
	Type         string
	QuicSecurity string
	QuicKey      string
	ServiceName  string
	ALPN         []string
	ServerName   string
}

func (s *LinkService) GenerateInboundLink(inbound *model.Inbound, endpoint *model.PublicEndpoint) (string, error) {
	if inbound == nil || endpoint == nil {
		return "", fmt.Errorf("inbound and endpoint are required")
	}
	if strings.TrimSpace(endpoint.Host) == "" {
		return "", fmt.Errorf("public endpoint host is empty")
	}
	if endpoint.Port <= 0 || endpoint.Port > 65535 {
		return "", fmt.Errorf("public endpoint port is invalid: %d", endpoint.Port)
	}

	switch inbound.Protocol {
	case model.VMess:
		return s.genVMessLink(inbound, endpoint)
	case model.VLESS:
		return s.genVLESSLink(inbound, endpoint)
	case model.Trojan:
		return s.genTrojanLink(inbound, endpoint)
	case model.Shadowsocks:
		return s.genShadowsocksLink(inbound, endpoint)
	default:
		return "", fmt.Errorf("protocol %s does not support share links", inbound.Protocol)
	}
}

func inboundTransportInfo(inbound *model.Inbound) (*inboundTransport, error) {
	info := &inboundTransport{Network: "tcp", Path: "/", Mode: "auto"}
	stream := map[string]interface{}{}
	if strings.TrimSpace(inbound.StreamSettings) != "" {
		if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
			return nil, fmt.Errorf("invalid stream settings: %w", err)
		}
	}
	if network, _ := stream["network"].(string); network != "" {
		info.Network = network
	}
	switch info.Network {
	case "tcp":
		if tcp, _ := stream["tcpSettings"].(map[string]interface{}); tcp != nil {
			if header, _ := tcp["header"].(map[string]interface{}); header != nil {
				info.Type, _ = header["type"].(string)
				if info.Type == "http" {
					if request, _ := header["request"].(map[string]interface{}); request != nil {
						info.Path = firstString(request["path"])
						if info.Path == "" {
							info.Path = "/"
						}
						if headers, _ := request["headers"].(map[string]interface{}); headers != nil {
							info.Host = headerValue(headers, "Host")
						}
					}
				}
			}
		}
	case "ws":
		if ws, _ := stream["wsSettings"].(map[string]interface{}); ws != nil {
			info.Path, _ = ws["path"].(string)
			if info.Path == "" {
				info.Path = "/"
			}
			if headers, _ := ws["headers"].(map[string]interface{}); headers != nil {
				info.Host = headerValue(headers, "Host")
			}
		}
	case "http":
		if h2, _ := stream["httpSettings"].(map[string]interface{}); h2 != nil {
			info.Path, _ = h2["path"].(string)
			if info.Path == "" {
				info.Path = "/"
			}
			info.Host = firstString(h2["host"])
		}
	case "quic":
		if quic, _ := stream["quicSettings"].(map[string]interface{}); quic != nil {
			info.QuicSecurity, _ = quic["security"].(string)
			info.QuicKey, _ = quic["key"].(string)
			if header, _ := quic["header"].(map[string]interface{}); header != nil {
				info.Type, _ = header["type"].(string)
			}
		}
	case "grpc":
		if grpc, _ := stream["grpcSettings"].(map[string]interface{}); grpc != nil {
			info.ServiceName, _ = grpc["serviceName"].(string)
			info.Path = "/" + strings.TrimPrefix(info.ServiceName, "/")
			if info.Path == "/" {
				info.Path = "/"
			}
		}
	case "xhttp":
		if xhttp, _ := stream["xhttpSettings"].(map[string]interface{}); xhttp != nil {
			info.Path, _ = xhttp["path"].(string)
			if info.Path == "" {
				info.Path = "/"
			}
			info.Host, _ = xhttp["host"].(string)
			if mode, _ := xhttp["mode"].(string); mode != "" {
				info.Mode = mode
			}
		}
	}
	if tlsSettings, _ := stream["tlsSettings"].(map[string]interface{}); tlsSettings != nil {
		info.ServerName, _ = tlsSettings["serverName"].(string)
		info.ALPN = stringSlice(tlsSettings["alpn"])
	}
	return info, nil
}

func (s *LinkService) genVLESSLink(inbound *model.Inbound, endpoint *model.PublicEndpoint) (string, error) {
	settings, err := decodeObject(inbound.Settings)
	if err != nil {
		return "", err
	}
	client := firstObject(settings["clients"])
	if client == nil {
		return "", fmt.Errorf("vless inbound has no client")
	}
	uuid, _ := client["id"].(string)
	if uuid == "" {
		return "", fmt.Errorf("vless client id is empty")
	}
	transport, err := inboundTransportInfo(inbound)
	if err != nil {
		return "", err
	}
	params := url.Values{}
	params.Set("type", transport.Network)
	applyTransportQuery(params, transport)
	applyPublicHostQuery(params, transport.Network, endpoint.Host)

	security := strings.TrimSpace(endpoint.Security)
	if security == "" {
		security = streamSecurity(inbound.StreamSettings)
	}
	if security == "" {
		security = "none"
	}
	params.Set("security", security)
	if security == "tls" {
		sni := strings.TrimSpace(endpoint.SNI)
		if sni == "" {
			sni = endpoint.Host
		}
		if sni == "" {
			sni = transport.ServerName
		}
		if sni != "" {
			params.Set("sni", sni)
		}
		if len(transport.ALPN) > 0 {
			params.Set("alpn", strings.Join(transport.ALPN, ","))
		}
	}
	if flow, _ := client["flow"].(string); flow != "" {
		params.Set("flow", flow)
	}
	if encryption, _ := settings["encryption"].(string); encryption != "" {
		params.Set("encryption", encryption)
	}

	base := fmt.Sprintf("vless://%s@%s", uuid, joinHostPort(endpoint.Host, endpoint.Port))
	return base + "?" + params.Encode() + "#" + escapeFragment(inbound.Remark), nil
}

func (s *LinkService) genVMessLink(inbound *model.Inbound, endpoint *model.PublicEndpoint) (string, error) {
	settings, err := decodeObject(inbound.Settings)
	if err != nil {
		return "", err
	}
	client := firstObject(settings["clients"])
	if client == nil {
		return "", fmt.Errorf("vmess inbound has no client")
	}
	transport, err := inboundTransportInfo(inbound)
	if err != nil {
		return "", err
	}
	network := transport.Network
	if network == "http" {
		network = "h2"
	}
	obj := map[string]interface{}{
		"v":    "2",
		"ps":   inbound.Remark,
		"add":  endpoint.Host,
		"port": endpoint.Port,
		"id":   stringValue(client["id"]),
		"aid":  intValue(client["alterId"]),
		"net":  network,
		"type": transport.Type,
		"host": transport.Host,
		"path": transport.Path,
		"tls":  endpoint.Security,
	}
	if transportUsesHTTPHost(transport.Network) {
		obj["host"] = endpoint.Host
	}
	if obj["tls"] == "" {
		obj["tls"] = streamSecurity(inbound.StreamSettings)
	}
	if endpoint.SNI != "" {
		obj["sni"] = endpoint.SNI
	} else if endpoint.Host != "" {
		obj["sni"] = endpoint.Host
	} else if transport.ServerName != "" {
		obj["sni"] = transport.ServerName
	}
	if len(transport.ALPN) > 0 {
		obj["alpn"] = strings.Join(transport.ALPN, ",")
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(data), nil
}

func (s *LinkService) genTrojanLink(inbound *model.Inbound, endpoint *model.PublicEndpoint) (string, error) {
	settings, err := decodeObject(inbound.Settings)
	if err != nil {
		return "", err
	}
	client := firstObject(settings["clients"])
	if client == nil {
		return "", fmt.Errorf("trojan inbound has no client")
	}
	password, _ := client["password"].(string)
	if password == "" {
		return "", fmt.Errorf("trojan client password is empty")
	}
	transport, err := inboundTransportInfo(inbound)
	if err != nil {
		return "", err
	}
	params := url.Values{}
	params.Set("type", transport.Network)
	applyTransportQuery(params, transport)
	applyPublicHostQuery(params, transport.Network, endpoint.Host)
	security := endpoint.Security
	if security == "" {
		security = streamSecurity(inbound.StreamSettings)
	}
	if security != "" {
		params.Set("security", security)
	}
	if endpoint.SNI != "" {
		params.Set("sni", endpoint.SNI)
	} else if endpoint.Host != "" {
		params.Set("sni", endpoint.Host)
	} else if transport.ServerName != "" {
		params.Set("sni", transport.ServerName)
	}
	if len(transport.ALPN) > 0 {
		params.Set("alpn", strings.Join(transport.ALPN, ","))
	}
	base := fmt.Sprintf("trojan://%s@%s", escapeUserinfo(password), joinHostPort(endpoint.Host, endpoint.Port))
	if len(params) > 0 {
		base += "?" + params.Encode()
	}
	return base + "#" + escapeFragment(inbound.Remark), nil
}

func (s *LinkService) genShadowsocksLink(inbound *model.Inbound, endpoint *model.PublicEndpoint) (string, error) {
	settings, err := decodeObject(inbound.Settings)
	if err != nil {
		return "", err
	}
	method, _ := settings["method"].(string)
	password, _ := settings["password"].(string)
	if method == "" || password == "" {
		return "", fmt.Errorf("shadowsocks method or password is empty")
	}
	raw := fmt.Sprintf("%s:%s@%s", method, password, joinHostPort(endpoint.Host, endpoint.Port))
	return "ss://" + base64.RawURLEncoding.EncodeToString([]byte(raw)) + "#" + escapeFragment(inbound.Remark), nil
}

func applyTransportQuery(params url.Values, transport *inboundTransport) {
	switch transport.Network {
	case "tcp":
		if transport.Type == "http" {
			params.Set("headerType", "http")
			if transport.Path != "" {
				params.Set("path", transport.Path)
			}
			if transport.Host != "" {
				params.Set("host", transport.Host)
			}
		}
	case "ws", "http":
		if transport.Path != "" {
			params.Set("path", transport.Path)
		}
		if transport.Host != "" {
			params.Set("host", transport.Host)
		}
	case "quic":
		if transport.QuicSecurity != "" {
			params.Set("quicSecurity", transport.QuicSecurity)
		}
		if transport.QuicKey != "" {
			params.Set("key", transport.QuicKey)
		}
		if transport.Type != "" {
			params.Set("headerType", transport.Type)
		}
	case "grpc":
		if transport.ServiceName != "" {
			params.Set("serviceName", transport.ServiceName)
		}
	case "xhttp":
		if transport.Path != "" {
			params.Set("path", transport.Path)
		}
		if transport.Host != "" {
			params.Set("host", transport.Host)
		}
		if transport.Mode != "" {
			params.Set("mode", transport.Mode)
		}
	}
}

func applyPublicHostQuery(params url.Values, network string, host string) {
	if strings.TrimSpace(host) == "" || !transportUsesHTTPHost(network) {
		return
	}
	params.Set("host", host)
}

func transportUsesHTTPHost(network string) bool {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "ws", "http", "xhttp":
		return true
	default:
		return false
	}
}

func streamSecurity(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var stream map[string]interface{}
	if json.Unmarshal([]byte(raw), &stream) != nil {
		return ""
	}
	value, _ := stream["security"].(string)
	return value
}

func decodeObject(raw string) (map[string]interface{}, error) {
	result := map[string]interface{}{}
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func firstObject(value interface{}) map[string]interface{} {
	switch items := value.(type) {
	case []interface{}:
		if len(items) == 0 {
			return nil
		}
		obj, _ := items[0].(map[string]interface{})
		return obj
	default:
		return nil
	}
}

func firstString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case []interface{}:
		if len(v) > 0 {
			s, _ := v[0].(string)
			return s
		}
	case []string:
		if len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func stringSlice(value interface{}) []string {
	result := []string{}
	switch values := value.(type) {
	case []interface{}:
		for _, item := range values {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				result = append(result, strings.TrimSpace(s))
			}
		}
	case []string:
		for _, s := range values {
			if strings.TrimSpace(s) != "" {
				result = append(result, strings.TrimSpace(s))
			}
		}
	case string:
		for _, s := range strings.Split(values, ",") {
			if strings.TrimSpace(s) != "" {
				result = append(result, strings.TrimSpace(s))
			}
		}
	}
	return result
}

func headerValue(headers map[string]interface{}, target string) string {
	for key, value := range headers {
		if strings.EqualFold(key, target) {
			return firstString(value)
		}
	}
	return ""
}

func stringValue(value interface{}) string {
	s, _ := value.(string)
	return s
}

func intValue(value interface{}) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := strconv.Atoi(v.String())
		return n
	}
	return 0
}

func joinHostPort(host string, port int) string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func escapeFragment(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func escapeUserinfo(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}
