package middleware

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type rateLimitBucket struct {
	count     int
	resetTime time.Time
}

type RateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]rateLimitBucket
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]rateLimitBucket),
	}
}

func (l *RateLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(r) {
			w.Header().Set("Retry-After", secondsUntil(l.window))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (l *RateLimiter) Allow(r *http.Request) bool {
	if l == nil || l.limit <= 0 || l.window <= 0 {
		return true
	}

	now := time.Now()
	key := rateLimitKey(r)

	l.mu.Lock()
	defer l.mu.Unlock()

	for bucketKey, bucket := range l.buckets {
		if !now.Before(bucket.resetTime) {
			delete(l.buckets, bucketKey)
		}
	}

	bucket := l.buckets[key]
	if bucket.resetTime.IsZero() || !now.Before(bucket.resetTime) {
		bucket = rateLimitBucket{resetTime: now.Add(l.window)}
	}
	if bucket.count >= l.limit {
		l.buckets[key] = bucket
		return false
	}

	bucket.count++
	l.buckets[key] = bucket
	return true
}

func rateLimitKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		host = r.RemoteAddr
	}
	return host + " " + r.URL.Path
}

func secondsUntil(d time.Duration) string {
	seconds := int(d.Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}
