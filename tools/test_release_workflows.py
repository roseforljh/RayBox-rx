"""Run the workflow publish steps against a fake GitHub CLI.

Covers first publish, same-SHA rerun, partial and stale draft recovery, non-404 API
errors and drafts that target another commit.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
SHA = "0123456789abcdef0123456789abcdef01234567"
REPO = "roseforljh/RayBox-rx"

FAKE_GH = r'''
import json, os, shutil, sys
sys.stdout.reconfigure(newline=chr(10))
from pathlib import Path
state_path = Path(os.environ["FAKE_GH_STATE"])
state = json.loads(state_path.read_text())
args = sys.argv[1:]
state["calls"].append(args)
def save(): state_path.write_text(json.dumps(state))
def fail(message, code=1):
    save(); sys.stderr.write(message + "\n"); sys.exit(code)
def release(tag):
    return next((r for r in state["releases"] if r["tag_name"] == tag), None)
if args[0] == "api":
    path = next(a for a in args[1:] if a.startswith("repos/"))
    if state.get("api_error"): fail("gh: Server Error (HTTP 500)")
    parts = path.split("?")[0].split("/")
    if parts[3:6] == ["git", "ref", "tags"]:
        if not state["tag_commit"]: fail("gh: Not Found (HTTP 404)")
        print(json.dumps({"object": {"type": "commit", "sha": state["tag_commit"]}}))
    elif parts[3:] == ["releases"]:
        jq = args[args.index("--jq") + 1]
        for r in state["releases"]:
            if r["tag_name"] in jq: print(r["id"])
    elif parts[3] == "releases":
        r = next(r for r in state["releases"] if str(r["id"]) == parts[4])
        print(json.dumps({**{k: r[k] for k in ("id", "tag_name", "draft", "target_commitish")},
                          "assets": [{"name": n} for n in r["assets"]]}))
    else: fail("unexpected api " + path)
elif args[:2] == ["release", "create"]:
    state["releases"].append({"id": 7, "tag_name": args[2], "draft": True,
                              "target_commitish": args[args.index("--target") + 1], "assets": {}})
elif args[:2] == ["release", "upload"]:
    r = release(args[2]); name = Path(args[3]).name
    if name in r["assets"]: fail("asset exists")
    r["assets"][name] = Path(args[3]).read_bytes().hex()
elif args[:2] == ["release", "download"]:
    target = Path(args[args.index("--dir") + 1]); target.mkdir(parents=True, exist_ok=True)
    for name, data in release(args[2])["assets"].items(): (target / name).write_bytes(bytes.fromhex(data))
elif args[:2] == ["release", "edit"]:
    r = release(args[2]); r["draft"] = False; state["tag_commit"] = args[args.index("--target") + 1]
else: fail("unexpected gh " + " ".join(args))
save()
'''

FAKE_JQ = r'''
import json, sys
sys.stdout.reconfigure(newline=chr(10))
value = json.load(sys.stdin)
path = sys.argv[-1]
if path == ".assets[].name":
    for item in value["assets"]: print(item["name"])
    sys.exit()
for key in path.strip(".").split("."): value = value[key]
print(str(value).lower() if isinstance(value, bool) else value)
'''


def bash():
    # On Windows PATH may resolve to WSL's bash, which cannot run these Windows paths.
    if os.name == "nt":
        for root in Path(shutil.which("git")).resolve().parents:
            for candidate in (root / "bin/bash.exe", root / "usr/bin/bash.exe"):
                if candidate.is_file():
                    return str(candidate)
        raise RuntimeError("Git Bash is required on Windows")
    return shutil.which("bash")


def step(workflow, name):
    data = yaml.safe_load((ROOT / ".github/workflows" / workflow).read_text())
    for job in data["jobs"].values():
        for item in job["steps"]:
            if item.get("name") == name:
                return item["run"]
    raise KeyError(name)


def manifest(kind, files):
    common = {"repository": REPO, "commit": SHA, "dirty": False, "sourceDigest": "d",
              "sourceDigestAlgorithm": "sha256-git-path-kind-content-v1"}
    import hashlib
    assets = {f: {"file": f, "size": len(files[f]), "sha256": hashlib.sha256(files[f]).hexdigest()} for f in files}
    if kind == "android":
        standard = "raybox-android-1.0.0-0123456789ab-standard.aar"
        legacy = "raybox-android-1.0.0-0123456789ab-legacy.aar"
        integrated = "raybox-android-1.0.0-0123456789ab-integrated.zip"
        return {**common, "tag": "android-" + SHA,
                "outputs": [{"name": "raybox.aar", "size": len(files[standard]), "sha256": assets[standard]["sha256"]},
                            {"name": "raybox-legacy.aar", "size": len(files[legacy]), "sha256": assets[legacy]["sha256"]}],
                "versionedOutputs": [{"name": standard, "size": len(files[standard]), "sha256": assets[standard]["sha256"]},
                                     {"name": legacy, "size": len(files[legacy]), "sha256": assets[legacy]["sha256"]}],
                "bundleName": integrated}
    source = {"url": f"https://github.com/{REPO}/tree/{SHA}", "runtimeLicense": "docs/reference/runtime/LICENSE",
              "protocolLicense": "docs/reference/protocol/LICENSE"}
    if kind == "windows":
        return {**common, "fullHead": SHA, "tag": "windows-" + SHA, "source": source, "assets": assets}
    return {**common, "schemaVersion": 1, "tag": "server-" + SHA, "rxDefault": False, "oobDefault": False,
            "source": {**source, "entry": "cmd/raybox-server"},
            "assets": {k: v for k, v in assets.items() if k.startswith("raybox-linux")}}


CASES = {
    "android": ("android-core.yml", "Verify source manifest and publish immutable release", "artifacts/libbox", "build.json",
                ["raybox-android-1.0.0-0123456789ab-standard.aar", "raybox-android-1.0.0-0123456789ab-legacy.aar", "raybox-android-1.0.0-0123456789ab-integrated.zip", "SHA256SUMS", "LICENSE", "RayBox-Protocol-LICENSE", "SOURCE.txt"]),
    "windows": ("windows-core.yml", "Verify source manifest and publish immutable release", "artifacts/windows", "manifest.json",
                ["raybox-windows-amd64.exe", "LICENSE", "RayBox-Protocol-LICENSE", "SOURCE.txt"]),
    "server": ("server-core.yml", "Create or resume immutable release", "artifacts", "manifest.json",
               ["raybox-linux-amd64", "raybox-linux-arm64", "raybox-linux-armv7", "SOURCE.txt", "LICENSE", "RayBox-Protocol-LICENSE"]),
}


class ReleaseWorkflowTest(unittest.TestCase):
    def run_case(self, kind, releases=(), tag_commit=None, api_error=False):
        workflow, name, directory, manifest_name, names = CASES[kind]
        temp = Path(tempfile.mkdtemp(prefix="raybox-wf-"))
        self.addCleanup(shutil.rmtree, temp, True)
        bin_dir = temp / "bin"; bin_dir.mkdir()
        (bin_dir / "fake_gh.py").write_text(FAKE_GH)
        (bin_dir / "fake_jq.py").write_text(FAKE_JQ)
        for tool in ("gh", "jq"):
            script = bin_dir / tool
            script.write_text(f'#!/bin/sh\nexec "{Path(sys.executable).as_posix()}" "$(dirname "$0")/fake_{tool}.py" "$@"\n')
            script.chmod(0o755)
            (bin_dir / (tool + ".cmd")).write_text(f'@"{sys.executable}" "%~dp0fake_{tool}.py" %*\n')
        # The server step calls python3; Windows may only provide a Store alias.
        (bin_dir / "python3").write_text(f'#!/bin/sh\nexec "{Path(sys.executable).as_posix()}" "$@"\n')
        (bin_dir / "python3").chmod(0o755)
        work = temp / "work"; (work / directory).mkdir(parents=True)
        files = {n: (kind + n).encode() for n in names}
        for n, data in files.items(): (work / directory / n).write_bytes(data)
        (work / directory / manifest_name).write_text(json.dumps(manifest(kind, files)))
        state = {"calls": [], "tag_commit": tag_commit, "api_error": api_error, "releases": list(releases)}
        state_path = temp / "state.json"; state_path.write_text(json.dumps(state))
        script = step(workflow, name)
        env = {**os.environ, "GITHUB_SHA": SHA, "GITHUB_REPOSITORY": REPO, "FAKE_GH_STATE": str(state_path),
               "PATH": str(bin_dir) + os.pathsep + os.environ["PATH"]}
        if kind == "server":
            # Script files avoid native argument re-quoting of the embedded jq filters.
            path = temp / "step.sh"
            path.write_bytes(("set -euo pipefail\n" + script).encode())
            command = [bash(), path.as_posix()]
        else:
            path = temp / "step.ps1"
            path.write_text(script, encoding="utf-8")
            command = ["pwsh", "-NoProfile", "-NonInteractive", "-File", str(path)]
        result = subprocess.run(command, cwd=work, env=env, capture_output=True, text=True, encoding="utf-8", errors="replace")
        return result, json.loads(state_path.read_text()), files | {manifest_name: (work / directory / manifest_name).read_bytes()}

    def assert_published(self, state, files):
        (release,) = state["releases"]
        self.assertFalse(release["draft"])
        self.assertEqual(state["tag_commit"], SHA)
        self.assertEqual({k: bytes.fromhex(v) for k, v in release["assets"].items()}, files)

    def test_workflows(self):
        for kind in CASES:
            with self.subTest(kind=kind, case="fresh"):
                result, state, files = self.run_case(kind)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assert_published(state, files)
                published = state["releases"]
            with self.subTest(kind=kind, case="same-sha rerun"):
                result, state, _ = self.run_case(kind, published, SHA)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertFalse([c for c in state["calls"] if c[:2] in (["release", "upload"], ["release", "create"])])
            with self.subTest(kind=kind, case="partial draft"):
                # Earlier run uploaded the manifest and first binary, then stopped.
                (fresh,) = published
                keep = {CASES[kind][3], CASES[kind][4][0]}
                draft = {**fresh, "draft": True, "assets": {k: v for k, v in fresh["assets"].items() if k in keep}}
                result, state, files = self.run_case(kind, [draft])
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                uploads = [Path(c[3]).name for c in state["calls"] if c[:2] == ["release", "upload"]]
                self.assertEqual(sorted(uploads), sorted(set(files) - keep))
                self.assert_published(state, files)
            with self.subTest(kind=kind, case="stale draft asset"):
                draft = {"id": 9, "tag_name": f"{kind}-{SHA}", "draft": True, "target_commitish": SHA,
                         "assets": {CASES[kind][4][0]: b"stale".hex()}}
                result, state, _ = self.run_case(kind, [draft])
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Existing asset differs", result.stdout + result.stderr)
                self.assertFalse([c for c in state["calls"] if c[:2] == ["release", "upload"]])
                self.assertTrue(state["releases"][0]["draft"])
            with self.subTest(kind=kind, case="api error"):
                result, state, _ = self.run_case(kind, api_error=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("HTTP 500", result.stdout + result.stderr)
                self.assertFalse(state["releases"])
            with self.subTest(kind=kind, case="draft for other commit"):
                draft = {"id": 9, "tag_name": f"{kind}-{SHA}", "draft": True, "target_commitish": "f" * 40, "assets": {}}
                result, state, _ = self.run_case(kind, [draft])
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("targets " + "f" * 40, result.stdout + result.stderr)
                self.assertTrue(state["releases"][0]["draft"])


if __name__ == "__main__":
    unittest.main()
