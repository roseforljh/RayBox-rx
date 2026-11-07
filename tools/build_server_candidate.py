"""Build local Linux server candidates from the same RayBox source snapshot."""
import argparse
import concurrent.futures
import hashlib
import importlib.util
import json
import os
import shutil
import struct
import subprocess
import tempfile
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("artifact_checks", ROOT / "tools/check_android_artifacts.py")
checks = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checks)
TARGETS = {"amd64": ("amd64", "", 2, 62), "arm64": ("arm64", "", 2, 183), "armv7": ("arm", "7", 1, 40)}


def build(allow_dirty):
    source = checks.source_identity(ROOT)
    if source["dirty"] and not allow_dirty:
        raise ValueError("Dirty server source requires --allow-dirty")
    local_go = ROOT / "artifacts/toolchain/go/bin" / ("go.exe" if os.name == "nt" else "go")
    go = str(local_go) if local_go.is_file() else "go"
    env = dict(os.environ, GOTOOLCHAIN="local", GOWORK="off", CGO_ENABLED="0")
    go_version = subprocess.check_output([go, "version"], env=env, text=True).strip()
    if not go_version.startswith("go version go1.27.3 "):
        raise ValueError("Go 1.27.3 is required")
    version = subprocess.check_output([go, "run", "./cmd/internal/read_tag"], cwd=ROOT, env=env, text=True).strip()
    if version != (ROOT / "VERSION").read_text().strip() + "-" + source["commit"][:12]:
        raise ValueError("Unexpected RayBox version: " + version)
    artifacts = ROOT / "artifacts"
    output = artifacts / ("server-candidate" if source["dirty"] else "server")
    with tempfile.TemporaryDirectory(prefix="server-build-", dir=artifacts) as temporary:
        staging = Path(temporary)

        def target(arch):
            goarch, goarm, elf_class, machine = TARGETS[arch]
            path = staging / ("raybox-linux-" + arch)
            command = [go, "-C", str(ROOT), "build", "-trimpath", "-buildvcs=false",
                       "-ldflags=-s -w -buildid= -X github.com/roseforljh/RayBox-rx/constant.Version=" + version +
                       " -X github.com/roseforljh/RayBox-rx/internal/protocol/core.build=" + version,
                       "-o", str(path), "./cmd/raybox-server"]
            subprocess.run(command, env=dict(env, GOOS="linux", GOARCH=goarch, GOARM=goarm), check=True)
            data = path.read_bytes()
            if (len(data) < 64 or data[:6] != b"\x7fELF" + bytes((elf_class, 1)) or
                    struct.unpack_from("<H", data, 18)[0] != machine):
                raise ValueError("Server ELF architecture mismatch: " + arch)
            return arch, {"file": path.name, "size": len(data), "sha256": hashlib.sha256(data).hexdigest()}

        with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:
            assets = dict(executor.map(target, TARGETS))
        # Same license assets as the server release workflow.
        shutil.copyfile(ROOT / "docs/reference/runtime/LICENSE", staging / "LICENSE")
        shutil.copyfile(ROOT / "docs/reference/protocol/LICENSE", staging / "RayBox-Protocol-LICENSE")
        (staging / "SOURCE.txt").write_text(
            "RayBox server core\nCommit: " + source["commit"] + "\nSource digest: " + source["sourceDigest"] +
            "\n\n" + (ROOT / "SOURCE.md").read_text(encoding="utf-8"), encoding="utf-8")
        manifest = {"schemaVersion": 1, "repository": "roseforljh/RayBox-rx", **source,
                    "tag": None if source["dirty"] else "server-" + source["commit"],
                    "product": "RayBox", "version": version, "goVersion": go_version,
                    "architectures": list(TARGETS), "rxDefault": False, "oobDefault": False,
                    "source": {"entry": "cmd/raybox-server", "runtimeLicense": "docs/reference/runtime/LICENSE",
                               "protocolLicense": "docs/reference/protocol/LICENSE",
                               "url": "https://github.com/roseforljh/RayBox-rx/tree/" + source["commit"]},
                    "assets": assets}
        (staging / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
        if checks.source_identity(ROOT) != source:
            raise ValueError("Source changed during server build; no candidate was promoted")
        backup = output.with_name(output.name + ".backup-" + uuid.uuid4().hex)
        had_output = output.exists()
        if had_output:
            output.rename(backup)
        try:
            shutil.copytree(staging, output)
        except BaseException:
            if output.exists():
                shutil.rmtree(output)
            if had_output:
                backup.rename(output)
            raise
        if had_output:
            shutil.rmtree(backup)
    return {"directory": str(output), **manifest}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--allow-dirty", action="store_true")
    print(json.dumps(build(parser.parse_args().allow_dirty), indent=2))
