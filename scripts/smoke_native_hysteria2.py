#!/usr/bin/env python3
"""Xray-core 26.3.27 native Hysteria2/Brutal loopback smoke test.

No custom tunnel protocol, Python QUIC package, FRP, STUN or WARP.
Requires the pinned Xray executable and the system's openssl CLI.
Runs two Xray processes on 127.0.0.1 and verifies SOCKS5 TCP/UDP plus
actual Brutal activation in both process logs.
"""
import argparse
import hashlib
import http.server
import json
import secrets
import socket
import ssl
import struct
import subprocess
import tempfile
import threading
import time
from pathlib import Path


def available_port(kind):
    with socket.socket(socket.AF_INET, kind) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def take_exact(sock, length):
    result = bytearray()
    while len(result) < length:
        chunk = sock.recv(length - len(result))
        if not chunk:
            raise RuntimeError("SOCKS connection closed prematurely")
        result.extend(chunk)
    return bytes(result)


class HTTPHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"NATIVE-XRAY-26.3.27-HYSTERIA2-TCP-PASS")

    def log_message(self, *_):
        pass


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--xray", required=True, type=Path)
    args = parser.parse_args()
    if not args.xray.is_file():
        parser.error(f"Xray executable not found: {args.xray}")

    version = subprocess.check_output([str(args.xray), "version"], text=True)
    if "Xray 26.3.27" not in version or "d2758a0" not in version:
        raise AssertionError("Smoke test requires pinned Xray-core 26.3.27 d2758a0")

    with tempfile.TemporaryDirectory(prefix="hy2-native-xray-") as tmp:
        base = Path(tmp)
        cert, key = base / "cert.pem", base / "key.pem"
        subprocess.run([
            "openssl", "req", "-x509", "-newkey", "ec",
            "-pkeyopt", "ec_paramgen_curve:prime256v1",
            "-nodes", "-days", "1",
            "-subj", "/CN=localhost",
            "-addext", "subjectAltName=DNS:localhost",
            "-keyout", str(key), "-out", str(cert),
        ], check=True, capture_output=True)
        cert_der = ssl.PEM_cert_to_DER_cert(cert.read_text(encoding="ascii"))
        pin = hashlib.sha256(cert_der).hexdigest()
        auth = secrets.token_hex(32)
        quic_port = available_port(socket.SOCK_DGRAM)
        socks_port = available_port(socket.SOCK_STREAM)

        server = {
            "log": {"loglevel": "debug"},
            "inbounds": [{
                "tag": "native-hy2",
                "listen": "127.0.0.1",
                "port": quic_port,
                "protocol": "hysteria",
                "settings": {"version": 2, "clients": [{"auth": auth}]},
                "streamSettings": {
                    "network": "hysteria",
                    "security": "tls",
                    "tlsSettings": {
                        "alpn": ["h3"],
                        "certificates": [{
                            "certificateFile": str(cert),
                            "keyFile": str(key),
                        }],
                    },
                    "hysteriaSettings": {"version": 2},
                    "finalmask": {"quicParams": {
                        "congestion": "force-brutal",
                        "brutalUp": "16mbps",
                        "brutalDown": "24mbps",
                    }},
                },
            }],
            "outbounds": [{"tag": "direct", "protocol": "freedom", "settings": {}}],
        }
        client = {
            "log": {"loglevel": "debug"},
            "inbounds": [{
                "tag": "local-socks",
                "listen": "127.0.0.1",
                "port": socks_port,
                "protocol": "socks",
                "settings": {"auth": "noauth", "udp": True, "ip": "127.0.0.1"},
            }],
            "outbounds": [{
                "tag": "to-server",
                "protocol": "hysteria",
                "settings": {"version": 2, "address": "127.0.0.1", "port": quic_port},
                "streamSettings": {
                    "network": "hysteria",
                    "security": "tls",
                    "tlsSettings": {
                        "serverName": "localhost",
                        "alpn": ["h3"],
                        "pinnedPeerCertSha256": pin,
                        "verifyPeerCertByName": "localhost",
                    },
                    "hysteriaSettings": {"version": 2, "auth": auth},
                    "finalmask": {"quicParams": {
                        "congestion": "force-brutal",
                        "brutalUp": "16mbps",
                        "brutalDown": "24mbps",
                    }},
                },
            }],
        }
        configs = []
        for name, data in (("server", server), ("client", client)):
            output = base / f"{name}.json"
            output.write_text(json.dumps(data), encoding="utf-8")
            subprocess.run(
                [str(args.xray), "run", "-test", "-c", str(output)],
                check=True, capture_output=True,
            )
            configs.append((name, output))

        web = http.server.ThreadingHTTPServer(("127.0.0.1", 0), HTTPHandler)
        web_thread = threading.Thread(target=web.serve_forever, daemon=True)
        web_thread.start()
        udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        udp.bind(("127.0.0.1", 0))
        udp.settimeout(0.2)
        udp_port = udp.getsockname()[1]
        stop = threading.Event()

        def echo():
            while not stop.is_set():
                try:
                    packet, address = udp.recvfrom(4096)
                    udp.sendto(b"NATIVE-XRAY-HY2-UDP-PASS:" + packet, address)
                except socket.timeout:
                    pass
                except OSError:
                    break

        threading.Thread(target=echo, daemon=True).start()
        processes = []
        success = False
        try:
            for name, config in configs:
                log_file = (base / f"{name}.log").open("wb")
                proc = subprocess.Popen(
                    [str(args.xray), "run", "-c", str(config)],
                    stdout=log_file, stderr=subprocess.STDOUT,
                )
                processes.append((name, proc, log_file))
            time.sleep(1)
            for _, proc, _ in processes:
                if proc.poll() is not None:
                    raise RuntimeError("Xray process exited unexpectedly")

            with socket.create_connection(("127.0.0.1", socks_port), timeout=8) as conn:
                conn.settimeout(10)
                conn.sendall(b"\x05\x01\x00")
                assert take_exact(conn, 2) == b"\x05\x00"
                conn.sendall(
                    b"\x05\x01\x00\x01" + socket.inet_aton("127.0.0.1") +
                    struct.pack("!H", web.server_address[1])
                )
                assert take_exact(conn, 2) == b"\x05\x00"
                # Remaining CONNECT response is 8 bytes, IPv4 address/port.
                take_exact(conn, 8)
                conn.sendall(b"GET / HTTP/1.0\r\nHost: localhost\r\n\r\n")
                received = bytearray()
                while packet := conn.recv(4096):
                    received.extend(packet)
                assert b"200 OK" in received
                assert b"NATIVE-XRAY-26.3.27-HYSTERIA2-TCP-PASS" in received

            with socket.create_connection(("127.0.0.1", socks_port), timeout=8) as ctrl:
                ctrl.settimeout(10)
                ctrl.sendall(b"\x05\x01\x00")
                assert take_exact(ctrl, 2) == b"\x05\x00"
                ctrl.sendall(b"\x05\x03\x00\x01" +
                             socket.inet_aton("127.0.0.1") + b"\x00\x00")
                assert take_exact(ctrl, 2) == b"\x05\x00"
                address_type = take_exact(ctrl, 2)
                assert address_type[1] == 1
                relay = socket.inet_ntoa(take_exact(ctrl, 4))
                relay_port = struct.unpack("!H", take_exact(ctrl, 2))[0]
                if relay == "0.0.0.0":
                    relay = "127.0.0.1"
                with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as packet_sock:
                    packet_sock.bind(("127.0.0.1", 0))
                    packet_sock.settimeout(10)
                    ping = b"QUIC-DATAGRAM-ECHO"
                    packet_sock.sendto(
                        b"\x00\x00\x00\x01" + socket.inet_aton("127.0.0.1") +
                        struct.pack("!H", udp_port) + ping,
                        (relay, relay_port),
                    )
                    pong, _ = packet_sock.recvfrom(4096)
                    assert b"NATIVE-XRAY-HY2-UDP-PASS:" + ping in pong

            # Ensure Xray installed real Brutal on both QUIC connections.
            time.sleep(0.15)
            success = True
        finally:
            stop.set()
            udp.close()
            web.shutdown()
            for _, proc, _ in processes:
                proc.terminate()
            for _, proc, handle in processes:
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=5)
                handle.close()

            for name, _, _ in processes:
                text = (base / f"{name}.log").read_text(errors="replace")
                if success and "congestion brutal bytes per second" not in text.lower():
                    success = False
                    print(f"{name}: missing actual Xray QUIC Brutal activation")
                if not success:
                    print(f"{name} log tail:", text.replace(auth, "[REDACTED]")[-3000:])

        if not success:
            raise AssertionError("Real Xray Hysteria2/Brutal TCP or UDP validation failed")
        print("Xray-core 26.3.27 d2758a0 native TCP, UDP, pinned TLS, dual Brutal: PASS")


if __name__ == "__main__":
    main()
