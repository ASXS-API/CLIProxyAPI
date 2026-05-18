package helps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	proxyPoolMaxTransports         = 128
	proxyPoolMaxActivePerTransport = 8
	proxyPoolIdleTTL               = 10 * time.Minute
	proxyPoolIdleSweepInterval     = time.Minute

	proxyReuseReasonAuthProxy   = "auth_proxy"
	proxyReuseReasonNoProxy     = "no_proxy"
	proxyReuseReasonPoolFull    = "pool_full"
	proxyReuseReasonBuildFailed = "build_failed"
)

var (
	errProxyPoolFull    = errors.New("proxy transport pool full")
	errProxyBuildFailed = errors.New("proxy transport build failed")

	globalProxyTransportPools = &proxyTransportPoolRegistry{pools: make(map[string]*proxyTransportPool)}
	globalProxyTransportID    atomic.Uint64
	globalProxyStreamID       atomic.Uint64
)

type proxyTransportPoolRegistry struct {
	mu          sync.Mutex
	janitorOnce sync.Once
	pools       map[string]*proxyTransportPool
}

func (r *proxyTransportPoolRegistry) pool(proxyURL string) *proxyTransportPool {
	r.janitorOnce.Do(func() {
		go r.runIdleJanitor()
	})

	r.mu.Lock()
	defer r.mu.Unlock()

	pool := r.pools[proxyURL]
	if pool == nil {
		pool = &proxyTransportPool{proxyURL: proxyURL}
		r.pools[proxyURL] = pool
	}
	return pool
}

func (r *proxyTransportPoolRegistry) runIdleJanitor() {
	ticker := time.NewTicker(proxyPoolIdleSweepInterval)
	defer ticker.Stop()

	for range ticker.C {
		r.evictIdleTransports(time.Now(), proxyPoolIdleTTL)
	}
}

func (r *proxyTransportPoolRegistry) evictIdleTransports(now time.Time, idleFor time.Duration) int {
	r.mu.Lock()
	pools := make([]*proxyTransportPool, 0, len(r.pools))
	for _, pool := range r.pools {
		pools = append(pools, pool)
	}
	r.mu.Unlock()

	evicted := 0
	for _, pool := range pools {
		evicted += pool.evictIdle(now, idleFor)
	}
	return evicted
}

type proxyTransportPool struct {
	mu         sync.Mutex
	proxyURL   string
	transports []*pooledProxyTransport
}

type pooledProxyTransport struct {
	id        uint64
	transport http.RoundTripper
	active    int
	lastUsed  time.Time
}

type proxyTransportLease struct {
	transport      *pooledProxyTransport
	streamID       uint64
	poolSize       int
	acquiredActive int
	acquiredAt     time.Time
	released       atomic.Bool
	pool           *proxyTransportPool
}

func (p *proxyTransportPool) acquire() (*proxyTransportLease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, transport := range p.transports {
		if transport.active < proxyPoolMaxActivePerTransport {
			return p.acquireLocked(transport), nil
		}
	}

	if len(p.transports) >= proxyPoolMaxTransports {
		return nil, errProxyPoolFull
	}

	transport := buildProxyTransport(p.proxyURL)
	if transport == nil {
		return nil, errProxyBuildFailed
	}

	pooled := &pooledProxyTransport{
		id:        globalProxyTransportID.Add(1),
		transport: transport,
		lastUsed:  time.Now(),
	}
	p.transports = append(p.transports, pooled)
	return p.acquireLocked(pooled), nil
}

func (p *proxyTransportPool) acquireLocked(transport *pooledProxyTransport) *proxyTransportLease {
	transport.active++
	transport.lastUsed = time.Now()
	return &proxyTransportLease{
		transport:      transport,
		streamID:       globalProxyStreamID.Add(1),
		poolSize:       len(p.transports),
		acquiredActive: transport.active,
		acquiredAt:     time.Now(),
		pool:           p,
	}
}

func (p *proxyTransportPool) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.transports)
}

func (l *proxyTransportLease) release(ctx context.Context, status string, err error) {
	if l == nil || !l.released.CompareAndSwap(false, true) {
		return
	}

	l.pool.mu.Lock()
	l.transport.active--
	active := l.transport.active
	l.transport.lastUsed = time.Now()
	l.pool.mu.Unlock()

	entry := LogWithRequestID(ctx)
	if err != nil {
		entry = entry.WithError(err)
	}
	entry.Debugf(
		"[REUSE RELEASE tid=%d sid=%d duration=%s active=%d/%d status=%s]",
		l.transport.id,
		l.streamID,
		time.Since(l.acquiredAt).Truncate(time.Millisecond),
		active,
		proxyPoolMaxActivePerTransport,
		status,
	)
}

func (p *proxyTransportPool) evictIdle(now time.Time, idleFor time.Duration) int {
	p.mu.Lock()
	kept := p.transports[:0]
	evicted := make([]http.RoundTripper, 0)
	for _, transport := range p.transports {
		if transport.active == 0 && now.Sub(transport.lastUsed) >= idleFor {
			evicted = append(evicted, transport.transport)
			continue
		}
		kept = append(kept, transport)
	}
	p.transports = kept
	p.mu.Unlock()

	for _, transport := range evicted {
		closeIdleConnections(transport)
	}
	return len(evicted)
}

func closeIdleConnections(transport http.RoundTripper) {
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type proxyPoolRoundTripper struct {
	ctx        context.Context
	proxyURL   string
	provider   string
	fallbackRT http.RoundTripper
	pool       *proxyTransportPool
}

func newProxyPoolRoundTripper(ctx context.Context, proxyURL, provider string, fallbackRT http.RoundTripper) http.RoundTripper {
	return &proxyPoolRoundTripper{
		ctx:        ctx,
		proxyURL:   proxyURL,
		provider:   provider,
		fallbackRT: fallbackRT,
		pool:       globalProxyTransportPools.pool(proxyURL),
	}
}

func (rt *proxyPoolRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	lease, errAcquire := rt.pool.acquire()
	if errAcquire != nil {
		reason := proxyReuseReasonBuildFailed
		var base http.RoundTripper
		poolSize := rt.pool.size()
		if errors.Is(errAcquire, errProxyPoolFull) {
			reason = proxyReuseReasonPoolFull
			base = buildProxyTransport(rt.proxyURL)
		}
		if base == nil {
			base = rt.fallbackRT
		}
		return roundTripInactive(rt.ctx, req, base, rt.provider, reason, poolSize)
	}

	resp, errRoundTrip := lease.transport.transport.RoundTrip(req)
	logProxyReuseActive(rt.ctx, req, resp, rt.provider, lease)
	if errRoundTrip != nil {
		lease.release(rt.ctx, "error", errRoundTrip)
		return resp, errRoundTrip
	}
	if resp == nil || resp.Body == nil {
		lease.release(rt.ctx, "close", nil)
		return resp, nil
	}
	resp.Body = &proxyReuseTrackedBody{ctx: rt.ctx, body: resp.Body, lease: lease}
	return resp, nil
}

type inactiveProxyReuseRoundTripper struct {
	ctx      context.Context
	base     http.RoundTripper
	provider string
	reason   string
}

func newInactiveProxyReuseRoundTripper(ctx context.Context, base http.RoundTripper, provider, reason string) http.RoundTripper {
	return &inactiveProxyReuseRoundTripper{ctx: ctx, base: base, provider: provider, reason: reason}
}

func (rt *inactiveProxyReuseRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return roundTripInactive(rt.ctx, req, rt.base, rt.provider, rt.reason, 0)
}

func roundTripInactive(ctx context.Context, req *http.Request, base http.RoundTripper, provider, reason string, poolSize int) (*http.Response, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	resp, errRoundTrip := base.RoundTrip(req)
	logProxyReuseInactive(ctx, req, resp, provider, reason, poolSize)
	return resp, errRoundTrip
}

type proxyReuseTrackedBody struct {
	ctx   context.Context
	body  io.ReadCloser
	lease *proxyTransportLease
	once  sync.Once
}

func (b *proxyReuseTrackedBody) Read(p []byte) (int, error) {
	return b.body.Read(p)
}

func (b *proxyReuseTrackedBody) Close() error {
	errClose := b.body.Close()
	b.once.Do(func() {
		b.lease.release(b.ctx, "close", errClose)
	})
	return errClose
}

func logProxyReuseActive(ctx context.Context, req *http.Request, resp *http.Response, provider string, lease *proxyTransportLease) {
	LogWithRequestID(ctx).Infof(
		"[REUSE ACTIVE tid=%d transport=%d/%d sid=%d stream=%d/%d] provider=%s host=%s%s",
		lease.transport.id,
		lease.poolSize,
		proxyPoolMaxTransports,
		lease.streamID,
		lease.acquiredActive,
		proxyPoolMaxActivePerTransport,
		provider,
		proxyReuseHost(req),
		proxyReuseProto(resp),
	)
}

func logProxyReuseInactive(ctx context.Context, req *http.Request, resp *http.Response, provider, reason string, poolSize int) {
	LogWithRequestID(ctx).Infof(
		"[REUSE INACTIVE tid=0 transport=%d/%d sid=0 stream=0/%d reason=%s] provider=%s host=%s%s",
		poolSize,
		proxyPoolMaxTransports,
		proxyPoolMaxActivePerTransport,
		reason,
		provider,
		proxyReuseHost(req),
		proxyReuseProto(resp),
	)
}

func proxyReuseHost(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return req.URL.Host
}

func proxyReuseProto(resp *http.Response) string {
	if resp == nil || strings.TrimSpace(resp.Proto) == "" {
		return ""
	}
	return fmt.Sprintf(" proto=%s", resp.Proto)
}

func proxyReuseProvider(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return strings.TrimSpace(auth.Provider)
}
