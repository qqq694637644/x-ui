package reverseexit

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

func TestRSX3ControlFrameRoundTrip(t *testing.T) {
	input := []frame{
		{openTCP, 7, []byte("destination")},
		{openUDP, 100, nil},
		{udpResult, 300, bytes.Repeat([]byte{0xff}, maxPayload)},
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

func TestRSX3RejectOversizedControlFrame(t *testing.T) {
	var header [9]byte
	binary.BigEndian.PutUint32(header[5:], maxPayload+1)
	if _, err := readFrame(bytes.NewReader(header[:])); err == nil {
		t.Fatal("accepted oversized frame")
	}
	if err := writeFrame(io.Discard, frame{openTCP, 1, make([]byte, maxPayload+1)}); err == nil {
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
		pkt := append(append([]byte{0, 0, 0}, raw...), []byte("dns")...)
		gotHost, gotPort, body, err := decodeUDP(pkt)
		if err != nil || gotHost != host || gotPort != port || string(body) != "dns" {
			t.Fatalf("UDP: %s %d %s %v", gotHost, gotPort, body, err)
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
