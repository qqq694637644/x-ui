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
	UpstreamHost string
	UpstreamPort int
	Kind         string
}

func RenderManagedCaddy(baseDomain string, publicPort int, certFile string, keyFile string, routes []ManagedRoute) (string, error) {
	if len(routes) == 0 {
		return managedCaddyBegin + "\n# no managed endpoints\n" + managedCaddyEnd, nil
	}
	baseDomain = normalizeDomain(baseDomain)
	if !validDomain(baseDomain) {
		return "", fmt.Errorf("invalid public base domain: %s", baseDomain)
	}
	if publicPort <= 0 || publicPort > 65535 {
		return "", fmt.Errorf("invalid public port: %d", publicPort)
	}
	certFile = strings.TrimSpace(certFile)
	keyFile = strings.TrimSpace(keyFile)
	if certFile == "" || keyFile == "" {
		return "", fmt.Errorf("managed endpoints require wildcard Caddy TLS certificate and key")
	}

	normalized := make([]ManagedRoute, 0, len(routes))
	seen := map[string]string{}
	for _, route := range routes {
		route.Host = strings.ToLower(strings.TrimSpace(route.Host))
		route.Path = normalizeManagedPath(route.Path)
		if route.Host == "" || route.Path == "" {
			return "", fmt.Errorf("managed route host/path must not be empty")
		}
		if !isDirectManagedSubdomain(route.Host, baseDomain) {
			return "", fmt.Errorf("managed route host %s is not a direct subdomain of %s", route.Host, baseDomain)
		}
		if route.UpstreamPort <= 0 || route.UpstreamPort > 65535 {
			return "", fmt.Errorf("invalid upstream port for %s: %d", route.Host, route.UpstreamPort)
		}
		if strings.TrimSpace(route.UpstreamHost) != "127.0.0.1" {
			return "", fmt.Errorf("managed XHTTP upstream must be 127.0.0.1, got %s", route.UpstreamHost)
		}
		if strings.ContainsAny(route.Path, " \t\r\n{}") {
			return "", fmt.Errorf("invalid managed route path: %s", route.Path)
		}
		key := route.Host + "||" + route.Path
		target := net.JoinHostPort(route.UpstreamHost, strconv.Itoa(route.UpstreamPort))
		if previous, exists := seen[key]; exists {
			if previous != target {
				return "", fmt.Errorf("conflicting managed route %s%s: %s vs %s", route.Host, route.Path, previous, target)
			}
			continue
		}
		seen[key] = target
		normalized = append(normalized, route)
	}

	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Host != normalized[j].Host {
			return normalized[i].Host < normalized[j].Host
		}
		if len(normalized[i].Path) != len(normalized[j].Path) {
			return len(normalized[i].Path) > len(normalized[j].Path)
		}
		if normalized[i].Path != normalized[j].Path {
			return normalized[i].Path < normalized[j].Path
		}
		return normalized[i].UpstreamPort < normalized[j].UpstreamPort
	})

	var b strings.Builder
	b.WriteString(managedCaddyBegin)
	b.WriteString("\n")
	site := "*." + baseDomain
	if publicPort != 443 {
		site = net.JoinHostPort(site, strconv.Itoa(publicPort))
	}
	b.WriteString(site)
	b.WriteString(" {\n")
	b.WriteString("    tls ")
	b.WriteString(certFile)
	b.WriteByte(' ')
	b.WriteString(keyFile)
	b.WriteString("\n")

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
		b.WriteString(" {\n        header Cache-Control \"no-store\"\n        respond \"\" 204\n    }\n")

		b.WriteString("\n    @")
		b.WriteString(routeMatcher)
		b.WriteString(" {\n        host ")
		b.WriteString(route.Host)
		b.WriteString("\n        path ")
		b.WriteString(route.Path)
		b.WriteString("*\n    }\n")
		b.WriteString("    handle @")
		b.WriteString(routeMatcher)
		b.WriteString(" {\n        reverse_proxy h2c://")
		b.WriteString(net.JoinHostPort(route.UpstreamHost, strconv.Itoa(route.UpstreamPort)))
		b.WriteString("\n    }\n")
	}
	b.WriteString("\n    handle {\n        respond \"Not Found\" 404\n    }\n")
	b.WriteString("}\n")
	b.WriteString(managedCaddyEnd)
	return b.String(), nil
}

func ReplaceOwnedManagedCaddy(content string, baseDomain string, block string) (string, error) {
	withoutManaged, err := removeManagedCaddyBlock(content)
	if err != nil {
		return "", err
	}
	cleaned, err := stripOwnedCaddySites(withoutManaged, baseDomain)
	if err != nil {
		return "", err
	}
	cleaned = strings.TrimRight(cleaned, "\r\n")
	if cleaned == "" {
		return block + "\n", nil
	}
	return cleaned + "\n\n" + block + "\n", nil
}

func removeManagedCaddyBlock(content string) (string, error) {
	begin := strings.Index(content, managedCaddyBegin)
	end := strings.Index(content, managedCaddyEnd)
	if begin < 0 && end < 0 {
		return content, nil
	}
	if begin < 0 || end < 0 || end < begin {
		return "", fmt.Errorf("caddy managed block markers are incomplete or out of order")
	}
	end += len(managedCaddyEnd)
	return content[:begin] + content[end:], nil
}

func stripOwnedCaddySites(content string, baseDomain string) (string, error) {
	baseDomain = normalizeDomain(baseDomain)
	if !validDomain(baseDomain) {
		return "", fmt.Errorf("invalid public base domain: %s", baseDomain)
	}
	lines := strings.SplitAfter(content, "\n")
	var out strings.Builder
	depth := 0
	skipping := false
	for _, line := range lines {
		delta, header, hasOpen, err := caddyLineStructure(line)
		if err != nil {
			return "", err
		}
		if depth == 0 && !skipping && hasOpen {
			owned, mixed := classifyOwnedCaddyHeader(header, baseDomain)
			if mixed {
				return "", fmt.Errorf("caddy site block mixes managed and unmanaged addresses: %s", strings.TrimSpace(header))
			}
			if owned {
				skipping = true
				depth += delta
				if depth == 0 {
					skipping = false
				}
				continue
			}
		}
		if skipping {
			depth += delta
			if depth < 0 {
				return "", fmt.Errorf("invalid Caddyfile brace structure")
			}
			if depth == 0 {
				skipping = false
			}
			continue
		}
		out.WriteString(line)
		depth += delta
		if depth < 0 {
			return "", fmt.Errorf("invalid Caddyfile brace structure")
		}
	}
	if depth != 0 || skipping {
		return "", fmt.Errorf("invalid Caddyfile brace structure")
	}
	return out.String(), nil
}

func caddyLineStructure(line string) (delta int, header string, hasOpen bool, err error) {
	inQuote := false
	escaped := false
	openIndex := -1
	for i, ch := range line {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inQuote {
			escaped = true
			continue
		}
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && ch == '#' {
			break
		}
		if inQuote {
			continue
		}
		switch ch {
		case '{':
			if openIndex < 0 {
				openIndex = i
			}
			delta++
		case '}':
			delta--
		}
	}
	if inQuote {
		return 0, "", false, fmt.Errorf("unterminated quote in Caddyfile line: %s", strings.TrimSpace(line))
	}
	if openIndex >= 0 {
		hasOpen = true
		header = strings.TrimSpace(line[:openIndex])
	}
	return delta, header, hasOpen, nil
}

func classifyOwnedCaddyHeader(header string, baseDomain string) (owned bool, mixed bool) {
	header = strings.TrimSpace(header)
	if header == "" || strings.HasPrefix(header, "(") {
		return false, false
	}
	parts := strings.FieldsFunc(header, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	ownedCount := 0
	otherCount := 0
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if ownedCaddyAddress(part, baseDomain) {
			ownedCount++
		} else {
			otherCount++
		}
	}
	if ownedCount > 0 && otherCount > 0 {
		return false, true
	}
	return ownedCount > 0 && otherCount == 0, false
}

func ownedCaddyAddress(address string, baseDomain string) bool {
	address = strings.TrimSpace(strings.Trim(address, ","))
	if address == "" {
		return false
	}
	if idx := strings.Index(address, "://"); idx >= 0 {
		address = address[idx+3:]
	}
	if idx := strings.IndexAny(address, "/{"); idx >= 0 {
		address = address[:idx]
	}
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	} else if idx := strings.LastIndex(address, ":"); idx > 0 {
		if _, parseErr := strconv.Atoi(address[idx+1:]); parseErr == nil {
			address = address[:idx]
		}
	}
	host := strings.ToLower(strings.Trim(strings.TrimSpace(address), "[]."))
	if host == "*."+baseDomain {
		return true
	}
	suffix := "." + baseDomain
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	label := strings.TrimSuffix(host, suffix)
	return label != "" && label != "*" && !strings.Contains(label, ".")
}

func isDirectManagedSubdomain(host string, baseDomain string) bool {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "."))
	baseDomain = normalizeDomain(baseDomain)
	suffix := "." + baseDomain
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	label := strings.TrimSuffix(host, suffix)
	return label != "" && label != "*" && !strings.Contains(label, ".")
}

func (s *CaddyService) ApplyManagedSite(baseDomain string, block string) (string, *CaddyCommandResult, error) {
	config, err := s.GetConfig()
	if err != nil {
		return "", nil, err
	}
	next, err := ReplaceOwnedManagedCaddy(config.Content, baseDomain, block)
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
