// Package requestlimits implements bounded per-process sliding-minute budgets.
package requestlimits

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Limiter struct {
	mu      sync.Mutex
	entries map[string][]time.Time
}

func New() *Limiter { return &Limiter{entries: make(map[string][]time.Time)} }
func (l *Limiter) Allow(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	for k, values := range l.entries {
		if len(values) == 0 || !values[len(values)-1].After(cutoff) {
			delete(l.entries, k)
		}
	}
	values := l.entries[key]
	i := 0
	for i < len(values) && !values[i].After(cutoff) {
		i++
	}
	values = values[i:]
	if len(values) >= max {
		l.entries[key] = values
		return false
	}
	// Bound memory under a high-cardinality attack by failing closed.
	if len(l.entries) >= 100000 && len(values) == 0 {
		return false
	}
	l.entries[key] = append(values, now)
	return true
}
func LoginKey(login string) string {
	return fmt.Sprintf("login:%x", sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(login)))))
}

// ClientKey keeps bearer material out of counters without applying login-name
// normalization to a case-sensitive opaque credential.
func ClientKey(bearer string) string {
	return fmt.Sprintf("client:%x", sha256.Sum256([]byte(bearer)))
}
func IP(r *http.Request) string {
	address := r.RemoteAddr
	if peer, ok := r.Context().Value(transportPeerKey{}).(string); ok {
		host, _, err := net.SplitHostPort(peer)
		if err != nil {
			host = peer
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			address = peer
		}
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	return host
}

type transportPeerKey struct{}

func CaptureTransportPeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), transportPeerKey{}, r.RemoteAddr)))
	})
}
