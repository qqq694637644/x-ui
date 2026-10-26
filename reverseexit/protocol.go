package reverseexit

// RSX3 authenticated control framing; TCP payloads use separate QUIC streams.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
)

const (
	maxPayload = 65535
	tcpChunk   = 16384

	openTCP   byte = 1
	tcpResult byte = 2
	tcpRST    byte = 5
	openUDP   byte = 6
	udpResult byte = 7
	udpClose  byte = 8
	ping      byte = 9
	pong      byte = 10
)

const udpLinkMax = 1100 // including the 4-byte session ID

type frame struct {
	kind byte
	id   uint32
	data []byte
}

func readFrame(r io.Reader) (frame, error) {
	var header [9]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return frame{}, err
	}
	size := binary.BigEndian.Uint32(header[5:])
	if size > maxPayload {
		return frame{}, errors.New("RSX3 oversized control frame")
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r, data); err != nil {
		return frame{}, err
	}
	return frame{header[0], binary.BigEndian.Uint32(header[1:]), data}, nil
}

func writeFrame(w io.Writer, f frame) error {
	if len(f.data) > maxPayload {
		return errors.New("RSX3 oversized control payload")
	}
	var header [9]byte
	header[0] = f.kind
	binary.BigEndian.PutUint32(header[1:], f.id)
	binary.BigEndian.PutUint32(header[5:], uint32(len(f.data)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, f.data)
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func decodeAddress(b []byte) (string, int, int, error) {
	if len(b) < 1 {
		return "", 0, 0, errors.New("empty address")
	}
	var host string
	var offset int
	switch b[0] {
	case 1:
		if len(b) < 7 {
			return "", 0, 0, errors.New("short IPv4 address")
		}
		host = net.IP(b[1:5]).String()
		offset = 5
	case 4:
		if len(b) < 19 {
			return "", 0, 0, errors.New("short IPv6 address")
		}
		host = net.IP(b[1:17]).String()
		offset = 17
	case 3:
		if len(b) < 2 || b[1] == 0 || len(b) < 2+int(b[1])+2 {
			return "", 0, 0, errors.New("short domain address")
		}
		host = string(b[2 : 2+int(b[1])])
		offset = 2 + int(b[1])
	default:
		return "", 0, 0, fmt.Errorf("invalid address type %d", b[0])
	}
	port := int(binary.BigEndian.Uint16(b[offset:]))
	return host, port, offset + 2, nil
}

func encodeAddress(ip net.IP, port int) ([]byte, error) {
	if port < 0 || port > 65535 {
		return nil, errors.New("invalid port")
	}
	var b []byte
	if v4 := ip.To4(); v4 != nil {
		b = append([]byte{1}, v4...)
	} else if v6 := ip.To16(); v6 != nil {
		b = append([]byte{4}, v6...)
	} else {
		return nil, errors.New("invalid IP")
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	return append(b, p[:]...), nil
}

func decodeUDP(b []byte) (string, int, []byte, error) {
	if len(b) < 4 || !bytes.Equal(b[:3], []byte{0, 0, 0}) {
		return "", 0, nil, errors.New("invalid/fragmented SOCKS5 UDP header")
	}
	host, port, offset, err := decodeAddress(b[3:])
	if err != nil {
		return "", 0, nil, err
	}
	return host, port, b[3+offset:], nil
}
