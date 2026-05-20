package logging

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func TestGinLogrusRecoveryRepanicsErrAbortHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusRecovery())
	engine.GET("/abort", func(c *gin.Context) {
		panic(http.ErrAbortHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/abort", nil)
	recorder := httptest.NewRecorder()

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("expected panic, got nil")
		}
		err, ok := recovered.(error)
		if !ok {
			t.Fatalf("expected error panic, got %T", recovered)
		}
		if !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("expected ErrAbortHandler, got %v", err)
		}
		if err != http.ErrAbortHandler {
			t.Fatalf("expected exact ErrAbortHandler sentinel, got %v", err)
		}
	}()

	engine.ServeHTTP(recorder, req)
}

func TestGinLogrusRecoveryHandlesRegularPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusRecovery())
	engine.GET("/panic", func(c *gin.Context) {
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
}

func TestIsAIAPIPathIncludesImages(t *testing.T) {
	if !isAIAPIPath("/v1/images/generations") {
		t.Fatalf("expected /v1/images/generations to be treated as AI API path")
	}
	if !isAIAPIPath("/v1/images/edits") {
		t.Fatalf("expected /v1/images/edits to be treated as AI API path")
	}
}

func TestGinLogrusLoggerIncludesUpstreamTTFB(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var buf bytes.Buffer
	previousOut := log.StandardLogger().Out
	previousFormatter := log.StandardLogger().Formatter
	previousLevel := log.StandardLogger().Level
	log.SetOutput(&buf)
	log.SetFormatter(&log.TextFormatter{DisableTimestamp: true, DisableColors: true})
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOut)
		log.SetFormatter(previousFormatter)
		log.SetLevel(previousLevel)
	})

	engine := gin.New()
	engine.Use(GinLogrusLogger())
	engine.POST("/v1/responses", func(c *gin.Context) {
		RecordUpstreamTTFB(c.Request.Context(), 1234*time.Millisecond)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	got := buf.String()
	if !bytes.Contains([]byte(got), []byte("TTFB 1.23s")) {
		t.Fatalf("log line missing TTFB: %s", got)
	}
	if !bytes.Contains([]byte(got), []byte("POST")) || !bytes.Contains([]byte(got), []byte("/v1/responses")) {
		t.Fatalf("log line missing request: %s", got)
	}
}

func TestFormatTTFBDuration(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{name: "seconds", in: 27325 * time.Millisecond, want: "27.33s"},
		{name: "milliseconds", in: 1234 * time.Microsecond, want: "1.23ms"},
		{name: "microseconds", in: 450 * time.Nanosecond, want: "0.45us"},
		{name: "missing", in: 0, want: "-"},
	}

	for _, tt := range tests {
		if got := formatTTFBDuration(tt.in); got != tt.want {
			t.Fatalf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}
