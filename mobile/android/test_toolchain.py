import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent

class ToolchainTest(unittest.TestCase):
    def test_sdk_selection(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bindir = root / 'bin'
            bindir.mkdir()
            hostname = bindir / 'hostname'
            hostname.write_text('#!/bin/sh\necho forge2-vm\n')
            hostname.chmod(0o755)
            env = {'PATH': str(bindir) + ':/usr/bin:/bin', 'WT_ANDROID_WORKDIR': str(root / 'local')}
            def source(extra):
                result = subprocess.check_output(['bash', '-eu', '-c', 'source "$1"; printf "%s\\n%s\\n%s\\n" "$ANDROID_HOME" "$JAVA_HOME" "$GRADLE_USER_HOME"', '_', str(HERE / 'toolchain.sh')], env={**env, **extra}, text=True)
                return result.splitlines()
            self.assertEqual(source({}), ['/work/cache/android/sdk', '/work/cache/android/sdk/jdk-21.0.13+9', '/work/cache/android/gradle-home'])
            self.assertEqual(source({'WINGTHING_ANDROID_HOME': '/custom'}), ['/custom/sdk', '/custom/sdk/jdk-21.0.13+9', '/custom/gradle-home'])
            self.assertEqual(source({'ANDROID_HOME': '/explicit'}), ['/explicit', '/explicit/jdk-21.0.12.1+1', str(root / 'local/gradle-home')])
            (root / 'local/sdk').mkdir(parents=True)
            self.assertEqual(source({'WINGTHING_ANDROID_HOME': '/custom'}), [str(root / 'local/sdk'), str(root / 'local/sdk/jdk-21.0.12.1+1'), str(root / 'local/gradle-home')])
            hostname.write_text('#!/bin/sh\necho other-forge2-vm\n')
            (root / 'local/sdk').rmdir()
            self.assertEqual(source({})[0], str(root / 'local/sdk'))

if __name__ == '__main__':
    unittest.main()
