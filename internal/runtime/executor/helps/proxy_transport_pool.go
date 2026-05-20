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
	proxyPoolMaxTransports          = 64
	proxyPoolMaxActivePerTransport  = 32
	proxyPoolSoftActivePerTransport = 16
	proxyPoolMaxLifetime            = 10 * time.Minute
	proxyPoolIdleTTL                = 10 * time.Minute
	proxyPoolIdleSweepInterval      = time.Minute

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
	createdAt time.Time
	lastUsed  time.Time
}

type proxyTransportLease struct {
	transport      *pooledProxyTransport
	streamID       uint64
	poolSize       int
	acquiredActive int
	acquiredAt     time.Time
	acquiredAge    time.Duration
	released       atomic.Bool
	pool           *proxyTransportPool
}

func (p *proxyTransportPool) acquire(ctx context.Context) (*proxyTransportLease, error) {
	now := time.Now()
	p.mu.Lock()
	evicted := p.evictExpiredIdleLocked(ctx, now)
	defer func() {
		p.mu.Unlock()
		closeIdleConnectionsFor(evicted)
	}()

	if transport := p.leastActiveLocked(proxyPoolSoftActivePerTransport, now); transport != nil {
		return p.acquireLocked(transport, now), nil
	}

	if len(p.transports) < proxyPoolMaxTransports {
		transport := buildProxyTransport(p.proxyURL)
		if transport != nil {
			pooled := &pooledProxyTransport{
				id:        globalProxyTransportID.Add(1),
				transport: transport,
				createdAt: now,
				lastUsed:  now,
			}
			p.transports = append(p.transports, pooled)
			return p.acquireLocked(pooled, now), nil
		}

		if transport := p.leastActiveLocked(proxyPoolMaxActivePerTransport, now); transport != nil {
			return p.acquireLocked(transport, now), nil
		}
		return nil, errProxyBuildFailed
	}

	if transport := p.leastActiveLocked(proxyPoolMaxActivePerTransport, now); transport != nil {
		return p.acquireLocked(transport, now), nil
	}
	return nil, errProxyPoolFull
}

func (p *proxyTransportPool) leastActiveLocked(limit int, now time.Time) *pooledProxyTransport {
	var best *pooledProxyTransport
	for _, transport := range p.transports {
		if transport.active >= limit || transport.expired(now) {
			continue
		}
		if best == nil || proxyPoolSoftDistance(transport.active) < proxyPoolSoftDistance(best.active) {
			best = transport
		}
	}
	return best
}

func proxyPoolSoftDistance(active int) int {
	distance := active - proxyPoolSoftActivePerTransport
	if distance < 0 {
		return -distance
	}
	return distance
}

func (transport *pooledProxyTransport) expired(now time.Time) bool {
	return !transport.createdAt.IsZero() && now.Sub(transport.createdAt) >= proxyPoolMaxLifetime
}

func (p *proxyTransportPool) acquireLocked(transport *pooledProxyTransport, now time.Time) *proxyTransportLease {
	transport.active++
	transport.lastUsed = now
	return &proxyTransportLease{
		transport:      transport,
		streamID:       globalProxyStreamID.Add(1),
		poolSize:       len(p.transports),
		acquiredActive: transport.active,
		acquiredAt:     now,
		acquiredAge:    now.Sub(transport.createdAt),
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

	now := time.Now()
	var evicted http.RoundTripper
	var expired bool
	l.pool.mu.Lock()
	l.transport.active--
	active := l.transport.active
	age := now.Sub(l.transport.createdAt)
	l.transport.lastUsed = now
	if active == 0 && l.transport.expired(now) {
		expired = true
		evicted = l.pool.removeTransportLocked(l.transport)
	}
	poolSize := len(l.pool.transports)
	l.pool.mu.Unlock()
	closeIdleConnections(evicted)

	entry := LogWithRequestID(ctx)
	if err != nil {
		entry = entry.WithError(err)
	}
	entry.Debugf(
		"[REUSE RELEASE tid=%d sid=%d duration=%s age=%s active=%d/%d status=%s]",
		l.transport.id,
		l.streamID,
		time.Since(l.acquiredAt).Truncate(time.Millisecond),
		age.Truncate(time.Millisecond),
		active,
		proxyPoolMaxActivePerTransport,
		status,
	)
	if expired {
		logProxyReuseExpired(ctx, l.transport, age, active, poolSize, "max_lifetime")
	}
}

func (p *proxyTransportPool) evictIdle(now time.Time, idleFor time.Duration) int {
	p.mu.Lock()
	evicted := p.evictIdleLocked(now, idleFor)
	p.mu.Unlock()

	closeIdleConnectionsFor(evicted)
	return len(evicted)
}

func (p *proxyTransportPool) evictIdleLocked(now time.Time, idleFor time.Duration) []http.RoundTripper {
	kept := p.transports[:0]
	evicted := make([]http.RoundTripper, 0)
	expired := make([]*pooledProxyTransport, 0)
	for _, transport := range p.transports {
		if transport.active == 0 && (now.Sub(transport.lastUsed) >= idleFor || transport.expired(now)) {
			evicted = append(evicted, transport.transport)
			if transport.expired(now) {
				expired = append(expired, transport)
			}
			continue
		}
		kept = append(kept, transport)
	}
	p.transports = kept
	for _, transport := range expired {
		logProxyReuseExpired(context.Background(), transport, now.Sub(transport.createdAt), transport.active, len(p.transports), "janitor_max_lifetime")
	}
	return evicted
}

func (p *proxyTransportPool) evictExpiredIdleLocked(ctx context.Context, now time.Time) []http.RoundTripper {
	kept := p.transports[:0]
	evicted := make([]http.RoundTripper, 0)
	expired := make([]*pooledProxyTransport, 0)
	for _, transport := range p.transports {
		if transport.active == 0 && transport.expired(now) {
			evicted = append(evicted, transport.transport)
			expired = append(expired, transport)
			continue
		}
		kept = append(kept, transport)
	}
	p.transports = kept
	for _, transport := range expired {
		logProxyReuseExpired(ctx, transport, now.Sub(transport.createdAt), transport.active, len(p.transports), "max_lifetime")
	}
	return evicted
}

func (p *proxyTransportPool) removeTransportLocked(target *pooledProxyTransport) http.RoundTripper {
	for i, transport := range p.transports {
		if transport != target {
			continue
		}
		p.transports = append(p.transports[:i], p.transports[i+1:]...)
		return transport.transport
	}
	return nil
}

func closeIdleConnections(transport http.RoundTripper) {
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func closeIdleConnectionsFor(transports []http.RoundTripper) {
	for _, transport := range transports {
		closeIdleConnections(transport)
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
	lease, errAcquire := rt.pool.acquire(rt.ctx)
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
		"[REUSE ACTIVE tid=%d transport=%d/%d sid=%d stream=%d/%d age=%s] provider=%s host=%s%s",
		lease.transport.id,
		lease.poolSize,
		proxyPoolMaxTransports,
		lease.streamID,
		lease.acquiredActive,
		proxyPoolMaxActivePerTransport,
		lease.acquiredAge.Truncate(time.Millisecond),
		provider,
		proxyReuseHost(req),
		proxyReuseProto(resp),
	)
}

func logProxyReuseExpired(ctx context.Context, transport *pooledProxyTransport, age time.Duration, active int, poolSize int, reason string) {
	if transport == nil {
		return
	}
	LogWithRequestID(ctx).Infof(
		"[REUSE EXPIRE tid=%d transport=%d/%d age=%s active=%d/%d reason=%s]",
		transport.id,
		poolSize,
		proxyPoolMaxTransports,
		age.Truncate(time.Millisecond),
		active,
		proxyPoolMaxActivePerTransport,
		reason,
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
