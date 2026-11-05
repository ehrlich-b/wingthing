import os
import sys
import tty
import signal
import fcntl
import struct
import termios

tty.setraw(0, termios.TCSANOW)
if '--ssh' in sys.argv:
    def resized(*unused):
        rows, cols, _, _ = struct.unpack('HHHH', fcntl.ioctl(0, termios.TIOCGWINSZ, b'\0' * 8))
        os.write(1, ('SIZE:%dx%d\r\n' % (cols, rows)).encode())
    signal.signal(signal.SIGWINCH, resized)

os.write(1, b'FID_READY\r\n')
assert os.read(0, 1) == b'g'
modes = b'1;9;25;47;1047;1049;1000;1001;1002;1003;1004;1005;1006;1007;1015;1016;2004;2026'
enable = b'\x1b[?1;9;1000;1001;1002;1003;1004;1005;1006;1007;1015;1016;2004;2026;47;1047;1049h\x1b[?25l'
if '--empty-params' in sys.argv:
    # Omitted CSI parameters default to zero, wherever they occur in the list.
    modes = b';' + modes.replace(b';', b';;') + b';'
    enable = enable.replace(b'h', b';h').replace(b'25l', b';25;;l')
if '--ignored-del' in sys.argv:
    # Ignore DEL at CSI entry, inside numbers, around separators and before finals.
    modes = b'\x7f' + b'\x7f'.join(bytes([byte]) for byte in modes) + b'\x7f'
    enable = enable.replace(b'[?', b'[\x7f?\x7f').replace(b';', b'\x7f;\x7f').replace(b'h', b'\x7fh').replace(b'l', b'\x7fl')
if '--exercise-saves' in sys.argv:
    # Save an unknown original, modify it, then restore that provider snapshot.
    os.write(1, b'\x1b[?' + modes + b'sORIGINAL_SAVED\r\n')
    assert os.read(0, 1) == b's'
    os.write(1, enable + b'\x1b[?' + modes + b'rORIGINAL_RESTORED\r\n')
    assert os.read(0, 1) == b's'
    # Overwrite the provider save, reset every mode, then restore the new save.
    os.write(1, enable + b'\x1b[?' + modes + b's\x1b[?' + modes + b'l\x1b[?25h\x1b[?' + modes + b'r')
    if '--empty-params' in sys.argv:
        # A trailing empty parameter must not expose the outer fallback slots.
        os.write(1, b'\x1b[?25;1000;1004;2004;s')
    if '--ignored-del' in sys.argv:
        # Even this otherwise ordinary save must leave the guardian slots intact.
        os.write(1, b'\x1b[?25;1000;1004;2004\x7fs')
    os.write(1, b'MODES_CHANGED\r\n')
else:
    os.write(1, enable + b'\x1b[?25;1000;1004;2004;1049sMODES_CHANGED\r\n')
while True:
    data = os.read(0, 4096)
    if not data or '--ssh' in sys.argv and b'q' in data:
        break
