package relay

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// BandwidthMeter applies per-user rate limiting on relay traffic
// and periodically syncs usage to the DB.
type BandwidthMeter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	counters map[string]*atomic.Int64
	rateVal  rate.Limit
	burst    int
	db       *sql.DB
}

// NewBandwidthMeter creates a meter with the given sustained rate (bytes/sec) and burst (bytes).
func NewBandwidthMeter(bytesPerSec int, burst int, db *sql.DB) *BandwidthMeter {
	return &BandwidthMeter{
		limiters: make(map[string]*rate.Limiter),
		counters: make(map[string]*atomic.Int64),
		rateVal:  rate.Limit(bytesPerSec),
		burst:    burst,
		db:       db,
	}
}

// Wait blocks until the user's rate limiter allows n bytes, or ctx is done.
func (b *BandwidthMeter) Wait(ctx context.Context, userID string, n int) error {
	b.counter(userID).Add(int64(n))
	lim := b.limiter(userID)
	if n <= b.burst {
		return lim.WaitN(ctx, n)
	}
	// Chunk large messages so WaitN doesn't reject (n > burst).
	for n > 0 {
		chunk := n
		if chunk > b.burst {
			chunk = b.burst
		}
		if err := lim.WaitN(ctx, chunk); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

func (b *BandwidthMeter) limiter(userID string) *rate.Limiter {
	b.mu.Lock()
	defer b.mu.Unlock()
	lim, ok := b.limiters[userID]
	if !ok {
		lim = rate.NewLimiter(b.rateVal, b.burst)
		b.limiters[userID] = lim
	}
	return lim
}

func (b *BandwidthMeter) counter(userID string) *atomic.Int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.counters[userID]
	if !ok {
		c = &atomic.Int64{}
		b.counters[userID] = c
	}
	return c
}

// StartSync syncs per-user bandwidth to the DB every interval. Only writes users with changes.
func (b *BandwidthMeter) StartSync(ctx context.Context, interval time.Duration) {
	go func() {
		last := make(map[string]int64)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.mu.Lock()
				snap := make(map[string]int64, len(b.counters))
				for k, v := range b.counters {
					snap[k] = v.Load()
				}
				b.mu.Unlock()

				month := currentMonth()
				for userID, cur := range snap {
					if cur == last[userID] {
						continue
					}
					last[userID] = cur
					log.Printf("bandwidth: user=%s total=%s", userID, humanBytes(cur))
					if b.db != nil {
						b.syncToDB(userID, month, cur)
					}
				}
			}
		}
	}()
}

func (b *BandwidthMeter) syncToDB(userID, month string, total int64) {
	_, err := b.db.Exec(
		`INSERT INTO bandwidth_log (user_id, month, bytes_total, updated_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(user_id, month) DO UPDATE SET bytes_total = ?, updated_at = datetime('now')`,
		userID, month, total, total,
	)
	if err != nil {
		log.Printf("bandwidth sync error: %v", err)
	}
}

func currentMonth() string {
	return time.Now().UTC().Format("2006-01")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// RateLimiter applies per-IP request rate limiting.
// "Friends and family" limits — just enough to prevent abuse.
type RateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*ipLimiter
	rate     rate.Limit
	burst    int
}

type ipLimiter struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter creates a per-IP rate limiter.
// reqPerSec is the sustained rate, burst is the max burst size.
func NewRateLimiter(reqPerSec float64, burst int) *RateLimiter {
	rl := &RateLimiter{
		limiters: make(map[string]*ipLimiter),
		rate:     rate.Limit(reqPerSec),
		burst:    burst,
	}
	// Evict stale entries every 5 minutes
	go func() {
		for range time.Tick(5 * time.Minute) {
			rl.mu.Lock()
			for ip, l := range rl.limiters {
				if time.Since(l.lastSeen) > 10*time.Minute {
					delete(rl.limiters, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	l, ok := rl.limiters[ip]
	if !ok {
		l = &ipLimiter{lim: rate.NewLimiter(rl.rate, rl.burst)}
		rl.limiters[ip] = l
	}
	l.lastSeen = time.Now()
	return l.lim
}

// Allow returns true if the request is within rate limits for the given IP.
func (rl *RateLimiter) Allow(ip string) bool {
	return rl.getLimiter(ip).Allow()
}

// Middleware wraps an http.Handler with rate limiting.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !rl.Allow(ip) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	// Check X-Forwarded-For (fly.io, cloudflare, etc.)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First IP is the client
		if i := len(xff); i > 0 {
			parts := xff
			for j := 0; j < len(parts); j++ {
				if parts[j] == ',' {
					return parts[:j]
				}
			}
			return parts
		}
	}
	// Fall back to RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
