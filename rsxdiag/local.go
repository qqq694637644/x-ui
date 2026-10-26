package rsxdiag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// LocalClient never uses HTTP_PROXY or resolves DNS. Its only allowed peer is
// a literal IPv4 loopback endpoint configured for the private frpc API.
type LocalClient struct{ Addr, Token string }

func (c LocalClient) Validate() error {
	h, p, e := net.SplitHostPort(c.Addr)
	n, er := strconv.Atoi(p)
	if e != nil || er != nil || h != "127.0.0.1" || n < 1 || n > 65535 {
		return errors.New("diagnostic endpoint must be 127.0.0.1:port")
	}
	if len(c.Token) < 32 {
		return errors.New("diagnostic token too short")
	}
	return nil
}
func (c LocalClient) Do(ctx context.Context, method, path string, body interface{}, maxBytes int64) (json.RawMessage, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, "/v1/") || strings.ContainsAny(path, "\r\n") {
		return nil, errors.New("invalid local diagnostic path")
	}
	if maxBytes <= 0 || maxBytes > 64<<20 {
		maxBytes = 4 << 20
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.Addr+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, errors.New("frpc diagnostic response too large")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("frpc diagnostics HTTP %d: %s", resp.StatusCode, string(b))
	}
	if !json.Valid(b) {
		return nil, errors.New("frpc returned non-JSON diagnostics")
	}
	return b, nil
}
func (c LocalClient) Lookup(ctx context.Context, local, remote string, at ...string) (interface{}, error) {
	q := url.Values{"local": {local}, "remote": {remote}}
	if len(at) > 0 && at[0] != "" {
		q.Set("at", at[0])
	}
	raw, err := c.Do(ctx, "GET", "/v1/lookup?"+q.Encode(), nil, 16<<10)
	if err != nil {
		return nil, err
	}
	var v interface{}
	err = json.Unmarshal(raw, &v)
	return v, err
}
