"""Check a native RayBox executable without TUN, system proxy, or remote connections."""
import argparse
import json
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path


def check_runtime(executable):
    executable = str(executable.resolve(strict=True))
    version = subprocess.check_output([executable, "version"], timeout=15, text=True)
    if "RayBox" not in version:
        raise ValueError("Executable must report RayBox version")
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    token = uuid.uuid4().hex
    config = {
        "log": {"level": "error"}, "inbounds": [],
        "outbounds": [
            {"type": "selector", "tag": "proxy", "outbounds": ["direct", "session"]},
            {"type": "direct", "tag": "direct"},
            {"type": "vless", "tag": "session", "server": "127.0.0.1", "server_port": 443,
             "uuid": "00000000-0000-0000-0000-000000000001",
             "transport": {"type": "xhttp", "path": "/session", "mode": "packet-up"}}],
        "route": {"final": "proxy"},
        "experimental": {"clash_api": {"external_controller": f"127.0.0.1:{port}", "secret": token}},
    }
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    base = f"http://127.0.0.1:{port}"

    def request(path, body=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(base + path, data=data, method="GET" if data is None else "PUT",
                                     headers={"Authorization": "Bearer " + token,
                                              "Content-Type": "application/json"})
        with opener.open(req, timeout=1) as response:
            return response.read()

    with tempfile.TemporaryDirectory(prefix="raybox-windows-check-") as temporary:
        path = Path(temporary) / "config.json"
        path.write_text(json.dumps(config), encoding="utf-8")
        subprocess.run([executable, "check", "-c", str(path)], check=True, timeout=15)
        with (Path(temporary) / "run.log").open("wb") as log:
            process = subprocess.Popen([executable, "run", "-c", str(path)], stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 15
                while True:
                    if process.poll() is not None:
                        raise ValueError("Core exited before Clash API became ready")
                    try:
                        api_version = json.loads(request("/version"))
                        break
                    except (urllib.error.URLError, TimeoutError):
                        if time.monotonic() >= deadline:
                            raise TimeoutError("Loopback Clash API did not become ready")
                        time.sleep(0.1)
                if not api_version.get("version"):
                    raise ValueError("Missing Clash API version")
                for name in ("session", "direct"):
                    request("/proxies/proxy", {"name": name})
                    proxies = json.loads(request("/proxies"))["proxies"]
                    if proxies["proxy"]["now"] != name or "session" not in proxies:
                        raise ValueError("Clash API selector state mismatch")
            finally:
                if process.poll() is None:
                    process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
    return {"version": version.splitlines()[0], "check": True, "run": True,
            "loopbackClashApi": True, "selectorSwitches": 2, "managedProcessStopped": True}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("executable", type=Path)
    print(json.dumps(check_runtime(parser.parse_args().executable), indent=2))
