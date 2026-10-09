//go:build linux

package procinfo

import (
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
)

func TestLinuxProcessStartIgnoresCommandDelimiters(t *testing.T) {
	stat := "42 (command ) with spaces) S " + strings.Repeat("0 ", 18) + "1234 0"
	if start, err := linuxProcessStart(stat); err != nil || start != "1234" {
		t.Fatalf("stat start time: %q %v", start, err)
	}
	for _, stat := range []string{"invalid", "42 (command) S 0", "42 (zombie) Z " + strings.Repeat("0 ", 20)} {
		if _, err := linuxProcessStart(stat); err == nil {
			t.Fatal("invalid or zombie process stat accepted")
		}
	}
}

func TestLinuxClockTicksRequiresValidAuxiliaryEntry(t *testing.T) {
	word := strconv.IntSize / 8
	entry := func(key, value uint64) []byte {
		data := make([]byte, 2*word)
		if word == 4 {
			binary.NativeEndian.PutUint32(data, uint32(key))
			binary.NativeEndian.PutUint32(data[word:], uint32(value))
		} else {
			binary.NativeEndian.PutUint64(data, key)
			binary.NativeEndian.PutUint64(data[word:], value)
		}
		return data
	}
	for _, rate := range []uint64{100, 1024} {
		auxv := append(entry(6, 4096), entry(17, rate)...)
		if got, err := linuxClockTicks(auxv); err != nil || got != int64(rate) {
			t.Fatalf("clock tick rate = %d, %v; want %d", got, err, rate)
		}
	}
	for _, auxv := range [][]byte{nil, {17}, entry(6, 4096), entry(17, 0), entry(17, 1<<32-1), append(entry(0, 0), entry(17, 100)...)} {
		if _, err := linuxClockTicks(auxv); err == nil {
			t.Fatalf("invalid auxiliary vector accepted: %v", auxv)
		}
	}
}
