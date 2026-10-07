package service

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"x-ui/database/model"
)

type LinkService struct{}

type managedInboundSpec struct {
	ClientID string
	Path     string
	Mode     string
}

type managedStreamSettings struct {
	Network  string `json:"network"`
	Security string `json:"security"`
	XHTTP    struct {
		Path string `json:"path"`
		Host string `json:"host"`
		Mode string `json:"mode"`
	} `json:"xhttpSettings"`
}

type managedVLESSSettings struct {
	Clients []struct {
		ID   string `json:"id"`
		Flow string `json:"flow"`
	} `json:"clients"`
	Decryption string            `json:"decryption"`
	Fallbacks  []json.RawMessage `json:"fallbacks"`
}

func validateManagedFixedPath(value string) (string, error) {
	path := strings.TrimSpace(value)
	if path == "" || !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("managed XHTTP path must start with /")
	}
	if strings.ContainsAny(path, "?#{* \t\r\n") || strings.Contains(path, "/"+managedHealthLeaf) {
		return "", fmt.Errorf("managed XHTTP path must be a plain fixed path without query, wildcard, whitespace, or reserved health segment")
	}
	return path, nil
}

func validateManagedInbound(inbound *model.Inbound) (*managedInboundSpec, error) {
	if inbound == nil {
		return nil, fmt.Errorf("inbound is required")
	}
	if inbound.Protocol != model.VLESS {
		return nil, fmt.Errorf("managed endpoint only supports VLESS, got %s", inbound.Protocol)
	}
	if strings.TrimSpace(inbound.Listen) != "127.0.0.1" {
		return nil, fmt.Errorf("managed VLESS/XHTTP inbound must listen on 127.0.0.1")
	}
	if inbound.Port <= 0 || inbound.Port > 65535 {
		return nil, fmt.Errorf("managed inbound port is invalid: %d", inbound.Port)
	}

	stream := &managedStreamSettings{}
	if err := json.Unmarshal([]byte(inbound.StreamSettings), stream); err != nil {
		return nil, fmt.Errorf("invalid stream settings: %w", err)
	}
	if strings.ToLower(strings.TrimSpace(stream.Network)) != "xhttp" {
		return nil, fmt.Errorf("managed endpoint only supports XHTTP transport")
	}
	if strings.ToLower(strings.TrimSpace(stream.Security)) != "none" {
		return nil, fmt.Errorf("managed VLESS/XHTTP inbound must use internal security=none")
	}
	if strings.TrimSpace(stream.XHTTP.Host) != "" {
		return nil, fmt.Errorf("managed VLESS/XHTTP inbound xhttpSettings.host must be empty")
	}
	path, err := validateManagedFixedPath(stream.XHTTP.Path)
	if err != nil {
		return nil, fmt.Errorf("managed VLESS/XHTTP inbound path is invalid: %w", err)
	}
	mode := strings.TrimSpace(stream.XHTTP.Mode)
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "auto", "packet-up", "stream-up", "stream-one":
	default:
		return nil, fmt.Errorf("unsupported managed XHTTP mode: %s", mode)
	}

	settings := &managedVLESSSettings{}
	if err := json.Unmarshal([]byte(inbound.Settings), settings); err != nil {
		return nil, fmt.Errorf("invalid VLESS settings: %w", err)
	}
	if strings.ToLower(strings.TrimSpace(settings.Decryption)) != "none" {
		return nil, fmt.Errorf("managed VLESS inbound decryption must be none")
	}
	if len(settings.Fallbacks) != 0 {
		return nil, fmt.Errorf("managed VLESS inbound fallbacks are not supported")
	}
	if len(settings.Clients) != 1 {
		return nil, fmt.Errorf("managed VLESS inbound must contain exactly one client")
	}
	if strings.TrimSpace(settings.Clients[0].Flow) != "" {
		return nil, fmt.Errorf("managed VLESS/XHTTP client flow must be empty")
	}
	clientID, err := model.NormalizeUUID(settings.Clients[0].ID)
	if err != nil {
		return nil, fmt.Errorf("invalid managed VLESS client id: %w", err)
	}

	return &managedInboundSpec{
		ClientID: clientID,
		Path:     path,
		Mode:     mode,
	}, nil
}

func (s *LinkService) GenerateInboundLink(inbound *model.Inbound, endpoint *model.PublicEndpoint) (string, error) {
	if endpoint == nil {
		return "", fmt.Errorf("public endpoint is required")
	}
	host := strings.ToLower(strings.Trim(strings.TrimSpace(endpoint.Host), "."))
	if host == "" {
		return "", fmt.Errorf("public endpoint host is empty")
	}
	if endpoint.Port <= 0 || endpoint.Port > 65535 {
		return "", fmt.Errorf("public endpoint port is invalid: %d", endpoint.Port)
	}
	spec, err := validateManagedInbound(inbound)
	if err != nil {
		return "", err
	}

	params := url.Values{}
	params.Set("type", "xhttp")
	params.Set("security", "tls")
	params.Set("path", spec.Path)
	params.Set("host", host)
	params.Set("mode", spec.Mode)
	params.Set("sni", host)
	params.Set("alpn", "http/1.1")

	base := fmt.Sprintf("vless://%s@%s", spec.ClientID, joinHostPort(host, endpoint.Port))
	return base + "?" + params.Encode() + "#" + escapeFragment(inbound.Remark), nil
}

func joinHostPort(host string, port int) string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func escapeFragment(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}
