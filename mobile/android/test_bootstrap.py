"""Exercise the offline bootstrap with a populated Go module download cache."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
VERSION = 'v0.0.0-20261011034528-197770444867'


class BootstrapTest(unittest.TestCase):
    def test_offline_install_from_download_cache(self):
        self.assertTrue(os.environ.get('GOMODCACHE'), 'set GOMODCACHE to a populated module cache')
        with tempfile.TemporaryDirectory() as directory:
            env = dict(os.environ, WT_ANDROID_WORKDIR=directory, GOPROXY='off', GOSUMDB='off')
            result = subprocess.run([str(HERE / 'bootstrap-gomobile.sh')], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            for name in ('gomobile', 'gobind'):
                binary = Path(directory) / 'bin' / name
                self.assertTrue(binary.is_file(), str(binary))
                info = subprocess.run(['go', 'version', '-m', str(binary)], env=env, text=True, capture_output=True, check=True)
                self.assertIn(f'golang.org/x/mobile {VERSION}', ' '.join(info.stdout.split()))


if __name__ == '__main__':
    unittest.main()
