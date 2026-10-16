package reverseexit

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	input := []frame{
		{tcpData, 7, []byte("hello")},
		{openUDP, 100, nil},
		{udpData, 300, bytes.Repeat([]byte{0xff}, maxPayload)},
	}
	var buf bytes.Buffer
	for _, f := range input {
		if err := writeFrame(&buf, f); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range input {
		got, err := readFrame(&buf)
		if err != nil || got.kind != want.kind || got.id != want.id || !bytes.Equal(got.data, want.data) {
			t.Fatalf("frame got=%v err=%v want=%v", got, err, want)
		}
	}
	if _, err := readFrame(&buf); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestRejectOversizedFrame(t *testing.T) {
	var header [9]byte
	binary.BigEndian.PutUint32(header[5:], maxPayload+1)
	if _, err := readFrame(bytes.NewReader(header[:])); err == nil {
		t.Fatal("accepted oversized frame")
	}
	if err := writeFrame(io.Discard, frame{tcpData, 1, make([]byte, maxPayload+1)}); err == nil {
		t.Fatal("accepted oversized payload")
	}
}

func TestAddressesAndUDP(t *testing.T) {
	for _, ip := range []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("2001:db8::1")} {
		raw, err := encodeAddress(ip, 5353)
		if err != nil {
			t.Fatal(err)
		}
		host, port, used, err := decodeAddress(raw)
		if err != nil || !net.ParseIP(host).Equal(ip) || port != 5353 || used != len(raw) {
			t.Fatalf("addr %s: got %s:%d used=%d err=%v", ip, host, port, used, err)
		}
		pkt, err := encodeUDP(&net.UDPAddr{IP: ip, Port: 5353}, []byte("dns"))
		if err != nil {
			t.Fatal(err)
		}
		decodedHost, decodedPort, body, err := decodeUDP(pkt)
		if err != nil || decodedHost != host || decodedPort != port || string(body) != "dns" {
			t.Fatalf("UDP: %s %d %s %v", decodedHost, decodedPort, body, err)
		}
	}
	raw := append([]byte{3, 11}, []byte("example.com")...)
	raw = append(raw, 0, 53)
	host, port, used, err := decodeAddress(raw)
	if err != nil || host != "example.com" || port != 53 || used != len(raw) {
		t.Fatalf("domain decode failed: %q:%d err=%v", host, port, err)
	}
	if _, _, _, err := decodeUDP([]byte{0, 0, 1, 1}); err == nil {
		t.Fatal("accepted fragmented UDP")
	}
}

func TestChallengeAuthentication(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	outcome := make(chan error, 1)
	go func() {
		challenge := append([]byte("RSX1"), bytes.Repeat([]byte{0x42}, 32)...)
		if _, err := b.Write(challenge); err != nil {
			outcome <- err
			return
		}
		mac := make([]byte, 32)
		_, err := io.ReadFull(b, mac)
		expected := hmac.New(sha256.New, []byte("this-token-is-long-enough"))
		_, _ = expected.Write(challenge[4:])
		if err == nil && !hmac.Equal(expected.Sum(nil), mac) {
			err = io.ErrUnexpectedEOF
		}
		outcome <- err
	}()
	if err := authenticate(a, []byte("this-token-is-long-enough")); err != nil {
		t.Fatal(err)
	}
	if err := <-outcome; err != nil {
		t.Fatal(err)
	}
}
