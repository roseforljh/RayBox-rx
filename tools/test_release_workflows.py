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
VERSION = "0.0.1"

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

def manifest(kind, files):
    common = {"repository": REPO, "commit": SHA, "dirty": False, "sourceDigest": "d",
              "version": VERSION, "sourceDigestAlgorithm": "sha256-git-path-kind-content-v1",
              "rxDefault": False, "oobDefault": False,
              "source": {"url": f"https://github.com/{REPO}/tree/{SHA}",
                         "runtimeLicense": "docs/reference/runtime/LICENSE", "protocolLicense": "docs/reference/protocol/LICENSE"}}
    import hashlib
    assets = {f: {"file": f, "size": len(files[f]), "sha256": hashlib.sha256(files[f]).hexdigest()} for f in files}
    if kind == "android":
        standard = "raybox-android-0.0.1-standard.aar"
        legacy = "raybox-android-0.0.1-legacy.aar"
        integrated = "raybox-android-0.0.1-integrated.zip"
        return {**common, "tag": "v" + VERSION,
                "outputs": [{"name": "raybox.aar", "size": len(files[standard]), "sha256": assets[standard]["sha256"]},
                            {"name": "raybox-legacy.aar", "size": len(files[legacy]), "sha256": assets[legacy]["sha256"]}],
                "versionedOutputs": [{"name": standard, "size": len(files[standard]), "sha256": assets[standard]["sha256"]},
                                     {"name": legacy, "size": len(files[legacy]), "sha256": assets[legacy]["sha256"]}],
                "bundleName": integrated}
    source = {"url": f"https://github.com/{REPO}/tree/{SHA}", "runtimeLicense": "docs/reference/runtime/LICENSE",
              "protocolLicense": "docs/reference/protocol/LICENSE"}
    if kind == "windows":
        return {**common, "fullHead": SHA, "tag": "v" + VERSION, "source": source, "assets": assets,
                "goos": "windows", "goarch": "amd64", "pe": {"machine": "amd64"}, "goVersion": "go version go1.27.3 windows/amd64"}
    return {**common, "schemaVersion": 1, "tag": "v" + VERSION, "rxDefault": False, "oobDefault": False,
            "source": {**source, "entry": "cmd/raybox-server"},
            "architectures": ["amd64", "arm64", "armv7"], "goVersion": "go version go1.27.3 linux/amd64",
            "assets": {k: v for k, v in assets.items() if k.startswith("raybox-linux")}}


CASES = {
    "android": ("artifacts/libbox", "build.json",
                ["raybox-android-0.0.1-standard.aar", "raybox-android-0.0.1-legacy.aar", "raybox-android-0.0.1-integrated.zip", "SHA256SUMS", "LICENSE", "RayBox-Protocol-LICENSE", "SOURCE.txt"]),
    "windows": ("artifacts/windows", "manifest.json",
                ["raybox-windows-amd64.exe", "LICENSE", "RayBox-Protocol-LICENSE", "SOURCE.txt"]),
    "server": ("artifacts/server", "manifest.json",
               ["raybox-linux-amd64", "raybox-linux-arm64", "raybox-linux-armv7", "SOURCE.txt", "LICENSE", "RayBox-Protocol-LICENSE"]),
}


class ReleaseWorkflowTest(unittest.TestCase):
    def test_release_gate_wiring(self):
        data = yaml.safe_load((ROOT / '.github/workflows/release.yml').read_text())
        triggers = data.get('on', data.get(True))
        self.assertEqual(triggers['push']['branches'], ['main'])
        self.assertIs(triggers['workflow_dispatch']['inputs']['publish']['default'], False)
        gate = data['jobs']['release-gate']
        self.assertEqual(gate['outputs']['publish'], '${{ steps.gate.outputs.publish }}')
        self.assertEqual(gate['steps'][0]['with']['fetch-depth'], 0)
        self.assertEqual(gate['steps'][1], {'id': 'gate', 'run': 'python3 -I tools/check_release_trigger.py'})
        publish = data['jobs']['publish']
        self.assertEqual(publish['needs'], ['release-gate', 'android', 'windows', 'server'])
        self.assertEqual(publish['if'], "${{ needs['release-gate'].outputs.publish == 'true' }}")
        self.assertFalse(publish['concurrency']['cancel-in-progress'])
        self.assertEqual(publish['permissions'], {'contents': 'write'})
        self.assertEqual(publish['steps'][-1]['run'], './tools/publish-release.ps1')
        downloads = [s['with'] for s in publish['steps'] if s.get('uses') == 'actions/download-artifact@v4']
        self.assertEqual({d['name']: d['path'] for d in downloads}, {
            'raybox-android-${{ github.sha }}': 'artifacts/libbox',
            'raybox-windows-${{ github.sha }}': 'artifacts/windows',
            'raybox-server-${{ github.sha }}': 'artifacts/server',
        })
        for script, snippet in [('build-libbox.ps1', 'else { "v$version" }'),
                                ('build-windows.ps1', 'else { "v$commitVersion" }'),
                                ('build_server_candidate.py', 'else "v" + version')]:
            self.assertIn(snippet, (ROOT / 'tools' / script).read_text(encoding='utf-8'))
        for kind in CASES:
            with self.subTest(kind=kind):
                self.assertEqual(data['jobs'][kind]['uses'], f'./.github/workflows/{kind}-core.yml')
                self.assertNotIn('needs', data['jobs'][kind])
                child = yaml.safe_load((ROOT / f'.github/workflows/{kind}-core.yml').read_text())
                self.assertEqual(set(child.get('on', child.get(True))), {'workflow_call', 'workflow_dispatch'})
                self.assertEqual(child['permissions'], {'contents': 'read'})
                self.assertNotIn('publish', child['jobs'])
        server = yaml.safe_load((ROOT / '.github/workflows/server-core.yml').read_text())
        self.assertEqual(server['jobs']['assemble']['needs'], 'build')
        self.assertIn('Assemble and verify release assets', str(server['jobs']['assemble']))

    def test_release_trigger(self):
        with tempfile.TemporaryDirectory(prefix="raybox-gate-") as directory:
            work = Path(directory)
            def git(*args):
                return subprocess.check_output(["git", *args], cwd=work, encoding="utf-8").strip()
            git("init", "-q")
            git("config", "user.name", "Release test")
            git("config", "user.email", "release-test@example.invalid")
            def commit(version):
                if version is not None:
                    (work / "VERSION").write_text(version, encoding="utf-8")
                    git("add", "VERSION")
                git("commit", "--allow-empty", "-qm", "release gate fixture")
                return git("rev-parse", "HEAD")
            absent = commit(None)
            first = commit("0.0.1\n")
            unchanged = commit("0.0.1\n")
            changed = commit("0.0.2\n")
            multi = commit("0.0.3\n")
            reverted = commit("0.0.1\n")
            newline = commit("0.0.1\r\n")
            cases = [
                ("same version", "push", {"before": first}, unchanged, False),
                ("version changed", "push", {"before": unchanged}, changed, True),
                ("multiple commits", "push", {"before": first}, multi, True),
                ("changed then reverted", "push", {"before": first}, reverted, False),
                ("line ending only", "push", {"before": first}, newline, False),
                ("initial push", "push", {"before": "0" * 40}, first, True),
                ("VERSION introduced", "push", {"before": absent}, first, True),
                ("missing before", "push", {}, first, None),
                ("history unavailable", "push", {"before": "f" * 40}, first, None),
                ("target VERSION missing", "push", {"before": first}, absent, None),
                ("manual default", "workflow_dispatch", {}, first, False),
                ("manual boolean false", "workflow_dispatch", {"inputs": {"publish": False}}, first, False),
                ("manual string false", "workflow_dispatch", {"inputs": {"publish": "false"}}, first, False),
                ("manual boolean true", "workflow_dispatch", {"inputs": {"publish": True}}, first, True),
                ("manual string true", "workflow_dispatch", {"inputs": {"publish": "true"}}, first, True),
                ("unrelated event", "pull_request", {}, first, False),
            ]
            for name, event_name, event, sha, expected in cases:
                with self.subTest(case=name):
                    event_path = work / "event.json"
                    output_path = work / "output.txt"
                    event_path.write_text(json.dumps(event), encoding="utf-8")
                    output_path.write_text("", encoding="utf-8")
                    env = {**os.environ, "GITHUB_EVENT_PATH": str(event_path), "GITHUB_EVENT_NAME": event_name,
                           "GITHUB_SHA": sha, "GITHUB_OUTPUT": str(output_path)}
                    result = subprocess.run([sys.executable, "-I", str(ROOT / "tools/check_release_trigger.py")],
                                            cwd=work, env=env, capture_output=True, text=True, encoding="utf-8")
                    if expected is None:
                        self.assertNotEqual(result.returncode, 0)
                        self.assertEqual(output_path.read_text(), "")
                    else:
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertEqual(output_path.read_text(), "publish=" + str(expected).lower() + "\n")

    def run_case(self, releases=(), tag_commit=None, api_error=False, missing=None, changed_time=False, corrupt=None):
        temp = Path(tempfile.mkdtemp(prefix='raybox-wf-'))
        self.addCleanup(shutil.rmtree, temp, True)
        bin_dir = temp / 'bin'; bin_dir.mkdir()
        (bin_dir / 'fake_gh.py').write_text(FAKE_GH)
        script = bin_dir / 'gh'
        script.write_text(f'#!/bin/sh\nexec "{Path(sys.executable).as_posix()}" "$(dirname "$0")/fake_gh.py" "$@"\n')
        script.chmod(0o755)
        (bin_dir / 'gh.cmd').write_text(f'@"{sys.executable}" "%~dp0fake_gh.py" %*\n')
        work = temp / 'work'; work.mkdir()
        (work / 'VERSION').write_text(VERSION + '\n')
        for kind, (directory, manifest_name, names) in CASES.items():
            root = work / directory
            root.mkdir(parents=True)
            files = {n: (kind + n).encode() for n in names}
            for name, content in files.items(): (root / name).write_bytes(content)
            metadata = manifest(kind, files)
            if changed_time and kind == 'android': metadata['builtAt'] = 'another build time'
            if corrupt == kind: metadata['commit'] = 'f' * 40
            (root / manifest_name).write_text(json.dumps(metadata))
            if missing == kind: (root / manifest_name).unlink()
            if missing == kind + '-binary': (root / names[0]).unlink()
        state = {'calls': [], 'tag_commit': tag_commit, 'api_error': api_error, 'releases': list(releases)}
        state_path = temp / 'state.json'; state_path.write_text(json.dumps(state))
        env = {**os.environ, 'GITHUB_SHA': SHA, 'GITHUB_REPOSITORY': REPO, 'FAKE_GH_STATE': str(state_path),
               'PATH': str(bin_dir) + os.pathsep + os.environ['PATH']}
        result = subprocess.run(['pwsh', '-NoProfile', '-NonInteractive', '-File', str(ROOT / 'tools/publish-release.ps1')],
                                cwd=work, env=env, capture_output=True, text=True, encoding='utf-8', errors='replace')
        staging = work / 'artifacts/release'
        files = {p.name: p.read_bytes() for p in staging.glob('*') if p.is_file()}
        return result, json.loads(state_path.read_text()), files

    def assert_published(self, state, files):
        import hashlib
        (release,) = state['releases']
        self.assertFalse(release['draft'])
        self.assertEqual(release['tag_name'], f'v{VERSION}')
        self.assertEqual(state['tag_commit'], SHA)
        actual = {k: bytes.fromhex(v) for k, v in release['assets'].items()}
        self.assertEqual(actual, files)
        combined = json.loads(actual['manifest.json'].decode('utf-8-sig'))
        self.assertEqual(set(combined['assets']), set(actual) - {'manifest.json'})
        for name, entry in combined['assets'].items():
            self.assertEqual(entry['size'], len(actual[name]))
            self.assertEqual(entry['sha256'], hashlib.sha256(actual[name]).hexdigest())
        for name in ('android-build.json', 'windows-manifest.json', 'server-manifest.json'):
            self.assertIn(name, actual)

    def test_workflows(self):
        result, state, files = self.run_case()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assert_published(state, files)
        creates = [c for c in state['calls'] if c[:2] == ['release', 'create']]
        self.assertEqual(len(creates), 1)
        self.assertEqual(creates[0][creates[0].index('--title') + 1], f'RayBox v{VERSION}')
        published = state['releases']
        for changed_time in (False, True):
            with self.subTest(case='same-SHA rerun', changed_time=changed_time):
                result, state, files = self.run_case(published, SHA, changed_time=changed_time)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assert_published(state, files)
                self.assertFalse([c for c in state['calls'] if c[:2] in (['release', 'upload'], ['release', 'create'], ['release', 'edit'])])
        with self.subTest(case='version belongs to another commit'):
            result, state, _ = self.run_case(published, 'f' * 40)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse([c for c in state['calls'] if c[:2] == ['release', 'create']])
        fresh = published[0]
        for keep in ({'manifest.json', 'android-build.json', 'raybox-linux-amd64'}, {'android-build.json', 'raybox-linux-amd64'}):
            with self.subTest(case='partial draft', keep=keep):
                draft = {**fresh, 'draft': True, 'assets': {k: v for k, v in fresh['assets'].items() if k in keep}}
                result, state, files = self.run_case([draft], changed_time=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assert_published(state, files)
                uploads = [Path(c[3]).name for c in state['calls'] if c[:2] == ['release', 'upload']]
                self.assertEqual(set(uploads), set(files) - keep)
        for case, assets in [('stale draft asset', {'raybox-linux-amd64': b'stale'.hex()}),
                             ('published missing asset', {k: v for k, v in fresh['assets'].items() if k != 'windows-manifest.json'})]:
            with self.subTest(case=case):
                draft = case.startswith('stale')
                release = {**fresh, 'draft': draft, 'assets': assets}
                result, state, _ = self.run_case([release], None if draft else SHA)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse([c for c in state['calls'] if c[:2] in (['release', 'upload'], ['release', 'edit'])])
        with self.subTest(case='api error'):
            result, state, _ = self.run_case(api_error=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('HTTP 500', result.stdout + result.stderr)
            self.assertFalse(state['releases'])
        with self.subTest(case='draft for other commit'):
            draft = {**fresh, 'draft': True, 'target_commitish': 'f' * 40, 'assets': {}}
            result, state, _ = self.run_case([draft])
            self.assertNotEqual(result.returncode, 0)
            self.assertTrue(state['releases'][0]['draft'])
        for kind in CASES:
            for missing in (kind, kind + '-binary'):
                with self.subTest(case='platform missing', missing=missing):
                    result, state, _ = self.run_case(missing=missing)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(state['releases'])
            with self.subTest(case='platform identity mismatch', kind=kind):
                result, state, _ = self.run_case(corrupt=kind)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(state['releases'])
        with self.subTest(case='duplicate releases'):
            result, state, _ = self.run_case([fresh, {**fresh, 'id': 9}], SHA)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse([c for c in state['calls'] if c[:2] in (['release', 'create'], ['release', 'upload'], ['release', 'edit'])])


if __name__ == "__main__":
    unittest.main()
