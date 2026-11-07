#!/usr/bin/env python3
"""Loopback-only VLESS/XHTTP end-to-end smoke test; Python standard library."""

import argparse
import contextlib
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import re
import socket
import socketserver
import struct
import subprocess
import tempfile
import threading
import time
import uuid


BLOCK = bytes(range(256)) * 256


def receive_exact(sock, count):
    result = bytearray()
    while len(result) < count:
        data = sock.recv(count - len(result))
        if not data:
            raise RuntimeError("unexpected EOF during SOCKS handshake")
        result.extend(data)
    return bytes(result)


def unused_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_listener(port, process):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"Xray exited during startup: code {process.returncode}")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                return
        except OSError:
            time.sleep(0.02)
    raise RuntimeError(f"loopback listener {port} did not start")


class LoopbackProxy(socketserver.ThreadingTCPServer):
    allow_reuse_address = False
    daemon_threads = True
    block_on_close = False

    def __init__(self, destination_port):
        super().__init__(("127.0.0.1", 0), ProxyHandler)
        self.destination_port = destination_port
        self.lock = threading.Lock()
        self.counters = {key: 0 for key in (
            "tcp_connections", "cuts", "client_to_server_bytes",
            "server_to_client_bytes", "get_header_uploads", "bodyless_get_uploads",
        )}

    def add(self, name, value=1):
        with self.lock:
            self.counters[name] += value

    def snapshot(self):
        with self.lock:
            return dict(self.counters)


class ProxyHandler(socketserver.BaseRequestHandler):
    def handle(self):
        self.server.add("tcp_connections")
        try:
            upstream = socket.create_connection(("127.0.0.1", self.server.destination_port), timeout=5)
        except OSError:
            return
        upstream.settimeout(5)
        downstream = self.request
        downstream.settimeout(5)
        stopped = threading.Event()

        def upload():
            header = bytearray()
            checked = False
            header_size = 0
            total = 0
            bodyless_candidate = False
            try:
                while not stopped.is_set():
                    data = downstream.recv(65536)
                    if not data:
                        break
                    total += len(data)
                    upstream.sendall(data)
                    self.server.add("client_to_server_bytes", len(data))
                    if not checked:
                        header.extend(data)
                        if b"\r\n\r\n" in header:
                            checked = True
                            header_size = header.index(b"\r\n\r\n") + 4
                            text = bytes(header).split(b"\r\n\r\n", 1)[0].lower()
                            if text.startswith(b"get ") and b"\r\ndata-0:" in text:
                                self.server.add("get_header_uploads")
                                lines = text.split(b"\r\n")[1:]
                                fields = dict(line.split(b":", 1) for line in lines if b":" in line)
                                if fields.get(b"content-length", b"0").strip() == b"0" and b"transfer-encoding" not in fields:
                                    bodyless_candidate = True
                            header.clear()
                        elif len(header) > 1 << 20:
                            raise RuntimeError("unexpectedly large HTTP header")
            except (OSError, RuntimeError):
                pass
            finally:
                if bodyless_candidate and total == header_size:
                    self.server.add("bodyless_get_uploads")
                with contextlib.suppress(OSError):
                    upstream.shutdown(socket.SHUT_WR)

        worker = threading.Thread(target=upload, daemon=True)
        worker.start()
        delivered = 0
        try:
            while delivered < 16384:
                data = upstream.recv(min(65536, 16384 - delivered))
                if not data:
                    break
                downstream.sendall(data)
                delivered += len(data)
                self.server.add("server_to_client_bytes", len(data))
            if delivered == 16384:
                self.server.add("cuts")
                # FIN the client-facing response, then release this TCP flow.
                with contextlib.suppress(OSError):
                    downstream.shutdown(socket.SHUT_WR)
        except OSError:
            pass
        finally:
            stopped.set()
            for sock in (upstream, downstream):
                with contextlib.suppress(OSError):
                    sock.shutdown(socket.SHUT_RDWR)
                sock.close()
            worker.join(timeout=1)


def configurations(server_port, proxy_port, socks_port, origin_port, identity):
    rx = {
        "enabled": True, "stallDetect": True, "maxReplayBytes": 262144,
        "maxSessions": 8, "probeIntervalMs": 20, "stallTimeoutMs": 80,
        "requestTimeoutMs": 500, "sessionTtlSecs": 30,
        "maxReconnects": 16000, "retryDelayMs": 1,
    }
    stream = {
        "network": "xhttp", "security": "none",
        "xhttpSettings": {
            "path": "/rx-smoke", "host": "localhost", "mode": "packet-up",
            "extra": {
                "uplinkHTTPMethod": "GET", "uplinkDataPlacement": "header",
                "uplinkDataKey": "data", "scMaxEachPostBytes": 4096, "rx": rx,
            },
        },
    }
    server = {
        "log": {"loglevel": "info"},
        "inbounds": [{
            "listen": "127.0.0.1", "port": server_port, "protocol": "vless",
            "settings": {"clients": [{"id": identity}], "decryption": "none"},
            "streamSettings": stream,
        }],
        # Current upstream blocks private destinations for VLESS by default.
        # Permit only this test's origin; retain a catch-all deny rule.
        "outbounds": [{"protocol": "freedom", "settings": {"finalRules": [
            {"action": "allow", "network": "tcp", "ip": ["127.0.0.1/32"], "port": str(origin_port)},
            {"action": "block", "blockDelay": 0},
        ]}}],
    }
    client = {
        "log": {"loglevel": "info"},
        "inbounds": [{
            "listen": "127.0.0.1", "port": socks_port, "protocol": "socks",
            "settings": {"auth": "noauth", "udp": False},
        }],
        "outbounds": [{
            "protocol": "vless",
            "settings": {"vnext": [{
                "address": "127.0.0.1", "port": proxy_port,
                "users": [{"id": identity, "encryption": "none"}],
            }]},
            "streamSettings": stream,
        }],
    }
    return server, client


def socks_download(socks_port, origin_port, size):
    digest = hashlib.sha256()
    downloaded = 0
    with socket.create_connection(("127.0.0.1", socks_port), timeout=10) as sock:
        sock.settimeout(60)
        sock.sendall(b"\x05\x01\x00")
        if receive_exact(sock, 2) != b"\x05\x00":
            raise RuntimeError("SOCKS authentication negotiation failed")
        sock.sendall(b"\x05\x01\x00\x01\x7f\x00\x00\x01" + struct.pack("!H", origin_port))
        reply = receive_exact(sock, 4)
        if reply[:2] != b"\x05\x00":
            raise RuntimeError(f"SOCKS connect failed: {reply.hex()}")
        if reply[3] == 1:
            receive_exact(sock, 4)
        elif reply[3] == 4:
            receive_exact(sock, 16)
        elif reply[3] == 3:
            receive_exact(sock, receive_exact(sock, 1)[0])
        else:
            raise RuntimeError("invalid SOCKS address type")
        receive_exact(sock, 2)
        sock.sendall(f"GET /payload HTTP/1.1\r\nHost: 127.0.0.1:{origin_port}\r\nConnection: close\r\n\r\n".encode("ascii"))
        response = http.client.HTTPResponse(sock)
        response.begin()
        if response.status != 200 or response.getheader("Content-Length") != str(size):
            raise RuntimeError(f"unexpected origin response: {response.status}")
        while True:
            chunk = response.read(65536)
            if not chunk:
                break
            downloaded += len(chunk)
            digest.update(chunk)
    return downloaded, digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--size-mib", type=int, default=1)
    parser.add_argument("--artifacts", type=Path)
    args = parser.parse_args()
    if not args.binary.is_absolute() or not args.binary.is_file():
        parser.error("--binary must be an existing absolute path")
    if not 1 <= args.size_mib <= 50:
        parser.error("--size-mib must be in [1,50]")
    output = args.artifacts or Path(__file__).resolve().parents[2] / "artifacts" / time.strftime("rx-smoke-%Y%m%d-%H%M%S")
    output = output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    size = args.size_mib * 1024 * 1024
    expected = hashlib.sha256()
    for _ in range(size // len(BLOCK)):
        expected.update(BLOCK)

    class Origin(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_GET(self):
            if self.path != "/payload":
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header("Content-Length", str(size))
            self.send_header("Content-Type", "application/octet-stream")
            self.end_headers()
            try:
                for _ in range(size // len(BLOCK)):
                    self.wfile.write(BLOCK)
            except OSError:
                pass

        def log_message(self, *_):
            pass

    processes = []
    logs = []
    services = []
    result = {"test": "loopback-vless-xhttp-get-header-fin-16k", "size_mib": args.size_mib,
              "fault_threshold_bytes": 16384, "fault_counts_http_headers": True,
              "expected_sha256": expected.hexdigest(), "artifacts": str(output), "passed": False}
    result["binary_sha256"] = hashlib.sha256(args.binary.read_bytes()).hexdigest()
    started = time.monotonic()
    try:
        origin = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Origin)
        origin.daemon_threads = True
        proxy = LoopbackProxy(unused_port())
        socks_port = unused_port()
        for service in (origin, proxy):
            threading.Thread(target=service.serve_forever, daemon=True).start()
            services.append(service)
        server, client = configurations(proxy.destination_port, proxy.server_address[1], socks_port, origin.server_port, str(uuid.uuid4()))
        with tempfile.TemporaryDirectory(prefix="xray-rx-smoke-") as temp:
            for name, config, listen_port in (("server", server, proxy.destination_port), ("client", client, socks_port)):
                path = Path(temp) / f"{name}.json"
                path.write_text(json.dumps(config), encoding="utf-8")
                log = (output / f"{name}.log").open("wb")
                logs.append(log)
                process = subprocess.Popen(
                    [str(args.binary), "run", "-config", str(path)], stdout=log, stderr=subprocess.STDOUT,
                    stdin=subprocess.DEVNULL, creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0,
                )
                processes.append(process)
                wait_listener(listen_port, process)
            transfer_started = time.monotonic()
            count, actual_hash = socks_download(socks_port, origin.server_port, size)
            result.update(bytes=count, actual_sha256=actual_hash, transfer_seconds=time.monotonic() - transfer_started)
            result.update(proxy.snapshot())
            if count != size or actual_hash != expected.hexdigest():
                raise RuntimeError("download byte count or SHA-256 differs")
            if result["cuts"] < 1:
                raise RuntimeError("fault injection did not cut any connection")
            if result["get_header_uploads"] < 1 or result["get_header_uploads"] != result["bodyless_get_uploads"]:
                raise RuntimeError("GET Header upload without a body was not observed")
            result["passed"] = True
    except Exception as exc:
        result["error"] = f"{type(exc).__name__}: {exc}"
        if "proxy" in locals():
            result.update(proxy.snapshot())
    finally:
        # Only process handles created above are terminated. No name matching.
        for process in reversed(processes):
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
        for service in reversed(services):
            service.shutdown()
            service.server_close()
        for log in logs:
            log.close()
    result["elapsed_seconds"] = time.monotonic() - started
    if result.get("bytes"):
        result["tcp_connections_per_mib"] = result["tcp_connections"] / (result["bytes"] / 1048576)
        result["throughput_mib_s"] = result["bytes"] / 1048576 / result["transfer_seconds"]
        result["extra_socket_bytes"] = result["client_to_server_bytes"] + result["server_to_client_bytes"] - result["bytes"]
    client_log = output / "client.log"
    if client_log.exists():
        durations = sorted(int(value) for value in re.findall(r"recovery_ms (\d+)", client_log.read_text(encoding="utf-8", errors="replace")))
        result["recoveries_with_first_byte"] = len(durations)
        if durations:
            result["recovery_p50_ms"] = durations[(len(durations) - 1) // 2]
            result["recovery_p95_ms"] = durations[min(len(durations) - 1, (len(durations) * 95 + 99) // 100 - 1)]
            result["recovery_time_definition"] = "client observed failure/cancel to first resumed bytes; excludes prior stall detection wait; integer-ms logs"
    (output / "result.json").write_text(json.dumps(result, indent=2), encoding="utf-8")
    print(json.dumps(result, indent=2))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
