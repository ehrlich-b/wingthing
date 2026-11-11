package pake

import (
	"bytes"
	"testing"
)

func TestSPAKE2(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		a, am, e := New(true, "one two three four five six", "wing-id:pin:invitation")
		if e != nil {
			t.Fatal(e)
		}
		secret := "one two three four five six"
		if wrong {
			secret = "wrong password"
		}
		b, bm, e := New(false, secret, "wing-id:pin:invitation")
		if e != nil {
			t.Fatal(e)
		}
		ak, e := a.Finish(bm)
		if e != nil {
			t.Fatal(e)
		}
		bk, e := b.Finish(am)
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Equal(ak, bk) == wrong {
			t.Fatal("password binding failed")
		}
		if _, e = a.Finish(bm); e == nil {
			t.Fatal("context reused")
		}
	}
}
