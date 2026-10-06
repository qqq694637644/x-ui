package service

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

const (
	managedCaddyBegin = "# BEGIN XUI MANAGED ENDPOINTS"
	managedCaddyEnd   = "# END XUI MANAGED ENDPOINTS"
	managedHealthLeaf = "__xui_health"
)

type ManagedRoute struct {
	Host         string
	Path         string
	Network      string
	UpstreamHost string
	UpstreamPort int
	Kind         string
}

type ManagedProbe struct {
	Host      string
	Path      string
	LocalHost string
	LocalPort int
}

func RenderManagedCaddy(baseDomain string, publicPort int, certFile string, keyFile string, routes []ManagedRoute) (string, []ManagedProbe, error) {
	if len(routes) == 0 {
		return managedCaddyBegin + "\n# no managed endpoints\n" + managedCaddyEnd, nil, nil
	}
	baseDomain = normalizeDomain(baseDomain)
	if !validDomain(baseDomain) {
		return "", nil, fmt.Errorf("invalid public base domain: %s", baseDomain)
	}
	if publicPort <= 0 || publicPort > 65535 {
		return "", nil, fmt.Errorf("invalid public port: %d", publicPort)
	}
	if (strings.TrimSpace(certFile) == "") != (strings.TrimSpace(keyFile) == "") {
		return "", nil, fmt.Errorf("caddy TLS cert and key must both be set or both be empty")
	}

	normalized := make([]ManagedRoute, 0, len(routes))
	seen := map[string]bool{}
	for _, route := range routes {
		route.Host = strings.ToLower(strings.TrimSpace(route.Host))
		route.Path = normalizeManagedPath(route.Path)
		if route.Host == "" || route.Path == "" {
			return "", nil, fmt.Errorf("managed route host/path must not be empty")
		}
		if route.UpstreamPort <= 0 || route.UpstreamPort > 65535 {
			return "", nil, fmt.Errorf("invalid upstream port for %s: %d", route.Host, route.UpstreamPort)
		}
		if route.UpstreamHost == "" || route.UpstreamHost == "0.0.0.0" || route.UpstreamHost == "::" || route.UpstreamHost == "[::]" {
			route.UpstreamHost = "127.0.0.1"
		}
		if strings.ContainsAny(route.Path, " \t\r\n{}") {
			return "", nil, fmt.Errorf("invalid managed route path: %s", route.Path)
		}
		key := route.Host + "||" + route.Path + "||" + strconv.Itoa(route.UpstreamPort)
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, route)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Host != normalized[j].Host {
			return normalized[i].Host < normalized[j].Host
		}
		if normalized[i].Path != normalized[j].Path {
			return normalized[i].Path < normalized[j].Path
		}
		return normalized[i].UpstreamPort < normalized[j].UpstreamPort
	})

	var b strings.Builder
	b.WriteString(managedCaddyBegin)
	b.WriteString("\n")
	if len(normalized) == 0 {
		b.WriteString("# no managed endpoints\n")
		b.WriteString(managedCaddyEnd)
		return b.String(), nil, nil
	}

	site := "*." + baseDomain
	if publicPort != 443 {
		site = net.JoinHostPort(site, strconv.Itoa(publicPort))
	}
	b.WriteString(site)
	b.WriteString(" {\n")
	if strings.TrimSpace(certFile) != "" {
		b.WriteString("    tls ")
		b.WriteString(strings.TrimSpace(certFile))
		b.WriteByte(' ')
		b.WriteString(strings.TrimSpace(keyFile))
		b.WriteString("\n")
	}

	probes := make([]ManagedProbe, 0, len(normalized))
	for i, route := range normalized {
		healthMatcher := fmt.Sprintf("xui_health_%d", i+1)
		routeMatcher := fmt.Sprintf("xui_route_%d", i+1)
		healthPath := managedHealthPath(route.Path)

		b.WriteString("\n    @")
		b.WriteString(healthMatcher)
		b.WriteString(" {\n        host ")
		b.WriteString(route.Host)
		b.WriteString("\n        path ")
		b.WriteString(healthPath)
		b.WriteString("\n    }\n")
		b.WriteString("    handle @")
		b.WriteString(healthMatcher)
		b.WriteString(" {\n        respond \"\" 204\n    }\n")

		b.WriteString("\n    @")
		b.WriteString(routeMatcher)
		b.WriteString(" {\n        host ")
		b.WriteString(route.Host)
		b.WriteString("\n        path ")
		b.WriteString(route.Path)
		b.WriteString("*\n    }\n")
		b.WriteString("    handle @")
		b.WriteString(routeMatcher)
		b.WriteString(" {\n        reverse_proxy ")
		b.WriteString(managedUpstream(route))
		b.WriteString("\n    }\n")

		probes = append(probes, ManagedProbe{
			Host:      route.Host,
			Path:      healthPath,
			LocalHost: route.UpstreamHost,
			LocalPort: route.UpstreamPort,
		})
	}
	b.WriteString("\n    handle {\n        respond \"Not Found\" 404\n    }\n")
	b.WriteString("}\n")
	b.WriteString(managedCaddyEnd)
	return b.String(), probes, nil
}

func ReplaceManagedCaddyBlock(content string, block string) (string, error) {
	begin := strings.Index(content, managedCaddyBegin)
	end := strings.Index(content, managedCaddyEnd)
	if begin < 0 && end < 0 {
		trimmed := strings.TrimRight(content, "\r\n")
		if trimmed == "" {
			return block + "\n", nil
		}
		return trimmed + "\n\n" + block + "\n", nil
	}
	if begin < 0 || end < 0 || end < begin {
		return "", fmt.Errorf("caddy managed block markers are incomplete or out of order")
	}
	end += len(managedCaddyEnd)
	prefix := strings.TrimRight(content[:begin], "\r\n")
	suffix := strings.TrimLeft(content[end:], "\r\n")
	var b strings.Builder
	if prefix != "" {
		b.WriteString(prefix)
		b.WriteString("\n\n")
	}
	b.WriteString(block)
	b.WriteString("\n")
	if suffix != "" {
		b.WriteString("\n")
		b.WriteString(suffix)
		if !strings.HasSuffix(suffix, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func (s *CaddyService) ApplyManagedBlock(block string) (string, *CaddyCommandResult, error) {
	config, err := s.GetConfig()
	if err != nil {
		return "", nil, err
	}
	next, err := ReplaceManagedCaddyBlock(config.Content, block)
	if err != nil {
		return "", nil, err
	}
	result, err := s.SaveAndReload(next)
	if err != nil {
		return config.Content, result, err
	}
	return config.Content, result, nil
}

func (s *CaddyService) RestoreContent(content string) error {
	_, err := s.SaveAndReload(content)
	return err
}

func normalizeManagedPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if len(value) > 1 {
		value = strings.TrimRight(value, "*")
	}
	return value
}

func managedHealthPath(path string) string {
	path = normalizeManagedPath(path)
	if path == "/" {
		return "/" + managedHealthLeaf
	}
	return strings.TrimRight(path, "/") + "/" + managedHealthLeaf
}

func managedUpstream(route ManagedRoute) string {
	target := net.JoinHostPort(strings.Trim(route.UpstreamHost, "[]"), strconv.Itoa(route.UpstreamPort))
	switch strings.ToLower(strings.TrimSpace(route.Network)) {
	case "xhttp", "http", "grpc":
		return "h2c://" + target
	default:
		return target
	}
}
