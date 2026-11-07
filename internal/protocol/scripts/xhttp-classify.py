#!/usr/bin/env python3
"""Bounded ordinary-XHTTP observations; default is offline preparation only."""

import argparse
import copy
import hashlib
import ipaddress
import json
import math
import os
from pathlib import Path
import signal
import socket
import socketserver
import subprocess
import tempfile
import threading
import time
import urllib.parse
import uuid


SIZE = 1 << 20
XHTTP_FIELDS = set("headers xPaddingBytes xPaddingObfsMode xPaddingKey xPaddingHeader "
                   "xPaddingPlacement xPaddingMethod uplinkHTTPMethod sessionIDPlacement "
                   "sessionIDKey sessionIDTable sessionIDLength seqPlacement seqKey "
                   "uplinkDataPlacement uplinkDataKey uplinkChunkSize noGRPCHeader noSSEHeader "
                   "scMaxEachPostBytes scMinPostsIntervalMs scMaxBufferedPosts "
                   "scStreamUpServerSecs serverMaxHeaderBytes".split())
TLS_FIELDS = set("serverName fingerprint alpn enableSessionResumption curvePreferences "
                 "minVersion maxVersion allowInsecure".split())


def require(condition, message):
    if not condition:
        raise ValueError(message)


def only_keys(obj, allowed, label):
    require(isinstance(obj, dict) and not obj.keys() - allowed,
            label + ": unsupported fields in experiment input")


def read_json(path):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "duplicate JSON key")
            result[key] = value
        return result
    return json.loads(Path(path).read_text(encoding="utf-8-sig"), object_pairs_hook=pairs,
                      parse_constant=lambda _: require(False, "non-finite JSON number"))


def number(obj, key, low, high, integer=False):
    value = obj.get(key)
    require(type(value) in ((int,) if integer else (int, float)) and
            math.isfinite(value) and low <= value <= high, "invalid budget: " + key)
    return value


def validate_spec(spec):
    only_keys(spec, set("host ips url sha256 request_timeout_s total_timeout_s max_requests "
                        "max_connections per_ip_connections fast_MB_s tcp_max_seg hello_split hello_split_bytes".split()), "spec")
    host = spec.get("host", "")
    require(isinstance(host, str) and 1 <= len(host) <= 253 and "." in host and
            all(label and len(label) <= 63 and label[0] != "-" and label[-1] != "-" and
                all(c in "abcdefghijklmnopqrstuvwxyz0123456789-" for c in label)
                for label in host.split(".")), "host must be an explicit ASCII DNS name")
    ips = spec.get("ips")
    require(isinstance(ips, list) and 1 <= len(ips) <= 64, "provide 1..64 explicit IPs")
    canonical = []
    for value in ips:
        require(isinstance(value, str) and "%" not in value, "literal IPs only")
        ip = ipaddress.ip_address(value)
        require(ip.is_global and not ip.is_multicast and not (ip.version == 6 and ip.ipv4_mapped),
                "only explicitly authorized public IP literals are supported")
        canonical.append(str(ip))
    require(len(set(canonical)) == len(ips), "duplicate IP")
    url = urllib.parse.urlsplit(spec.get("url", ""))
    require(url.scheme in ("http", "https") and url.hostname and not url.username and
            not url.password and not url.fragment and url.path.startswith("/") and
            not any(ord(c) < 33 for c in spec["url"]) and
            not any(c in spec["url"] for c in "{}[]"), "invalid fixed payload URL")
    require(url.port is None or 1 <= url.port <= 65535, "invalid payload port")
    sha = spec.get("sha256", "")
    require(isinstance(sha, str) and len(sha) == 64 and
            all(c in "0123456789abcdef" for c in sha), "expected SHA256 is required")
    number(spec, "request_timeout_s", 1, 120)
    number(spec, "total_timeout_s", 1, 900)
    number(spec, "max_requests", len(ips), 64, True)
    number(spec, "max_connections", 1, 512, True)
    number(spec, "per_ip_connections", 1, spec["max_connections"], True)
    number(spec, "fast_MB_s", 0.001, 10000)
    # Optional advertised MSS for the single gate egress socket; 0/absent keeps the OS default.
    spec.setdefault("tcp_max_seg", 0)
    number(spec, "tcp_max_seg", 0, 1460, True)
    require(spec["tcp_max_seg"] == 0 or spec["tcp_max_seg"] >= 536, "tcp_max_seg must be 0 or 536..1460")
    # Optional first-flight split: "tcp" separate segments, "record" separate TLS records.
    spec.setdefault("hello_split", "")
    spec.setdefault("hello_split_bytes", 64)
    require(spec["hello_split"] in ("", "tcp", "record"), "hello_split must be empty, tcp or record")
    number(spec, "hello_split_bytes", 8, 1024, True)
    require(spec["request_timeout_s"] <= spec["total_timeout_s"], "request exceeds window")
    return canonical


def client_config(template, host):
    only_keys(template, {"log", "inbounds", "outbounds"}, "template")
    require(isinstance(template.get("outbounds"), list) and len(template["outbounds"]) == 1,
            "one VLESS outbound is required")
    outbound = copy.deepcopy(template["outbounds"][0])
    only_keys(outbound, {"protocol", "settings", "streamSettings", "tag"}, "outbound")
    require(outbound.get("protocol") == "vless", "VLESS only")
    settings = outbound.get("settings")
    only_keys(settings, {"vnext"}, "VLESS settings")
    require(isinstance(settings.get("vnext"), list) and len(settings["vnext"]) == 1,
            "one vnext required")
    peer = settings["vnext"][0]
    only_keys(peer, {"address", "port", "users"}, "vnext")
    require(type(peer.get("port")) is int and 1 <= peer["port"] <= 65535, "invalid entry port")
    require(isinstance(peer.get("users"), list) and len(peer["users"]) == 1, "one user required")
    user = peer["users"][0]
    only_keys(user, {"id", "encryption", "flow", "level", "email"}, "VLESS user")
    uuid.UUID(user["id"])
    require(user.get("encryption") == "none" and not user.get("flow"),
            "plain VLESS identity without extra flow required")
    stream = outbound.get("streamSettings")
    only_keys(stream, {"network", "security", "tlsSettings", "xhttpSettings"}, "stream")
    require(stream.get("network") == "xhttp" and stream.get("security") == "tls",
            "ordinary XHTTP over verified TLS required")
    tls = stream.get("tlsSettings")
    only_keys(tls, TLS_FIELDS, "TLS")
    require(tls.get("serverName") == host and tls.get("allowInsecure", False) is False,
            "SNI must equal host; insecure TLS forbidden")
    require(tls.get("fingerprint") == "unsafe" and tls.get("alpn") in (["h2"], ["http/1.1"]),
            "native TLS requires fingerprint=unsafe and a single H1/H2 ALPN")
    xhttp = stream.get("xhttpSettings")
    only_keys(xhttp, XHTTP_FIELDS | {"host", "path", "mode", "extra", "rx"}, "XHTTP")
    require(xhttp.get("host") == host and xhttp.get("mode") == "packet-up" and
            isinstance(xhttp.get("path"), str) and xhttp["path"].startswith("/"),
            "explicit matching Host, path and packet-up required")
    # extra replaces all outer transport fields except host/path/mode, just as Xray does.
    effective = copy.deepcopy(xhttp.get("extra", xhttp))
    only_keys(effective, XHTTP_FIELDS | {"host", "path", "mode", "rx"}, "effective XHTTP")
    effective.pop("rx", None)
    effective.update({key: xhttp[key] for key in ("host", "path", "mode")})
    headers = effective.setdefault("headers", {})
    require(isinstance(headers, dict) and all(isinstance(k, str) and isinstance(v, str) and
            k.lower() != "host" and "\r" not in k + v and "\n" not in k + v for k, v in headers.items()),
            "invalid XHTTP headers")
    for key in list(headers):
        if key.lower() == "accept-encoding":
            del headers[key]
    headers["Accept-Encoding"] = "identity"
    stream["xhttpSettings"] = effective
    port = peer["port"]
    peer["address"], peer["port"] = "127.0.0.1", 1081
    return {"log": {"loglevel": "none"}, "inbounds": [{
        "listen": "127.0.0.1", "port": 1080, "protocol": "socks",
        "settings": {"auth": "password", "accounts": [{
            "user": uuid.uuid4().hex, "pass": uuid.uuid4().hex}], "udp": False}}], "outbounds": [outbound]}, port


def write_json(path, value):
    with path.open("x", encoding="utf-8") as target:
        json.dump(value, target, indent=2, ensure_ascii=True)
        target.write("\n")
    path.chmod(0o600)


class Budget:
    def __init__(self, limit, deadline):
        self.limit, self.deadline = limit, deadline
        self.attempts = 0
        self.lock = threading.Lock()
        self.exhausted = threading.Event()

    def take(self):
        with self.lock:
            if time.monotonic() >= self.deadline or self.attempts >= self.limit:
                self.exhausted.set()
                return False
            self.attempts += 1
            return True


class Gate(socketserver.ThreadingTCPServer):
    daemon_threads = True
    block_on_close = False

    def __init__(self, ip, port, limit, budget, deadline=None, mss=0, split=None):
        self.destination, self.mss, self.split = (ip, port), mss, split
        self.limit, self.budget = limit, budget
        self.deadline = min(budget.deadline, deadline if deadline is not None else budget.deadline)
        self.lock = threading.Lock()
        self.sockets = set()
        self.workers = set()
        self.closing = False
        self.stats = dict(connect_attempts=0, established=0, connect_failures=0,
                          rejected=0, socket_sent_bytes=0, socket_received_bytes=0, hello_pieces=0)
        self.exhausted = threading.Event()
        super().__init__(("127.0.0.1", 0), GateHandler)
        self.thread = threading.Thread(target=self.serve_forever, kwargs={"poll_interval": .05})
        self.thread.start()
        self.expiry = threading.Timer(max(0, self.deadline - time.monotonic()), self.seal)
        self.expiry.daemon = True
        self.expiry.start()

    def add_socket(self, sock):
        with self.lock:
            if self.closing:
                sock.close()
                return False
            self.sockets.add(sock)
            return True

    def seal(self):
        with self.lock:
            self.closing = True
            sockets = list(self.sockets)
        for sock in sockets:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            sock.close()

    def close(self):
        self.expiry.cancel()
        self.seal()
        self.expiry.join()
        self.shutdown()
        self.server_close()
        self.thread.join()
        with self.lock:
            workers = list(self.workers)
        for worker in workers:
            worker.join(6)
        with self.lock:
            return {**self.stats, "cleanup_workers_alive": len(self.workers),
                    "cleanup_sockets_open": len(self.sockets)}


class GateHandler(socketserver.BaseRequestHandler):
    def handle(self):
        gate = self.server
        worker = threading.current_thread()
        upstream = None
        with gate.lock:
            if gate.closing:
                return
            gate.workers.add(worker)
        gate.add_socket(self.request)
        try:
            with gate.lock:
                if (gate.closing or time.monotonic() >= gate.deadline or
                        gate.stats["connect_attempts"] >= gate.limit or not gate.budget.take()):
                    gate.stats["rejected"] += 1
                    gate.exhausted.set()
                    return
                gate.stats["connect_attempts"] += 1
            ip, port = gate.destination
            upstream = socket.socket(socket.AF_INET6 if ":" in ip else socket.AF_INET, socket.SOCK_STREAM)
            if not gate.add_socket(upstream):
                return
            remaining = gate.deadline - time.monotonic()
            if remaining <= 0:
                gate.exhausted.set()
                return
            upstream.settimeout(min(3, remaining))
            try:
                if gate.mss:
                    upstream.setsockopt(socket.IPPROTO_TCP, socket.TCP_MAXSEG, gate.mss)
                if gate.closing or time.monotonic() >= gate.deadline:
                    gate.exhausted.set()
                    return
                upstream.connect((ip, port))  # Exactly one connect call; no DNS/fallback/retry.
                require(ipaddress.ip_address(upstream.getpeername()[0]) == ipaddress.ip_address(ip),
                        "unexpected gate peer")
            except (OSError, ValueError):
                with gate.lock:
                    gate.stats["connect_failures"] += 1
                return
            with gate.lock:
                gate.stats["established"] += 1
            upstream.settimeout(.2)
            self.request.settimeout(.2)
            stopped = threading.Event()
            first_sent = threading.Event()

            def pump(source, target, counter):
                try:
                    while not stopped.is_set() and time.monotonic() < gate.deadline:
                        try:
                            block = source.recv(16384)
                        except socket.timeout:
                            continue
                        if not block:
                            break
                        if counter == "socket_received_bytes":
                            with gate.lock:
                                gate.stats[counter] += len(block)
                        if counter == "socket_sent_bytes" and gate.split and not first_sent.is_set():
                            first_sent.set()
                            pieces = split_hello(block, *gate.split)
                            with gate.lock:
                                gate.stats["hello_pieces"] = len(pieces)
                            if len(pieces) > 1:
                                target.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
                                for piece in pieces[:-1]:
                                    target.sendall(piece)
                                    with gate.lock:
                                        gate.stats[counter] += len(piece)
                                    time.sleep(.01)  # ponytail: fixed 10ms gap so pieces leave as separate segments.
                                block = pieces[-1]
                        pending = memoryview(block)
                        while pending and not stopped.is_set() and time.monotonic() < gate.deadline:
                            try:
                                count = target.send(pending)
                            except socket.timeout:
                                continue
                            if not count:
                                return
                            if counter == "socket_sent_bytes":
                                with gate.lock:
                                    gate.stats[counter] += count
                            pending = pending[count:]
                except OSError:
                    pass
                finally:
                    try:
                        target.shutdown(socket.SHUT_WR)
                    except OSError:
                        pass

            sender = threading.Thread(target=pump, args=(self.request, upstream, "socket_sent_bytes"))
            sender.start()
            try:
                pump(upstream, self.request, "socket_received_bytes")
            finally:
                stopped.set()
                sender.join(1)
        finally:
            for sock in (self.request, upstream):
                if sock is not None:
                    sock.close()
                    with gate.lock:
                        gate.sockets.discard(sock)
            with gate.lock:
                gate.workers.discard(worker)


def split_hello(block, mode, size):
    """Split the first client flight into pieces sent as separate TCP segments."""
    if mode == "record" and len(block) >= 5 and block[0] == 22 and len(block) >= 5 + int.from_bytes(block[3:5], "big"):
        end = 5 + int.from_bytes(block[3:5], "big")
        body = block[5:end]
        pieces = [block[:3] + len(body[i:i + size]).to_bytes(2, "big") + body[i:i + size]
                  for i in range(0, len(body), size)]
        return pieces + ([block[end:]] if block[end:] else [])
    if mode == "tcp":
        return [block[i:i + size] for i in range(0, len(block), size)]
    return [block]


def response_identity(path):
    if not path.is_file() or path.stat().st_size > 65536:
        return False
    data = path.read_bytes().decode("iso-8859-1").replace("\r\n", "\n")
    blocks = [b for b in data.split("\n\n") if b.startswith("HTTP/")]
    if not blocks:
        return False
    lines = blocks[-1].splitlines()
    if len(lines[0].split()) < 2 or lines[0].split()[1] != "200":
        return False
    for line in lines[1:]:
        key, separator, value = line.partition(":")
        if not separator or key.lower() in ("content-range", "location"):
            return False
        if key.lower() == "content-encoding" and value.strip().lower() != "identity":
            return False
        if key.lower() == "content-length" and value.strip() != str(SIZE):
            return False
    return True


def outcome(metrics, exit_code, received, digest, expected, identity, duration, threshold,
            within_deadline=True):
    verified = (within_deadline and exit_code == 0 and metrics.get("http_code") == 200 and
                metrics.get("num_redirects") == 0 and metrics.get("size_download") == SIZE and
                received == SIZE and digest == expected and identity)
    return "verified_1mib_fast" if verified and received / duration / 1e6 >= threshold else (
        "verified_1mib_slow" if verified else "inconclusive")


def stop_process(process):
    if process is None:
        return True
    try:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
        return process.poll() is not None
    except (OSError, subprocess.SubprocessError):
        return False


def child_environment():
    return {key: value for key, value in os.environ.items()
            if not key.startswith("=") and key.lower() not in
            ("http_proxy", "https_proxy", "all_proxy", "no_proxy") and
            not key.lower().startswith(("xray.", "xray_", "v2ray.", "v2ray_"))}


def unused_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def socks_identity(sock, account):
    def exact(size):
        data = bytearray()
        while len(data) < size:
            block = sock.recv(size - len(data))
            require(block, "SOCKS identity EOF")
            data.extend(block)
        return bytes(data)
    sock.sendall(b"\x05\x01\x02")
    require(exact(2) == b"\x05\x02", "SOCKS requires this round's password method")
    user, password = account["user"].encode(), account["pass"].encode()
    sock.sendall(b"\x01" + bytes([len(user)]) + user + bytes([len(password)]) + password)
    require(exact(2) == b"\x01\x00", "SOCKS round identity mismatch")


def probe(directory, config, ip, entry_port, spec, budget, binary, curl):
    started = time.monotonic()
    deadline = min(budget.deadline, started + spec["request_timeout_s"])
    gate = Gate(ip, entry_port, spec["per_ip_connections"], budget, deadline, spec["tcp_max_seg"],
                (spec["hello_split"], spec["hello_split_bytes"]) if spec["hello_split"] else None)
    client = download = None
    resources = []
    error = None
    payload, headers = directory / "payload.bin", directory / "headers.txt"
    metrics = {}
    application_requests = 0
    client_clean = download_clean = False
    try:
        config = copy.deepcopy(config)
        account = {"user": uuid.uuid4().hex, "pass": uuid.uuid4().hex}
        config["inbounds"][0]["settings"]["accounts"] = [account]
        socks_port = unused_port()
        config["inbounds"][0]["port"] = socks_port
        peer = config["outbounds"][0]["settings"]["vnext"][0]
        peer["port"] = gate.server_address[1]
        write_json(directory / "client.json", config)
        with (directory / "client-stderr.txt").open("x") as client_log, \
                (directory / "curl-stderr.txt").open("x") as curl_log, \
                (directory / "curl-metrics.json").open("x") as curl_metrics:
            client = subprocess.Popen([binary, "run", "-config", str(directory / "client.json")],
                                      stdin=subprocess.DEVNULL, stdout=client_log, stderr=client_log,
                                      env=child_environment())
            ready = min(deadline, time.monotonic() + 3)
            while True:
                require(client.poll() is None, "client startup failed")
                try:
                    with socket.create_connection(("127.0.0.1", socks_port), timeout=.05) as sock:
                        sock.settimeout(min(.2, max(.001, ready - time.monotonic())))
                        socks_identity(sock, account)
                        require(client.poll() is None, "client exited during identity check")
                        break
                except OSError:
                    require(time.monotonic() < ready, "client startup timeout")
                    time.sleep(.02)
            remaining = deadline - time.monotonic()
            require(remaining > 0, "request window expired")
            command = [curl, "--disable", "--globoff", "--proxy", f"socks5h://127.0.0.1:{socks_port}",
                       "--proxy-user", account["user"] + ":" + account["pass"],
                       "--noproxy", "", "--http1.1", "--request", "GET", "--retry", "0",
                       "--max-redirs", "0", "--proto", "=http,https", "--connect-timeout", str(min(3, remaining)),
                       "--max-time", str(remaining), "--max-filesize", str(SIZE), "--header",
                       "Accept-Encoding: identity", "--header", "Connection: close", "--silent",
                       "--show-error", "--output", str(payload), "--dump-header", str(headers),
                       "--write-out", "%{json}", spec["url"]]
            download = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=curl_metrics,
                                        stderr=curl_log, env=child_environment())
            application_requests = 1
            while download.poll() is None:
                resources.append(sample_process(client.pid))
                if (time.monotonic() >= deadline or gate.exhausted.is_set() or
                        budget.exhausted.is_set() or client.poll() is not None):
                    error = "deadline_budget_or_client_exit"
                    break
                if ((payload.exists() and payload.stat().st_size > SIZE) or
                        (headers.exists() and headers.stat().st_size > 65536)):
                    error = "response_size_limit"
                    break
                time.sleep(.02)
    except (OSError, ValueError, subprocess.SubprocessError):
        error = "probe_setup_or_process_error"
    finally:
        finished = time.monotonic()
        duration = max(.000001, finished - started)
        if finished >= deadline or gate.exhausted.is_set() or budget.exhausted.is_set():
            error = error or "deadline_or_budget_exhausted_at_completion"
        if client is not None and client.poll() is not None:
            error = error or "client_exited_at_completion"
        gate.seal()
        try:
            download_clean = stop_process(download)
        finally:
            try:
                client_clean = stop_process(client)
            finally:
                transport = gate.close()
                write_json(directory / "cleanup.json", {
                    "client_stopped": client_clean, "curl_stopped": download_clean,
                    "transport": transport})
    if (directory / "curl-metrics.json").exists():
        try:
            metrics = read_json(directory / "curl-metrics.json")
        except (ValueError, OSError):
            error = error or "invalid_curl_metrics"
    received, digest = 0, hashlib.sha256()
    if payload.exists():
        with payload.open("rb") as source:
            while block := source.read(65536):
                received += len(block)
                digest.update(block)
    digest = digest.hexdigest()
    identity = response_identity(headers)
    clean = client_clean and download_clean and not transport["cleanup_workers_alive"] and not transport["cleanup_sockets_open"]
    classification = outcome(metrics, download.returncode if download else None, received, digest,
                             spec["sha256"], identity, duration, spec["fast_MB_s"], finished < deadline)
    if error or not clean or gate.exhausted.is_set() or budget.exhausted.is_set() or not transport["established"]:
        classification = "inconclusive"
    samples = [s for s in resources if s]
    row = {"ip": ip, "classification": classification, "application_requests": application_requests,
           "application_retry": False, "range_used": False, "expected_bytes": SIZE,
           "tcp_max_seg": spec["tcp_max_seg"], "hello_split": spec["hello_split"], "hello_split_bytes": spec["hello_split_bytes"], "received_bytes": received, "sha256": digest, "expected_sha256": spec["sha256"],
           "duration_s": duration, "MB_s": received / duration / 1e6, "MiB_s": received / duration / SIZE,
           "http_status": metrics.get("http_code"), "curl_exit": download.returncode if download else None,
           "identity_encoding": identity, "full_file_complete": False, "error": error,
           "measurement_path": "loopback_tcp_gate", "transport": transport,
           "syn_packets": None, "wire_bytes": None, "tls_handshakes": None,
           "xhttp_requests": None, "xhttp_retries": None,
           "curl_num_connects_application_only": metrics.get("num_connects"),
           "resources": {"sampled_peak_rss_bytes": max(s["rss_bytes"] for s in samples),
                         "cpu_s_observed": max(s["cpu_s"] for s in samples)} if samples else None,
           "cleanup_complete": clean, "total_including_cleanup_s": time.monotonic() - started}
    write_json(directory / "result.json", row)
    return row


def sample_process(pid):
    try:
        fields = (Path("/proc") / str(pid) / "stat").read_text().rsplit(")", 1)[1].split()
        return {"cpu_s": (int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK"),
                "rss_bytes": int(fields[21]) * os.sysconf("SC_PAGE_SIZE")}
    except (OSError, ValueError, IndexError):
        return None


def self_test():
    spec = {"host": "entry.example.com", "ips": ["1.1.1.1"],
            "url": "http://127.0.0.1:18090/check.bin", "sha256": "a" * 64,
            "request_timeout_s": 8, "total_timeout_s": 30, "max_requests": 1,
            "max_connections": 4, "per_ip_connections": 2, "fast_MB_s": 1}
    template = {"outbounds": [{"protocol": "vless", "settings": {"vnext": [{
        "address": "entry.example.com", "port": 443,
        "users": [{"id": "00000000-0000-4000-8000-000000000001", "encryption": "none"}]}]},
        "streamSettings": {"network": "xhttp", "security": "tls", "tlsSettings": {
            "serverName": spec["host"], "fingerprint": "unsafe", "alpn": ["h2"]},
            "xhttpSettings": {"host": spec["host"], "path": "/private-path", "mode": "packet-up",
                              "rx": {"enabled": True}, "xPaddingBytes": "1000-1000", "extra": {
                                  "xPaddingBytes": "100-100", "rx": {"enabled": True}}}}}]}
    original = copy.deepcopy(template)
    assert validate_spec(spec) == ["1.1.1.1"] and spec["tcp_max_seg"] == 0
    hello = bytes([22, 3, 1, 0, 200]) + bytes(range(200)) + b"tail"
    records = split_hello(hello, "record", 64)
    assert len(records) == 5 and records[-1] == b"tail" and b"".join(r[5:] for r in records[:-1]) == hello[5:205]
    assert all(r[:3] == hello[:3] and int.from_bytes(r[3:5], "big") == len(r) - 5 for r in records[:-1])
    assert b"".join(split_hello(hello, "tcp", 64)) == hello and split_hello(hello[:100], "record", 64) == [hello[:100]]
    config, port = client_config(template, spec["host"])
    effective = config["outbounds"][0]["streamSettings"]["xhttpSettings"]
    assert port == 443 and "rx" not in effective and "extra" not in effective
    assert effective["xPaddingBytes"] == "100-100" and template == original
    assert effective["headers"]["Accept-Encoding"] == "identity"

    def rejected(action):
        try:
            action()
        except (ValueError, TypeError, KeyError):
            return
        raise AssertionError("unsafe input accepted")

    for key, value in (("ips", ["1.1.1.1", "1.1.1.1"]), ("ips", ["1.1.1.0/24"]),
                       ("total_timeout_s", float("inf")), ("max_requests", 0),
                       ("max_connections", True), ("tcp_max_seg", 100), ("tcp_max_seg", 1500), ("hello_split", "x"), ("hello_split_bytes", 4), ("host", "entry.example.com:443"),
                       ("ips", ["224.0.0.1"])):
        rejected(lambda key=key, value=value: validate_spec({**spec, key: value}))
    bad = copy.deepcopy(template)
    bad["outbounds"][0]["streamSettings"]["tlsSettings"]["allowInsecure"] = True
    rejected(lambda: client_config(bad, spec["host"]))
    bad = copy.deepcopy(template)
    bad["outbounds"][0]["streamSettings"]["xhttpSettings"]["extra"]["downloadSettings"] = {}
    rejected(lambda: client_config(bad, spec["host"]))
    bad = copy.deepcopy(template)
    bad["outbounds"].append(copy.deepcopy(bad["outbounds"][0]))
    rejected(lambda: client_config(bad, spec["host"]))
    metrics = dict(http_code=200, num_redirects=0, size_download=SIZE)
    assert outcome(metrics, 0, SIZE, "a", "a", True, 1, 1) == "verified_1mib_fast"
    for size, sha, identity in ((SIZE - 1, "a", True), (SIZE, "b", True), (SIZE, "a", False)):
        assert outcome(metrics, 0, size, sha, "a", identity, 1, 1) == "inconclusive"
    assert outcome(metrics, 0, SIZE, "a", "a", True, 10, 1) == "verified_1mib_slow"
    assert outcome(metrics, 0, SIZE, "a", "a", True, 1, 1, False) == "inconclusive"

    class SocksReply:
        def __init__(self, replies):
            self.replies = bytearray(replies)
            self.sent = []

        def recv(self, size):
            result = bytes(self.replies[:size])
            del self.replies[:size]
            return result

        def sendall(self, data):
            self.sent.append(data)

    account = {"user": "round", "pass": "secret"}
    for reply in (b"\x05\x00", b"\x05\x02\x01\x01"):
        sock = SocksReply(reply)
        rejected(lambda: socks_identity(sock, account))
        assert sock.sent[0] == b"\x05\x01\x02"
        assert len(sock.sent) <= 2  # Method/auth only; no CONNECT can leak to another listener.
    sock = SocksReply(b"\x05\x02\x01\x00")
    socks_identity(sock, account)
    assert len(sock.sent) == 2

    class FailedProcess:
        def poll(self):
            return None

        def terminate(self):
            raise OSError("self-test cleanup failure")

    assert stop_process(FailedProcess()) is False
    with tempfile.TemporaryDirectory() as temporary:
        header = Path(temporary) / "headers"
        header.write_bytes(b"HTTP/1.1 200 OK\r\nContent-Length: 1048576\r\n\r\n")
        assert response_identity(header)
        header.write_bytes(b"HTTP/1.1 200 OK\r\nContent-Encoding: gzip\r\n\r\n")
        assert not response_identity(header)
        duplicate = Path(temporary) / "bad.json"
        duplicate.write_text('{"a": 1, "a": 2}')
        rejected(lambda: read_json(duplicate))
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        listener.listen(1)
        listener.settimeout(3)
        def echo():
            with listener.accept()[0] as conn:
                conn.settimeout(3)
                data = b""
                while len(data) < 4:
                    block = conn.recv(4 - len(data))
                    assert block, "early EOF at self-test origin"
                    data += block
                conn.sendall(data)
        echo_thread = threading.Thread(target=echo)
        echo_thread.start()
        budget = Budget(1, time.monotonic() + 5)
        gate = Gate("127.0.0.1", listener.getsockname()[1], 2, budget)
        try:
            with socket.create_connection(gate.server_address, timeout=2) as conn:
                conn.sendall(b"test")
                result = b""
                while len(result) < 4:
                    block = conn.recv(4 - len(result))
                    assert block, "early EOF at self-test gate"
                    result += block
                assert result == b"test"
            with socket.create_connection(gate.server_address, timeout=2):
                assert gate.exhausted.wait(2)
        finally:
            stats = gate.close()
            echo_thread.join(3)
        assert budget.attempts == 1 and stats["connect_attempts"] == 1
        assert stats["established"] == 1 and stats["rejected"] == 1
        assert stats["socket_sent_bytes"] == 4 and stats["socket_received_bytes"] == 4
        assert not stats["cleanup_workers_alive"] and not stats["cleanup_sockets_open"]
        budget = Budget(4, time.monotonic() + 5)
        gate = Gate("127.0.0.1", listener.getsockname()[1], 4, budget, time.monotonic() + .05)
        try:
            gate.expiry.join(1)
            assert gate.closing and time.monotonic() < budget.deadline
            with socket.create_connection(gate.server_address, timeout=1):
                pass
        finally:
            stats = gate.close()
        assert budget.attempts == 0 and stats["connect_attempts"] == 0
        assert not stats["cleanup_workers_alive"] and not stats["cleanup_sockets_open"]
    print("self-test passed (offline validation and loopback only)")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--spec", help="Private JSON: fixed whitelist, resource hash and budgets")
    parser.add_argument("--template", help="Private single-outbound VLESS/XHTTP experiment config")
    parser.add_argument("--output", help="New directory beneath artifacts; never reuse a round")
    parser.add_argument("--execute", action="store_true", help="NETWORK: requires separate field authorization")
    parser.add_argument("--binary", help="Existing experiment Xray binary; no building/downloading")
    parser.add_argument("--curl", default="curl")
    args = parser.parse_args()
    if args.self_test:
        require(not args.execute and not args.spec and not args.template and not args.output,
                "self-test cannot be combined with a field round")
        self_test()
        return 0
    require(args.spec and args.template and args.output, "spec, template and output required")
    spec = read_json(args.spec)
    ips = validate_spec(spec)
    config, entry_port = client_config(read_json(args.template), spec["host"])
    output = Path(args.output).resolve()
    require("artifacts" in [part.lower() for part in output.parts], "output must be beneath artifacts")
    require(not output.exists() and output.parent.is_dir(), "use a new round in an existing parent")
    binary = None
    if args.execute:
        require(args.binary and Path(args.binary).is_file(), "explicit existing binary required")
        binary = str(Path(args.binary).resolve())
        def interrupted(signum, frame):
            raise KeyboardInterrupt
        signal.signal(signal.SIGTERM, interrupted)
    output.mkdir(mode=0o700)
    write_json(output / "plan.json", {"network_enabled": args.execute, "ips": ips,
               "expected_bytes": SIZE, "sha256": spec["sha256"], "max_requests": spec["max_requests"],
               "max_connections": spec["max_connections"], "per_ip_connections": spec["per_ip_connections"],
               "request_timeout_s": spec["request_timeout_s"], "total_timeout_s": spec["total_timeout_s"],
               "entry_port": entry_port, "measurement_path": "loopback_tcp_gate",
               "host_sha256": hashlib.sha256(spec["host"].encode()).hexdigest()})
    if not args.execute:
        write_json(output / "client-preview.json", config)
        print("Offline plan and private client preview written; no connections started.")
        return 0
    started = time.monotonic()
    budget = Budget(spec["max_connections"], started + spec["total_timeout_s"])
    rows = []
    error = None
    try:
        for index, ip in enumerate(ips):
            if (time.monotonic() >= budget.deadline or budget.exhausted.is_set() or
                    budget.attempts >= budget.limit or len(rows) >= spec["max_requests"]):
                error = "window_or_global_budget_exhausted"
                break
            directory = output / str(index)
            directory.mkdir(mode=0o700)
            row = probe(directory, config, ip, entry_port, spec, budget, binary, args.curl)
            rows.append(row)
            print(json.dumps({key: row[key] for key in ("ip", "classification", "received_bytes", "MB_s", "MiB_s")}))
            if not row["cleanup_complete"]:
                error = "cleanup_incomplete"
                break
    except BaseException:
        error = "interrupted_or_local_error"
        raise
    finally:
        write_json(output / "result.json", {"rows": rows, "connect_attempts": budget.attempts,
                   "application_requests": sum(row["application_requests"] for row in rows),
                   "duration_s": time.monotonic() - started, "error": error,
                   "all_candidates_observed": len(rows) == len(ips), "full_file_complete": False,
                   "measurement_limit": "Gate changes TCP buffering and packetization. Socket bytes exclude "
                   "IP/TCP headers and retransmissions. SYN/TLS/internal XHTTP retries are unobserved. "
                   "One 1MiB sample cannot establish permanent restriction or sustained 1GiB speed."})
    return int(error is not None or any(row["classification"] == "inconclusive" for row in rows))


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, TypeError, KeyError, OSError) as failure:
        # Do not expose UUIDs, authenticated URLs, headers or private template contents.
        print("Classification stopped: " + type(failure).__name__ + "; inspect private round files.")
        raise SystemExit(2)
