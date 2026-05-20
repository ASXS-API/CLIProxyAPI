package logging

import (
	"context"
	"sync/atomic"
	"time"
)

type endpointKey struct{}
type responseStatusKey struct{}
type upstreamTTFBKey struct{}

type responseStatusHolder struct {
	status atomic.Int32
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
