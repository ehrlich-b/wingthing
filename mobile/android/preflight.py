#!/usr/bin/env python3
"""Check the Android debug build prerequisites without starting a build."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import xml.etree.ElementTree as ET

MOBILE = 'v0.0.0-20261011034528-197770444867'

def main():
    sdk = Path(os.environ['ANDROID_HOME'])
    ndk = Path(os.environ['ANDROID_NDK_HOME'])
    errors = []
    def require(path, description):
        if not path.is_file():
            errors.append(f'{description}: missing {path}')
    require(sdk / 'platforms/android-37.0/android.jar', 'Android SDK platform 37 (install platforms;android-37.0)')
    require(sdk / 'build-tools/37.0.0/aapt2', 'Android build tools 37.0.0 (install build-tools;37.0.0)')
    def package_metadata(directory, expected_path, version_element, expected_version, description):
        metadata = directory / 'package.xml'
        try:
            package = ET.parse(metadata).getroot().find('localPackage')
            if package is None or package.get('path') != expected_path:
                raise ValueError('wrong package path')
            if version_element == 'api-level':
                actual = package.findtext('type-details/api-level')
            else:
                revision = package.find('revision')
                actual = '.'.join(revision.findtext(part, '0') for part in ('major', 'minor', 'micro')) if revision is not None else None
            if actual != expected_version:
                raise ValueError(f'found version {actual or "missing"}')
        except (OSError, ET.ParseError, ValueError) as exc:
            errors.append(f'{description} package metadata at {metadata}: {exc}; install {expected_path}')
    package_metadata(sdk / 'platforms/android-37.0', 'platforms;android-37.0', 'api-level', '37.0', 'Android SDK platform 37')
    package_metadata(sdk / 'build-tools/37.0.0', 'build-tools;37.0.0', 'revision', '37.0.0', 'Android build tools 37.0.0')
    props = ndk / 'source.properties'
    try:
        version = next((line.split('=', 1)[1].strip() for line in props.read_text().splitlines() if line.startswith('Pkg.Revision')), '')
    except OSError:
        version = ''
    if version != '29.0.14206865':
        errors.append(f'Android NDK 29.0.14206865 required at {ndk}; found {version or "missing"} (install ndk;29.0.14206865)')
    require(ndk / 'toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib/aarch64-linux-android/libc++_shared.so', 'NDK arm64 C++ runtime')
    require(ndk / 'toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib/x86_64-linux-android/libc++_shared.so', 'NDK x86_64 C++ runtime')
    java = Path(os.environ['JAVA_HOME']) / 'bin/java'
    try:
        result = subprocess.run([str(java), '-version'], capture_output=True, text=True, check=True)
        if 'version "21.' not in result.stderr + result.stdout:
            raise ValueError('expected JDK 21')
    except (OSError, subprocess.CalledProcessError, ValueError):
        errors.append(f'JDK 21 required at {java}; set JAVA_HOME to a JDK 21 installation')
    javac = Path(os.environ['JAVA_HOME']) / 'bin/javac'
    try:
        result = subprocess.run([str(javac), '-version'], capture_output=True, text=True, check=True)
        if not (result.stdout + result.stderr).strip().startswith('javac 21.'):
            raise ValueError('expected javac 21')
    except (OSError, subprocess.CalledProcessError, ValueError):
        errors.append(f'JDK 21 compiler required at {javac}; set JAVA_HOME to a full JDK 21 installation')
    for name in ('gomobile', 'gobind'):
        binary = shutil.which(name)
        try:
            result = subprocess.run(['go', 'version', '-m', binary or ''], capture_output=True, text=True, check=True)
            if not any(line.split()[:4] == ['mod', 'golang.org/x/mobile', MOBILE, 'h1:rav73cdd7OQUPgt1aIFbZQTiPNnjfueKXvfOiLCY0Ss='] for line in result.stdout.splitlines()) or not any(line.split() == ['path', f'golang.org/x/mobile/cmd/{name}'] for line in result.stdout.splitlines()):
                raise ValueError('wrong module version')
        except (OSError, subprocess.CalledProcessError, ValueError):
            errors.append(f'{name} {MOBILE} required (found {binary or "missing"}); run mobile/android/bootstrap-gomobile.sh')
    if errors:
        print('Android preflight failed:\n  ' + '\n  '.join(errors), file=sys.stderr)
        return 1
    print('Android preflight passed')
    return 0

if __name__ == '__main__':
    sys.exit(main())
