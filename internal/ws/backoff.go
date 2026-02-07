package ws

import "time"

type Backoff struct {
	Base    time.Duration
	Max     time.Duration
	attempt int
}

func NewBackoff(base, max time.Duration) *Backoff {
	return &Backoff{Base: base, Max: max}
}

func (b *Backoff) Next() time.Duration {
	d := b.Base << b.attempt
	if d > b.Max {
		d = b.Max
	}
	b.attempt++
	return d
}

func (b *Backoff) Reset() {
	b.attempt = 0
}
