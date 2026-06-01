package logging

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type endpointKey struct{}
type responseStatusKey struct{}
type responseHeadersKey struct{}
type upstreamTTFBKey struct{}

type responseStatusHolder struct {
	status atomic.Int32
}

type responseHeadersHolder struct {
	mu      sync.RWMutex
	headers http.Header
}

type upstreamTTFBHolder struct {
	durationNanos atomic.Int64
}

func WithEndpoint(ctx context.Context, endpoint string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, endpointKey{}, endpoint)
}

func GetEndpoint(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if endpoint, ok := ctx.Value(endpointKey{}).(string); ok {
		return endpoint
	}
	return ""
}

func WithResponseStatusHolder(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if holder, ok := ctx.Value(responseStatusKey{}).(*responseStatusHolder); ok && holder != nil {
		return ctx
	}
	return context.WithValue(ctx, responseStatusKey{}, &responseStatusHolder{})
}

func WithResponseHeadersHolder(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if holder, ok := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder); ok && holder != nil {
		return ctx
	}
	return context.WithValue(ctx, responseHeadersKey{}, &responseHeadersHolder{})
}

func SetResponseStatus(ctx context.Context, status int) {
	if ctx == nil || status <= 0 {
		return
	}
	holder, ok := ctx.Value(responseStatusKey{}).(*responseStatusHolder)
	if !ok || holder == nil {
		return
	}
	holder.status.Store(int32(status))
}

func SetResponseHeaders(ctx context.Context, headers http.Header) {
	if ctx == nil {
		return
	}
	holder, ok := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder)
	if !ok || holder == nil {
		return
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	holder.headers = cloneHTTPHeader(headers)
}

func GetResponseStatus(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	holder, ok := ctx.Value(responseStatusKey{}).(*responseStatusHolder)
	if !ok || holder == nil {
		return 0
	}
	return int(holder.status.Load())
}

func GetResponseHeaders(ctx context.Context) http.Header {
	if ctx == nil {
		return nil
	}
	holder, ok := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder)
	if !ok || holder == nil {
		return nil
	}
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return cloneHTTPHeader(holder.headers)
}

func cloneHTTPHeader(src http.Header) http.Header {
	if len(src) == 0 {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func WithUpstreamTTFBHolder(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if holder, ok := ctx.Value(upstreamTTFBKey{}).(*upstreamTTFBHolder); ok && holder != nil {
		return ctx
	}
	return context.WithValue(ctx, upstreamTTFBKey{}, &upstreamTTFBHolder{})
}

func WithUpstreamTTFBHolderFrom(ctx, source context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if source == nil {
		return WithUpstreamTTFBHolder(ctx)
	}
	if holder, ok := source.Value(upstreamTTFBKey{}).(*upstreamTTFBHolder); ok && holder != nil {
		return context.WithValue(ctx, upstreamTTFBKey{}, holder)
	}
	return WithUpstreamTTFBHolder(ctx)
}

func RecordUpstreamTTFB(ctx context.Context, duration time.Duration) {
	if ctx == nil {
		return
	}
	if duration <= 0 {
		duration = time.Nanosecond
	}
	holder, ok := ctx.Value(upstreamTTFBKey{}).(*upstreamTTFBHolder)
	if !ok || holder == nil {
		return
	}
	holder.durationNanos.CompareAndSwap(0, int64(duration))
}

func GetUpstreamTTFB(ctx context.Context) (time.Duration, bool) {
	if ctx == nil {
		return 0, false
	}
	holder, ok := ctx.Value(upstreamTTFBKey{}).(*upstreamTTFBHolder)
	if !ok || holder == nil {
		return 0, false
	}
	nanos := holder.durationNanos.Load()
	if nanos <= 0 {
		return 0, false
	}
	return time.Duration(nanos), true
}
