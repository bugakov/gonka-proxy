package benchmark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glaicer/gonka-proxy/internal/config"
)

func TestRunDiscoversAndTestsEveryModelWithUniquePrompts(t *testing.T) {
	var prompts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"model-b"},{"id":"model-a"}]}`))
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		prompts = append(prompts, payload.Messages[0].Content)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer server.Close()

	report, err := Run(context.Background(), config.Config{Providers: []config.Provider{{
		Name: "test", BaseURL: server.URL + "/v1", APIKey: "secret", ModelAlias: "fallback", Priority: 1,
	}}}, Options{Rounds: 2, Delay: 0, Now: func() time.Time { return time.Unix(100, 0) }, Rand: func(buffer []byte) (int, error) {
		for i := range buffer {
			buffer[i] = byte(i + 1)
		}
		return len(buffer), nil
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(report.Results) != 1 || len(report.Results[0].Models) != 2 || len(report.Results[0].Runs) != 4 {
		t.Fatalf("report = %#v, want one provider, two models, four runs", report)
	}
	if report.Results[0].Models[0] != "model-a" || report.Results[0].Models[1] != "model-b" {
		t.Fatalf("models = %#v, want sorted discovery order", report.Results[0].Models)
	}
	if len(prompts) != 4 || prompts[0] == prompts[1] || prompts[0] == prompts[2] {
		t.Fatalf("prompts are not unique: %#v", prompts)
	}
	for _, run := range report.Results[0].Runs {
		if run.Status != "success" || run.Model == "" || run.TotalLatencyMS < 0 {
			t.Fatalf("run = %#v, want success", run)
		}
	}
}

func TestRunFallsBackToConfiguredModelsWhenDiscoveryFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			http.Error(w, "not supported", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer server.Close()

	report, err := Run(context.Background(), config.Config{Providers: []config.Provider{{
		Name: "test", BaseURL: server.URL + "/v1", APIKey: "secret", ModelAlias: "legacy", ModelAliases: map[string]string{"virtual": "upstream"}, Priority: 1,
	}}}, Options{Rounds: 1, Delay: 0})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	result := report.Results[0]
	if result.DiscoveryStatus != "error" || !strings.Contains(result.DiscoveryError, "client-error") {
		t.Fatalf("discovery = %#v, want client-error", result)
	}
	if len(result.Models) != 2 || len(result.Runs) != 2 {
		t.Fatalf("result = %#v, want two configured fallback models and runs", result)
	}
}
