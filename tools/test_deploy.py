import os
import ast
import base64
import re
from pathlib import Path
import shutil
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'deploy.sh'
BASH = str(Path(os.environ.get('ProgramFiles', 'C:/Program Files')) / 'Git' / 'bin' / 'bash.exe') if os.name == 'nt' else shutil.which('bash')


class StandaloneInstallerTests(unittest.TestCase):
    def test_bash_syntax(self):
        subprocess.run([BASH, '-n'], input=SCRIPT.read_bytes(), check=True)

    def test_help_and_invalid_input_do_not_install(self):
        for args, expected in [(['--help'], 0), (['--protocol', 'xhpt'], 1), (['--name', 'bad;name'], 1), (['--protocol'], 1)]:
            result = subprocess.run([BASH, '-s', '--', *args], input=SCRIPT.read_bytes(), capture_output=True)
            self.assertEqual(result.returncode, expected, result.stderr)
        self.assertIn(b'RayBox:', subprocess.run([BASH, '-s', '--', '--help'], input=SCRIPT.read_bytes(), capture_output=True).stdout)

    def test_multi_protocol_payload_is_embedded_and_valid_python(self):
        script = SCRIPT.read_text(encoding='utf-8')
        encoded = re.search(r"printf '%s' '([A-Za-z0-9+/=]+)' \| base64 -d > \"\$multi_work/install.py\"", script)
        self.assertIsNotNone(encoded)
        source = base64.b64decode(encoded[1]).decode()
        ast.parse(source)
        for protocol in 'vlpt xhpt vxpt vwpt xupt xcpt tupt anpt nvpt arpt sspt hypt vmpt sopt'.split():
            self.assertIn("'" + protocol + "'", source)
        self.assertIn("'with_quic,with_utls,with_reality,with_acme'", source)
        self.assertIn("'refs/tags/' + tag", source)
        self.assertIn('FETCH_HEAD^{commit}', source)
        self.assertIn("constant.Version=' + manifest['version']", source)
        self.assertIn('节点已安装', source)

    def test_public_core_and_independent_runtime(self):
        script = SCRIPT.read_text(encoding='utf-8')
        self.assertNotIn('STANDALONE_', script)
        self.assertNotIn('node.5945946.xyz', script)
        self.assertNotIn('dispatch-api.', script)
        self.assertIn('manifest_asset=server-manifest.json', script)
        self.assertIn('/releases?per_page=100&page=', script)
        self.assertNotRegex(script, r'/releases/download/(?:server-)?v[0-9]+\.')
        self.assertEqual(script.count('bash "$work/install.sh" --core-file "$core_asset"'), 4)
        self.assertEqual(script.count("jq -er '.share_link' \"$result\""), 8)


if __name__ == '__main__':
    unittest.main()
