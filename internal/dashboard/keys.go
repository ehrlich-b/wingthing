package dashboard

import "unicode/utf8"

type keyDecoder struct{ pending []byte }

// feed retains incomplete escape sequences and UTF-8 across reads. Unsupported
// terminal sequences are consumed as a unit so their bytes cannot run actions.
func (d *keyDecoder) feed(input []byte) []string {
	d.pending = append(d.pending, input...)
	var keys []string
	for len(d.pending) > 0 {
		b := d.pending[0]
		if b == 0x1b {
			if len(d.pending) == 1 {
				break
			}
			if d.pending[1] != '[' && d.pending[1] != 'O' {
				keys = append(keys, "escape")
				d.pending = d.pending[1:]
				continue
			}
			end := 2
			for end < len(d.pending) && (d.pending[end] < 0x40 || d.pending[end] > 0x7e) {
				end++
			}
			if end == len(d.pending) {
				break
			}
			if end == 2 {
				if key := map[byte]string{'A': "up", 'B': "down", 'C': "right", 'D': "left"}[d.pending[end]]; key != "" {
					keys = append(keys, key)
				}
			}
			d.pending = d.pending[end+1:]
			continue
		}
		if !utf8.FullRune(d.pending) {
			break
		}
		r, size := utf8.DecodeRune(d.pending)
		d.pending = d.pending[size:]
		key := string(r)
		switch b {
		case 3:
			key = "ctrl-c"
		case 21:
			key = "ctrl-u"
		case 8, 127:
			key = "backspace"
		case '\r', '\n':
			key = "enter"
		}
		keys = append(keys, key)
	}
	return keys
}

func (d *keyDecoder) escape() []string {
	if len(d.pending) == 1 && d.pending[0] == 0x1b {
		d.pending = nil
		return []string{"escape"}
	}
	return nil
}
