package reverseexit

// Direct IPv6 QUIC reuses authenticated RSX3 streams without touching FRPC.
// The DDNS AAAA record is resolved on every reconnect and checked periodically.
import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	quic "github.com/quic-go/quic-go"
)

const directALPN = "rsx3-direct-v1"

type DirectConfig struct {
	Hostname   string
	Port       int
	CertSHA256 string
	Agent      Config
}

func (d DirectConfig) Validate() error {
	if net.ParseIP(d.Hostname) != nil || len(d.Hostname) < 3 ||
		len(d.Hostname) > 253 || !strings.Contains(d.Hostname, ".") ||
		strings.ContainsAny(d.Hostname, " /:\\\r\n\t") {
		return errors.New("direct QUIC requires a DDNS hostname, not a raw IP")
	}
	if d.Port < 1 || d.Port > 65535 {
		return errors.New("invalid direct QUIC UDP port")
	}
	if len(d.CertSHA256) != 64 {
		return errors.New("direct QUIC certificate SHA-256 must be 64 hex characters")
	}
	if _, err := hex.DecodeString(d.CertSHA256); err != nil {
		return err
	}
	d.Agent.DirectQUIC = true
	return d.Agent.Validate()
}

type directQUICTransport struct{ conn quic.Connection }

type quicNetConn struct {
	stream quic.Stream
	conn   quic.Connection
}

func (c *quicNetConn) Read(p []byte) (int, error)  { return c.stream.Read(p) }
func (c *quicNetConn) Write(p []byte) (int, error) { return c.stream.Write(p) }
func (c *quicNetConn) Close() error {
	c.stream.CancelRead(0)
	c.stream.CancelWrite(0)
	return c.stream.Close()
}
func (c *quicNetConn) CloseWrite() error                  { return c.stream.Close() }
func (c *quicNetConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *quicNetConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *quicNetConn) SetDeadline(t time.Time) error      { return c.stream.SetDeadline(t) }
func (c *quicNetConn) SetReadDeadline(t time.Time) error  { return c.stream.SetReadDeadline(t) }
func (c *quicNetConn) SetWriteDeadline(t time.Time) error { return c.stream.SetWriteDeadline(t) }

func (t *directQUICTransport) OpenStream(ctx context.Context) (net.Conn, error) {
	s, err := t.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &quicNetConn{stream: s, conn: t.conn}, nil
}
func (t *directQUICTransport) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	return t.conn.ReceiveDatagram(ctx)
}
func (t *directQUICTransport) SendDatagram(b []byte) error { return t.conn.SendDatagram(b) }

func directTLS(d DirectConfig) *tls.Config {
	want, _ := hex.DecodeString(d.CertSHA256)
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: d.Hostname,
		NextProtos: []string{directALPN},
		// Pinning is intentional for user-managed self-signed DDNS certificates:
		// the verification below enforces certificate identity, dates and pin.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("missing QUIC server certificate")
			}
			sum := sha256.Sum256(raw[0])
			if !equalPin(sum[:], want) {
				return errors.New("direct QUIC certificate SHA-256 mismatch")
			}
			cert, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			if err = cert.VerifyHostname(d.Hostname); err != nil {
				return err
			}
			now := time.Now()
			if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return errors.New("direct QUIC certificate expired or not yet valid")
			}
			return nil
		},
	}
}
func equalPin(got, want []byte) bool {
	if len(got) != len(want) {
		return false
	}
	var diff byte
	for i := range got {
		diff |= got[i] ^ want[i]
	}
	return diff == 0
}
func resolveAAAA(ctx context.Context, hostname string) ([]net.IP, error) {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip6", hostname)
	if err != nil {
		return nil, err
	}
	valid := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if ip.To16() != nil && ip.To4() == nil && ip.IsGlobalUnicast() {
			valid = append(valid, ip)
		}
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("DDNS %s has no globally routable AAAA records", hostname)
	}
	return valid, nil
}
func directDial(ctx context.Context, d DirectConfig) (quic.Connection, net.IP, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	ips, err := resolveAAAA(lookupCtx, d.Hostname)
	cancel()
	if err != nil {
		return nil, nil, err
	}
	var last error
	for _, ip := range ips {
		dialCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		conn, e := quic.DialAddr(dialCtx, net.JoinHostPort(ip.String(), strconv.Itoa(d.Port)),
			directTLS(d), &quic.Config{EnableDatagrams: true, KeepAlivePeriod: 20 * time.Second})
		stop()
		if e == nil {
			return conn, ip, nil
		}
		last = e
	}
	return nil, nil, fmt.Errorf("direct QUIC dial failed for DDNS %s: %w", d.Hostname, last)
}

// RunDirect is independent of the legacy Manager and its FRPC child.
func RunDirect(ctx context.Context, d DirectConfig, onState func(string, string)) error {
	d.Agent.DirectQUIC = true
	if err := d.Validate(); err != nil {
		return err
	}
	state := func(s, msg string) {
		if onState != nil {
			onState(s, msg)
		}
	}
	retry := d.Agent.RetryInterval
	for ctx.Err() == nil {
		state("connecting", "")
		q, ip, err := directDial(ctx, d)
		if err == nil {
			state("connected", ip.String())
			t := &directQUICTransport{conn: q}
			control, e := t.OpenStream(ctx)
			if e == nil {
				// Re-resolve at intervals; a prefix update forces reconnect.
				sessionCtx, stop := context.WithCancel(ctx)
				go func() {
					ticker := time.NewTicker(60 * time.Second)
					defer ticker.Stop()
					for {
						select {
						case <-sessionCtx.Done():
							return
						case <-ticker.C:
							c, cancel := context.WithTimeout(sessionCtx, 8*time.Second)
							ips, e := resolveAAAA(c, d.Hostname)
							cancel()
							if e != nil {
								continue
							} // keep working connection during DDNS outage
							found := false
							for _, v := range ips {
								if v.Equal(ip) {
									found = true
									break
								}
							}
							if !found {
								_ = q.CloseWithError(0, "DDNS AAAA changed")
								return
							}
						}
					}
				}()
				err = serveRSX3Transport(sessionCtx, control, d.Agent, state, t)
				stop()
			} else {
				err = e
			}
			_ = q.CloseWithError(0, "reconnect")
		}
		if ctx.Err() != nil {
			break
		}
		state("error", err.Error())
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
	return ctx.Err()
}
