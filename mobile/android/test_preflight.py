import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
VERSION = 'v0.0.0-20261011034528-197770444867'

class PreflightTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.sdk = self.root / 'sdk'
        self.ndk = self.sdk / 'ndk/29.0.14206865'
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.env = dict(os.environ, ANDROID_HOME=str(self.sdk), ANDROID_NDK_HOME=str(self.ndk), JAVA_HOME=str(self.root / 'jdk'), PATH=str(self.bin) + ':/usr/bin:/bin')

    def file(self, path, content='', executable=False):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        if executable:
            path.chmod(0o755)

    def valid(self, metadata=True):
        self.file(self.sdk / 'platforms/android-37.0/android.jar')
        if metadata:
            self.file(self.sdk / 'platforms/android-37.0/package.xml', '<repository><localPackage path="platforms;android-37.0"><type-details><api-level>37.0</api-level></type-details></localPackage></repository>')
        self.file(self.sdk / 'build-tools/37.0.0/aapt2')
        self.file(self.sdk / 'build-tools/37.0.0/package.xml', '<repository><localPackage path="build-tools;37.0.0"><revision><major>37</major><minor>0</minor><micro>0</micro></revision></localPackage></repository>')
        self.file(self.ndk / 'source.properties', 'Pkg.Revision = 29.0.14206865\n')
        for triple in ('aarch64-linux-android', 'x86_64-linux-android'):
            self.file(self.ndk / 'toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib' / triple / 'libc++_shared.so')
        self.file(self.root / 'jdk/bin/java', '#!/bin/sh\necho \'openjdk version "21.0.1"\' >&2\n', True)
        self.file(self.root / 'jdk/bin/javac', '#!/bin/sh\necho javac 21.0.1 >&2\n', True)
        self.file(self.bin / 'go', f'#!/bin/sh\nprintf "path golang.org/x/mobile/cmd/%s\\nmod golang.org/x/mobile {VERSION} h1:rav73cdd7OQUPgt1aIFbZQTiPNnjfueKXvfOiLCY0Ss=\\n" "${{3##*/}}"\n', True)
        for name in ('gomobile', 'gobind'):
            self.file(self.bin / name, '#!/bin/sh\n', True)

    def run_check(self):
        return subprocess.run(['python3', str(HERE / 'preflight.py')], env=self.env, text=True, capture_output=True)

    def test_missing_reports_all_components(self):
        result = self.run_check()
        self.assertNotEqual(result.returncode, 0)
        for name in ('platform 37', 'build tools 37.0.0', 'NDK 29.0.14206865', 'JDK 21', 'gomobile', 'gobind'):
            self.assertIn(name, result.stderr)

    def test_missing_platform_metadata(self):
        self.valid(metadata=False)
        result = self.run_check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('package metadata', result.stderr)

    def test_incompatible_sdk_metadata(self):
        self.valid()
        self.file(self.sdk / 'platforms/android-37.0/package.xml', '<repository><localPackage path="platforms;android-37.0"><type-details><api-level>36</api-level></type-details></localPackage></repository>')
        self.file(self.sdk / 'build-tools/37.0.0/package.xml', '<repository><localPackage path="build-tools;37.0.0"><revision><major>35</major></revision></localPackage></repository>')
        result = self.run_check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('platform 37 package metadata', result.stderr)
        self.assertIn('build tools 37.0.0 package metadata', result.stderr)

    def test_missing_and_incompatible_javac(self):
        self.valid()
        javac = self.root / 'jdk/bin/javac'
        javac.unlink()
        self.assertIn('JDK 21 compiler', self.run_check().stderr)
        self.file(javac, '#!/bin/sh\necho javac 17.0.1 >&2\n', True)
        self.assertIn('JDK 21 compiler', self.run_check().stderr)

    def test_valid_and_incompatible_versions(self):
        self.valid()
        self.assertEqual(self.run_check().returncode, 0)
        self.file(self.ndk / 'source.properties', 'Pkg.Revision = 28.0.0\n')
        self.file(self.root / 'jdk/bin/java', '#!/bin/sh\necho \'openjdk version "17.0.1"\' >&2\n', True)
        self.file(self.bin / 'go', '#!/bin/sh\necho "mod golang.org/x/mobile wrong"\n', True)
        result = self.run_check()
        self.assertNotEqual(result.returncode, 0)
        for name in ('found 28.0.0', 'JDK 21', 'gomobile', 'gobind'):
            self.assertIn(name, result.stderr)

if __name__ == '__main__':
    unittest.main()
