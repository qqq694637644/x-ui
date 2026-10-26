#!/usr/bin/env python3
"""Actual Xray 26.3.27 smoke: domestic Portal, VPS Bridge and optional local SOCKS exit.

Builds both sides using their production generators:
- reverse_node -> domestic reverse.portals + Hysteria2 listener + local SOCKS5
- x-ui cmd/reverse-quic-fixture -> overseas reverse.bridges + QUIC Brutal outbound
- third Xray -> a VPS-local SOCKS5 listener forwarding to freedom

Checks TCP and UDP and deliberately stops the true SOCKS5 exit to ensure no
silent fall-through to VPS direct.
"""
import argparse
import http.server
import json
import os
import signal
import socket
import struct
import subprocess
import tempfile
import threading
import time
from pathlib import Path


def free_port(family, socktype, address):
    with socket.socket(family, socktype) as s:
        s.bind((address, 0))
        return s.getsockname()[1]


def recv_exact(sock, length):
    data = b""
    while len(data) < length:
        part = sock.recv(length - len(data))
        if not part:
            raise ConnectionError("SOCKS5 connection closed")
        data += part
    return data


def socks_connect(address, port):
    sock = socket.create_connection(address, timeout=9)
    sock.settimeout(9)
    sock.sendall(b"\x05\x01\x00")
    assert recv_exact(sock, 2) == b"\x05\x00"
    sock.sendall(b"\x05\x01\x00\x01" + socket.inet_aton("127.0.0.1") + struct.pack("!H", port))
    reply = recv_exact(sock, 4)
    if reply[1] != 0:
        sock.close()
        raise ConnectionError("SOCKS TCP CONNECT denied: " + repr(reply))
    read_bound(sock, reply[3])
    return sock


def read_bound(sock, atyp):
    if atyp == 1:
        host = socket.inet_ntoa(recv_exact(sock, 4))
    elif atyp == 4:
        host = socket.inet_ntop(socket.AF_INET6, recv_exact(sock, 16))
    elif atyp == 3:
        host = recv_exact(sock, recv_exact(sock, 1)[0]).decode()
    else:
        raise RuntimeError("unexpected SOCKS address type")
    return host, struct.unpack("!H", recv_exact(sock, 2))[0]


def http_request_through_socks(port, target_port):
    with socks_connect(("127.0.0.1", port), target_port) as sock:
        sock.sendall(b"GET / HTTP/1.0\r\nHost: localhost\r\n\r\n")
        data = b""
        while True:
            try:
                part = sock.recv(8192)
            except (ConnectionResetError, ConnectionAbortedError):
                break
            if not part:
                break
            data += part
        return data


def udp_request_through_socks(socks_port, target_port):
    with socket.create_connection(("127.0.0.1", socks_port), timeout=9) as control:
        control.settimeout(9)
        control.sendall(b"\x05\x01\x00")
        assert recv_exact(control, 2) == b"\x05\x00"
        control.sendall(b"\x05\x03\x00\x01" + socket.inet_aton("127.0.0.1") + b"\x00\x00")
        reply = recv_exact(control, 4)
        if reply[1] != 0:
            raise ConnectionError("SOCKS UDP ASSOCIATE rejected")
        relay_host, relay_port = read_bound(control, reply[3])
        if relay_host in ("0.0.0.0", "::"):
            relay_host = "127.0.0.1"
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as u:
            u.settimeout(10)
            u.bind(("127.0.0.1", 0))
            payload = b"REVERSE-QUIC-BRIDGE-UDP"
            packet = b"\x00\x00\x00\x01" + socket.inet_aton("127.0.0.1") + struct.pack("!H", target_port) + payload
            u.sendto(packet, (relay_host, relay_port))
            response, _ = u.recvfrom(4096)
            assert b"REVERSE-QUIC-BRIDGE-UDP-PASS:" + payload in response, repr(response)


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"REVERSE-QUIC-BRIDGE-TCP-PASS"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


def check_config(binary, file):
    p = subprocess.run([binary, "run", "-test", "-c", str(file)], text=True, capture_output=True, timeout=30)
    if p.returncode:
        raise RuntimeError("Xray rejected %s: %s %s" % (file, p.stdout, p.stderr))


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--xray", required=True, type=Path)
    p.add_argument("--reverse-node", required=True, type=Path)
    p.add_argument("--fixture", required=True, type=Path)
    args = p.parse_args()
    xray, reverse_node, fixture = (str(p.resolve()) for p in (args.xray, args.reverse_node, args.fixture))
    version = subprocess.check_output([xray, "version"], text=True)
    assert "Xray 26.3.27" in version and "d2758a0" in version, version

    with tempfile.TemporaryDirectory(prefix="xui-reverse-quic-") as tmp:
        root = Path(tmp)
        state = root / "domestic"
        public_port = free_port(socket.AF_INET6, socket.SOCK_DGRAM, "::1")
        socks_port = free_port(socket.AF_INET, socket.SOCK_STREAM, "127.0.0.1")
        exit_port = free_port(socket.AF_INET, socket.SOCK_STREAM, "127.0.0.1")
        init = subprocess.run([
            reverse_node, "init", "--xray-path", xray, "--public-host", "::1",
            "--listen", "::1", "--port", str(public_port),
            "--socks-port", str(socks_port), "--socks-listen", "127.0.0.1",
            "--state-dir", str(state), "--brutal-up", "16mbps",
            "--brutal-down", "24mbps", "--log-level", "debug"
        ], text=True, capture_output=True, timeout=30)
        if init.returncode:
            raise RuntimeError("domestic init failed:\n" + init.stdout + init.stderr)
        domestic_fragment = json.loads((state / "overseas-bridge.fragment.json").read_text())
        original = domestic_fragment["outbounds"][0]
        stream = original["streamSettings"]
        profile = {
            "enabled": True,
            "domain": domestic_fragment["reverse"]["bridges"][0]["domain"],
            "publicHost": original["settings"]["address"],
            "publicPort": original["settings"]["port"],
            "auth": stream["hysteriaSettings"]["auth"],
            "serverName": stream["tlsSettings"]["serverName"],
            "certificatePin": stream["tlsSettings"]["pinnedPeerCertSha256"],
            "brutalUp": stream["finalmask"]["quicParams"]["brutalUp"],
            "brutalDown": stream["finalmask"]["quicParams"]["brutalDown"],
            "egressMode": "socks5",
            "socks5Address": "127.0.0.1:" + str(exit_port)
        }
        (root / "bridge-profile.json").write_text(json.dumps(profile))
        bridge_path = root / "xui-bridge.json"
        result = subprocess.run([
            fixture, "--profile", str(root / "bridge-profile.json"),
            "--output", str(bridge_path)
        ], text=True, capture_output=True, timeout=30)
        if result.returncode:
            raise RuntimeError("x-ui Bridge production generator failed:\n" + result.stderr)
        bridge = json.loads(bridge_path.read_text())
        assert "bridges" in bridge["reverse"] and "portals" not in bridge["reverse"]
        assert next(x for x in bridge["outbounds"] if x["tag"] == "reverse-node-egress")["protocol"] == "socks"
        assert next(x for x in bridge["outbounds"] if x["tag"] == "reverse-node-hysteria")["streamSettings"]["finalmask"]["quicParams"]["congestion"] == "force-brutal"
        check_config(xray, state / "xray-portal.json")
        check_config(xray, bridge_path)
        proxy_config = {
            "log": {"loglevel": "warning"},
            "inbounds": [
                {"tag": "local-vps-socks", "listen": "127.0.0.1", "port": exit_port,
                 "protocol": "socks", "settings": {"auth": "noauth", "udp": True, "ip": "127.0.0.1"}}
            ],
            "outbounds": [{"tag": "freedom", "protocol": "freedom", "settings": {}}],
        }
        proxy_path = root / "vps-local-socks.json"
        proxy_path.write_text(json.dumps(proxy_config))
        check_config(xray, proxy_path)
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        udp.bind(("127.0.0.1", 0))
        udp.settimeout(0.2)
        stop_udp = threading.Event()

        def echo_udp():
            while not stop_udp.is_set():
                try:
                    data, addr = udp.recvfrom(4096)
                    udp.sendto(b"REVERSE-QUIC-BRIDGE-UDP-PASS:" + data, addr)
                except socket.timeout:
                    continue
                except OSError:
                    break

        threading.Thread(target=echo_udp, daemon=True).start()
        processes = []
        passed = False
        try:
            def start(name, args):
                log = (root / (name + ".log")).open("wb")
                proc = subprocess.Popen(args, stdout=log, stderr=subprocess.STDOUT)
                processes.append((name, proc, log))
                return proc

            proxy = start("vps-socks", [xray, "run", "-c", str(proxy_path)])
            time.sleep(.3)
            start("domestic-portal", [
                reverse_node, "run", "--xray-path", xray, "--state-dir", str(state),
                "--restart=false"
            ])
            time.sleep(.3)
            start("overseas-bridge", [xray, "run", "-c", str(bridge_path)])
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                for name, proc, _ in processes:
                    if proc.poll() is not None:
                        raise RuntimeError(name + " exited early")
                portal_text = (root / "domestic-portal.log").read_text(errors="replace").lower()
                bridge_text = (root / "overseas-bridge.log").read_text(errors="replace").lower()
                if "congestion brutal bytes per second" in portal_text and "congestion brutal bytes per second" in bridge_text:
                    break
                time.sleep(.25)
            else:
                raise RuntimeError("reverse Bridge did not establish Hysteria2/QUIC Brutal connection")
            got = http_request_through_socks(socks_port, server.server_address[1])
            assert b"200 OK" in got and b"REVERSE-QUIC-BRIDGE-TCP-PASS" in got, repr(got[-400:])
            print("PASS: domestic SOCKS5 -> reverse Portal -> QUIC Brutal -> overseas Bridge -> VPS-local SOCKS5 TCP")
            udp_request_through_socks(socks_port, udp.getsockname()[1])
            print("PASS: SOCKS5 UDP ASSOCIATE uses VPS-local SOCKS5 through the reverse Bridge")
            print("PASS: both Xray peers negotiated actual QUIC Brutal")
            # Deliberately break the selected SOCKS5 exit; any successful web
            # response proves a dangerous silent switch to VPS freedom.
            proxy.terminate()
            proxy.wait(timeout=5)
            time.sleep(.25)
            try:
                after = http_request_through_socks(socks_port, server.server_address[1])
            except (OSError, ConnectionError, TimeoutError):
                after = b""
            if b"REVERSE-QUIC-BRIDGE-TCP-PASS" in after:
                raise AssertionError("security regression: SOCKS5 failure fell back to direct")
            print("PASS: VPS-local SOCKS5 offline -> reverse business FAILS CLOSED (no direct fallback)")
            passed = True
        finally:
            stop_udp.set()
            udp.close()
            server.shutdown()
            for name, proc, _ in processes:
                if proc.poll() is None:
                    if name == "domestic-portal":
                        proc.send_signal(signal.SIGINT)
                    else:
                        proc.terminate()
            for name, proc, handle in processes:
                try:
                    proc.wait(timeout=6)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=5)
                handle.close()
                if not passed:
                    text = (root / (name + ".log")).read_text(errors="replace")
                    text = text.replace(profile["auth"], "[REDACTED]")
                    print(name + " log tail:\n" + text[-2500:])


if __name__ == "__main__":
    main()
