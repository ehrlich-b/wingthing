"""Recording provider: identical program and cases for direct and attached PTYs."""
import hashlib
import json
import os
import signal
import struct
import sys
import termios
import fcntl
import tty

tty.setraw(0)
record_path, cases_path = sys.argv[1:3]
with open(cases_path) as source:
    cases = json.load(source)


def emit(data):
    data = data.encode() if isinstance(data, str) else data
    while data:
        data = data[os.write(1, data):]


def read_exact(n):
    result = b''
    while len(result) < n:
        part = os.read(0, n - len(result))
        if not part:
            raise RuntimeError('input closed')
        result += part
    return result


def size():
    rows, cols, _, _ = struct.unpack('HHHH', fcntl.ioctl(0, termios.TIOCGWINSZ, b'\0' * 8))
    return '%dx%d' % (cols, rows)


def resized(*unused):
    emit('SIZE:' + size() + '\r\n')


signal.signal(signal.SIGWINCH, resized)
emit('FID_READY\r\n')
assert read_exact(1) == b'g'
records = []
for case in cases:
    name = case['name']
    emit('BEGIN:' + name + '\r\n')
    emit(case['output'])
    emit('WIRE:' + name + '\r\n')
    expected = case['input'].encode()
    if name == 'large-paste':
        payload = (b'wide-\xe7\x95\x8c\x02q' * 131072)[:1 << 20]
        expected = b'\x1b[200~' + payload + b'\x1b[201~'
    actual = read_exact(len(expected))
    record = dict(name=name, length=len(actual), sha256=hashlib.sha256(actual).hexdigest())
    if len(actual) < 256:
        record['hex'] = actual.hex()
    if name == 'sigwinch':
        record['size'] = size()
        assert record['size'] == '93x31', record
    records.append(record)
    with open(record_path, 'w') as target:
        json.dump(records, target, indent=2)
    assert actual == expected, (name, actual[:80], expected[:80])
    emit('PASS:' + name + '\r\n')
emit('FID_DONE\r\n')
# Keep the provider alive so detach is distinguished from provider exit.
assert read_exact(1) == b'q'
