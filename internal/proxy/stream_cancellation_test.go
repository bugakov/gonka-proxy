package proxy_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glaicer/gonka-proxy/internal/config"
	"github.com/glaicer/gonka-proxy/internal/proxy"
)

func TestChatCompletionsClassifiesStreamCancellation(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		stream     string
		cancelAt   string
		writeError bool
		completed  bool
	}{
		{name: "before_done", stream: "data: {\"content\":\"[DONE]\"}\n\n", cancelAt: "[DONE]"},
		{name: "after_done", stream: "data: {\"content\":\"complete\"}\n\ndata: [DONE]\n\n", cancelAt: "data: [DONE]\n\n", completed: true},
		{name: "canceled_write", stream: "data: [DONE]\n\n", writeError: true},
	} {
		for _, level := range []config.LogLevel{config.LogLevelInfo, config.LogLevelWarn, config.LogLevelError} {
			t.Run(testCase.name+"/"+string(level), func(t *testing.T) {
				var primaryHits, backupHits atomic.Int32
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if primaryHits.Add(1) == 1 {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, testCase.stream)
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"primary"},"finish_reason":"stop"}]}`)
				}))
				defer primary.Close()
				backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					backupHits.Add(1)
					_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"backup"},"finish_reason":"stop"}]}`)
				}))
				defer backup.Close()
				var logs bytes.Buffer
				handler, err := proxy.NewWithLogger(config.Config{
					ListenAddress: "127.0.0.1:8080", Cooldown: time.Minute, RecoveryWait: time.Second,
					ResponseHeaderTimeout: time.Second, LogLevel: level,
					Providers: []config.Provider{
						{Name: "primary", BaseURL: primary.URL + "/v1", APIKey: "primary-secret", ModelAlias: "primary-model", Priority: 100},
						{Name: "backup", BaseURL: backup.URL + "/v1", APIKey: "backup-secret", ModelAlias: "backup-model", Priority: 50},
					},
				}, cancellationLogger(func(format string, args ...any) {
					fmt.Fprintf(&logs, format+"\n", args...)
				}))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				response := &cancelingStreamWriter{
					ResponseRecorder: httptest.NewRecorder(), cancel: cancel,
					cancelAt: testCase.cancelAt, writeError: testCase.writeError,
				}
				request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[],"stream":true}`)).WithContext(ctx)
				handler.ServeHTTP(response, request)
				if ctx.Err() != context.Canceled {
					t.Fatalf("request context = %v, want client cancellation", ctx.Err())
				}
				if !testCase.writeError && response.Body.String() != testCase.stream {
					t.Fatalf("stream = %q, want %q", response.Body.String(), testCase.stream)
				}
				followup := httptest.NewRecorder()
				handler.ServeHTTP(followup, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[]}`)))
				if primaryHits.Load() != 2 || backupHits.Load() != 0 || !strings.Contains(followup.Body.String(), "primary") {
					t.Errorf("cancellation affected Provider availability: primary=%d backup=%d response=%s", primaryHits.Load(), backupHits.Load(), followup.Body.String())
				}
				output := logs.String()
				if strings.Contains(output, "STREAM-ABORT") || strings.Contains(output, "failover") || strings.Contains(output, "cooldown") {
					t.Errorf("cancellation logged as stream failure: %s", output)
				}
				wantCancellation := level == config.LogLevelInfo && !testCase.completed
				if strings.Contains(output, "request canceled - stream") != wantCancellation {
					t.Errorf("unexpected cancellation logging: %s", output)
				}
			})
		}
	}
}

func TestChatCompletionsStillWarnsOnUpstreamStreamFailure(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		contentLength string
		reason        string
		readError     error
	}{
		{name: "missing_done", reason: "stream ended at EOF without [DONE] marker"},
		{name: "read_error", contentLength: "100", reason: "upstream read error: unexpected EOF", readError: io.ErrUnexpectedEOF},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			const chunk = "data: {\"content\":\"incomplete\"}\n\n"
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if testCase.contentLength != "" {
					w.Header().Set("Content-Length", testCase.contentLength)
				}
				_, _ = io.WriteString(w, chunk)
				w.(http.Flusher).Flush()
			}))
			defer upstream.Close()
			server, logs := newLoggedProxyServer(t, []providerFixture{
				{baseURL: upstream.URL + "/v1", apiKey: "provider-secret", modelAlias: "provider-model", priority: 1},
			}, config.LogLevelWarn)
			defer server.Close()
			response, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"messages":[],"stream":true}`))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, readErr := io.ReadAll(response.Body)
			if string(body) != chunk || !errors.Is(readErr, testCase.readError) {
				t.Fatalf("response = (%q, %v), want (%q, %v)", body, readErr, chunk, testCase.readError)
			}
			server.Close()
			output := logs.String()
			if !strings.Contains(output, "STREAM-ABORT") || !strings.Contains(output, testCase.reason) {
				t.Errorf("upstream stream failure warning missing: %s", output)
			}
		})
	}
}

type cancelingStreamWriter struct {
	*httptest.ResponseRecorder
	cancel     context.CancelFunc
	cancelAt   string
	writeError bool
}

func (w *cancelingStreamWriter) Write(chunk []byte) (int, error) {
	if w.writeError {
		w.cancel()
		return 0, io.ErrClosedPipe
	}
	count, err := w.ResponseRecorder.Write(chunk)
	if strings.Contains(w.Body.String(), w.cancelAt) {
		w.cancel()
	}
	return count, err
}
