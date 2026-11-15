#!/usr/bin/env python3
"""Copy compact captures from an explicit directory to an explicit output directory."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil

MODES = ['states-final', 'launch-final', 'logical-final', 'native-final', 'stability-final']


def retain(source, output, mode):
    source, output = source.resolve(), output.resolve()
    if mode not in MODES:
        raise ValueError('Unknown session capture mode: ' + mode)
    if source == output or source.is_relative_to(output) or output.is_relative_to(source):
        raise ValueError('source and output must be disjoint')
    # Validate all selected inputs before copying any of them.
    files = []
    for file in sorted(source.iterdir()):
        keep = file.suffix in {'.json', '.jsonl', '.jpg'}
        if mode == 'native-final' and file.suffix == '.jpg':
            keep = file.name.startswith(('claude-control-', 'codex-control-'))
        if not keep:
            continue
        if file.is_symlink():
            raise ValueError('escaping or symlink capture input: ' + file.name)
        if file.is_file():
            target = output / file.name
            if target.exists() or target.is_symlink():
                raise ValueError('output already exists: ' + str(target))
            files.append(file)
    output.mkdir(parents=True, exist_ok=True)
    retained = []
    for file in files:
        shutil.copyfile(file, output / file.name)
        retained.append({'path': file.name, 'bytes': file.stat().st_size,
                         'sha256': hashlib.sha256(file.read_bytes()).hexdigest()})
    print(json.dumps({'mode': mode, 'inputs': retained,
                      'bytes': sum(item['bytes'] for item in retained)}, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--mode', required=True, choices=MODES)
    args = parser.parse_args()
    retain(args.source, args.output, args.mode)


if __name__ == '__main__':
    main()
