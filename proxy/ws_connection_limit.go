package proxy

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/codex2api/api"
)

const (
	defaultWSMaxConnectionsPerKey = 64
	defaultWSMaxConnections       = 4096
)

// wsConnectionLimiter bounds downstream WebSocket connections per API key and
// globally. Connections are cheap to open but expensive to keep: each one holds
// a goroutine, file descriptor and read buffer for as long as the client stays
// silent.
type wsConnectionLimiter struct {
	mu        sync.Mutex
	maxTotal  int
	maxPerKey int
	total     int
	perKey    map[int64]int
}

func newWSConnectionLimiter(maxTotal, maxPerKey int) *wsConnectionLimiter {
	if maxTotal <= 0 {
		maxTotal = defaultWSMaxConnections
	}
	if maxPerKey <= 0 {
		maxPerKey = defaultWSMaxConnectionsPerKey
	}
	return &wsConnectionLimiter{
		maxTotal:  maxTotal,
		maxPerKey: maxPerKey,
		perKey:    make(map[int64]int),
	}
}

var globalWSConnectionLimiter = newWSConnectionLimiter(defaultWSMaxConnections, defaultWSMaxConnectionsPerKey)

// acquire registers one connection. The returned release is idempotent.
func (l *wsConnectionLimiter) acquire(keyID int64) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.maxTotal {
		return nil, false
	}
	if keyID > 0 && l.perKey[keyID] >= l.maxPerKey {
		return nil, false
	}
	l.total++
	if keyID > 0 {
		l.perKey[keyID]++
	}
	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if released {
			return
		}
		released = true
		l.total--
		if keyID > 0 {
			l.perKey[keyID]--
			if l.perKey[keyID] <= 0 {
				delete(l.perKey, keyID)
			}
		}
	}, true
}

// acquireDownstreamWebSocket reserves a slot before the HTTP connection is
// hijacked. On rejection the caller must not upgrade.
func (h *Handler) acquireDownstreamWebSocket(c *gin.Context) (func(), bool) {
	release, ok := globalWSConnectionLimiter.acquire(requestAPIKeyID(c))
	if !ok {
		c.Header("Retry-After", "5")
		api.SendErrorWithStatus(c, api.NewAPIError(
			api.ErrCodeServiceUnavailable,
			"too many concurrent WebSocket connections",
			api.ErrorTypeServer,
		), http.StatusServiceUnavailable)
		return nil, false
	}
	return release, true
}
