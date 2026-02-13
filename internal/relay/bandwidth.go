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

// Tier rate/cap constants.
const (
	freeRate     = 125_000     // 1 Mbit/s
	proRate      = 375_000     // 3 Mbit/s
	freeMonthlyCap int64 = 1 << 30 // 1 GiB
	defaultBurst = 1 * 1024 * 1024 // 1 MB
)

// TierLookup returns a user's tier string ("free", "pro", "team").
type TierLookup func(userID string) string

// BandwidthMeter applies per-user rate limiting on relay traffic
// and periodically syncs usage to the DB.
type BandwidthMeter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	tiers    map[string]string // cached tier per user
	counters map[string]*atomic.Int64
	exceeded map[string]bool   // users who exceeded monthly cap (set by login via sync)
	month    string             // current month "2006-01" — counters reset on rollover
	rateVal  rate.Limit
	burst    int
	db       *sql.DB
	tierFn   TierLookup
}

// NewBandwidthMeter creates a meter with the given sustained rate (bytes/sec) and burst (bytes).
func NewBandwidthMeter(bytesPerSec int, burst int, db *sql.DB) *BandwidthMeter {
	return &BandwidthMeter{
		limiters: make(map[string]*rate.Limiter),
		tiers:    make(map[string]string),
		counters: make(map[string]*atomic.Int64),
		rateVal:  rate.Limit(bytesPerSec),
		burst:    burst,
		db:       db,
	}
}

// SetTierLookup sets the function used to look up user tiers for rate differentiation.
func (b *BandwidthMeter) SetTierLookup(fn TierLookup) {
	b.tierFn = fn
}

// SetExceeded replaces the set of users who exceeded their monthly cap (pushed from login via sync).
func (b *BandwidthMeter) SetExceeded(userIDs []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.exceeded = make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		b.exceeded[id] = true
	}
}

// IsExceeded returns true if a user has been flagged as over their monthly cap.
func (b *BandwidthMeter) IsExceeded(userID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded[userID]
}

// ExceededUsers returns user IDs that have exceeded their monthly bandwidth cap.
// Only meaningful on the login node where counters reflect cluster-wide totals.
func (b *BandwidthMeter) ExceededUsers() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var result []string
	for userID, c := range b.counters {
		if c.Load() < freeMonthlyCap {
			continue
		}
		tier := b.tiers[userID]
		if tier == "" && b.tierFn != nil {
			tier = b.tierFn(userID)
			b.tiers[userID] = tier
		}
		if tier == "" || tier == "free" {
			result = append(result, userID)
		}
	}
	return result
}

// Wait blocks until the user's rate limiter allows n bytes, or ctx is done.
// Rejects immediately if the user has exceeded their monthly bandwidth cap.
func (b *BandwidthMeter) Wait(ctx context.Context, userID string, n int) error {
	if b.IsExceeded(userID) || b.ExceededMonthly(userID) {
		return fmt.Errorf("bandwidth limit exceeded")
	}
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

// ExceededMonthly returns true if a free-tier user has exceeded their monthly cap.
// Always returns false for pro/team users.
func (b *BandwidthMeter) ExceededMonthly(userID string) bool {
	tier := b.userTier(userID)
	if tier != "" && tier != "free" {
		return false
	}
	return b.counter(userID).Load() >= freeMonthlyCap
}

// MonthlyUsage returns the user's current month bandwidth usage in bytes.
func (b *BandwidthMeter) MonthlyUsage(userID string) int64 {
	return b.counter(userID).Load()
}

func (b *BandwidthMeter) userTier(userID string) string {
	b.mu.Lock()
	tier, ok := b.tiers[userID]
	b.mu.Unlock()
	if ok {
		return tier
	}
	if b.tierFn != nil {
		tier = b.tierFn(userID)
		b.mu.Lock()
		b.tiers[userID] = tier
		b.mu.Unlock()
		return tier
	}
	return "free"
}

func (b *BandwidthMeter) InvalidateUser(userID string) {
	b.mu.Lock()
	delete(b.tiers, userID)
	delete(b.limiters, userID)
	b.mu.Unlock()
}

func (b *BandwidthMeter) limiter(userID string) *rate.Limiter {
	b.mu.Lock()
	defer b.mu.Unlock()
	lim, ok := b.limiters[userID]
	if !ok {
		// Tier-aware rate: pro/team get 3 Mbit/s, free gets 1 Mbit/s
		r := b.rateVal
		if b.tierFn != nil {
			tier := b.tiers[userID]
			if tier == "" {
				// lookup and cache
				tier = b.tierFn(userID)
				b.tiers[userID] = tier
			}
			if tier == "pro" || tier == "team" {
				r = rate.Limit(proRate)
			} else {
				r = rate.Limit(freeRate)
			}
		}
		lim = rate.NewLimiter(r, b.burst)
		b.limiters[userID] = lim
	}
	return lim
}

func (b *BandwidthMeter) counter(userID string) *atomic.Int64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Reset all counters on month rollover
	m := currentMonth()
	if b.month != m {
		b.month = m
		b.counters = make(map[string]*atomic.Int64)
	}

	c, ok := b.counters[userID]
	if !ok {
		c = &atomic.Int64{}
		b.counters[userID] = c
	}
	return c
}

// DrainCounters returns per-user byte counts accumulated since the last drain, then resets them.
// Used by edge nodes to report usage to login via the sync protocol.
func (b *BandwidthMeter) DrainCounters() map[string]int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.counters) == 0 {
		return nil
	}
	result := make(map[string]int64, len(b.counters))
	for userID, c := range b.counters {
		v := c.Swap(0)
		if v > 0 {
			result[userID] = v
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// AddUsage adds external usage bytes (reported by edge nodes) to the local counters.
func (b *BandwidthMeter) AddUsage(userID string, bytes int64) {
	b.counter(userID).Add(bytes)
}

// SeedFromDB loads current month's bandwidth totals from the DB into memory.
// Called on login startup to avoid resetting counters to zero.
func (b *BandwidthMeter) SeedFromDB() {
	if b.db == nil {
		return
	}
	month := currentMonth()
	rows, err := b.db.Query(
		`SELECT user_id, bytes_total FROM bandwidth_log WHERE month = ?`, month)
	if err != nil {
		log.Printf("bandwidth seed error: %v", err)
		return
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var userID string
		var total int64
		if err := rows.Scan(&userID, &total); err != nil {
			continue
		}
		b.counter(userID).Store(total)
		n++
	}
	if n > 0 {
		log.Printf("bandwidth: seeded %d users from DB for %s", n, month)
	}
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
