import os
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

    def test_public_core_and_independent_runtime(self):
        script = SCRIPT.read_text(encoding='utf-8')
        self.assertNotIn('STANDALONE_', script)
        self.assertNotIn('node.5945946.xyz', script)
        self.assertNotIn('dispatch-api.', script)
        self.assertIn('releases/download/server-v0.0.1/raybox-linux-$arch', script)
        self.assertIn('399067a9d6b6669681d52777ee7c80be0e425ce8ae1d3c66b78e8bba4b156cd4', script)
        self.assertEqual(script.count('bash "$work/install.sh" --core-file "$RAYBOX_CORE_FILE"'), 4)
        self.assertEqual(script.count("jq -er '.share_link' \"$result\""), 8)


if __name__ == '__main__':
    unittest.main()
