import os
import tty

tty.setraw(0)
os.write(1, b'FID_READY\r\n')
assert os.read(0, 1) == b'g'
# Invert default and seeded states, including alternate selectors and focus.
os.write(1, b'\x1b[?9;1000;1001;1002;1003;1004;1005;1006;1007;1015;1016;2004;47;1047;1049h\x1b[?25l\x1b[?25;1000;1004;2004;1049sMODES_CHANGED\r\n')
while os.read(0, 4096):
    pass
