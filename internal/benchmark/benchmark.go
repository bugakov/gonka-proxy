// Package benchmark contains the reusable provider benchmark runner.
package benchmark

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/glaicer/gonka-proxy/internal/config"
)

// Options controls one benchmark run.
type Options struct {
	Rounds       int
	MaxTokens    int
	Timeout      time.Duration
	Delay        time.Duration
	PromptPrefix string
	Now          func() time.Time
	Rand         func([]byte) (int, error)
}

// Report is a machine-readable benchmark result. It deliberately excludes
// provider URLs, API keys, prompts, and response bodies.
type Report struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Rounds      int              `json:"rounds"`
	MaxTokens   int              `json:"max_tokens"`
	Results     []ProviderResult `json:"results"`
}

// ProviderResult groups discovery and chat results for one configured provider.
type ProviderResult struct {
	Provider        string      `json:"provider"`
	Models          []string    `json:"models,omitempty"`
	DiscoveryStatus string      `json:"discovery_status"`
	DiscoveryError  string      `json:"discovery_error,omitempty"`
	Runs            []RunResult `json:"runs,omitempty"`
}

// RunResult is one model/round measurement.
type RunResult struct {
	Model              string    `json:"model"`
	Round              int       `json:"round"`
	StartedAt          time.Time `json:"started_at"`
	Status             string    `json:"status"`
	ErrorCategory      string    `json:"error_category,omitempty"`
	StatusCode         int       `json:"status_code,omitempty"`
	HeaderLatencyMS    int64     `json:"header_latency_ms,omitempty"`
	FirstByteLatencyMS int64     `json:"first_byte_latency_ms,omitempty"`
	TotalLatencyMS     int64     `json:"total_latency_ms"`
	ResponseBytes      int64     `json:"response_bytes,omitempty"`
}

// Run discovers models and tests each discovered model sequentially.
func Run(ctx context.Context, cfg config.Config, options Options) (Report, error) {
	options = options.withDefaults()
	report := Report{
		GeneratedAt: options.Now().UTC(),
		Rounds:      options.Rounds,
		MaxTokens:   options.MaxTokens,
		Results:     make([]ProviderResult, 0, len(cfg.Providers)),
	}

	client := &http.Client{Timeout: options.Timeout}
	for _, provider := range cfg.Providers {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		result := ProviderResult{Provider: provider.Name, DiscoveryStatus: "error"}
		models, err := discoverModels(ctx, client, provider)
		if err != nil {
			result.DiscoveryError = classifyError(err)
			models = configuredModels(provider)
		} else {
			result.DiscoveryStatus = "success"
		}
		result.Models = models

		for _, model := range models {
			for round := 1; round <= options.Rounds; round++ {
				if err := ctx.Err(); err != nil {
					return report, err
				}
				run := testModel(ctx, client, provider, model, round, options)
				result.Runs = append(result.Runs, run)
				if options.Delay > 0 && (round != options.Rounds || model != models[len(models)-1]) {
					select {
					case <-ctx.Done():
						return report, ctx.Err()
					case <-time.After(options.Delay):
					}
				}
			}
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}

func (o Options) withDefaults() Options {
	if o.Rounds <= 0 {
		o.Rounds = 3
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = 8
	}
	if o.Timeout <= 0 {
		o.Timeout = 45 * time.Second
	}
	if o.Delay < 0 {
		o.Delay = 0
	}
	if o.PromptPrefix == "" {
		o.PromptPrefix = "Return exactly OK followed by the nonce."
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Rand == nil {
		o.Rand = rand.Read
	}
	return o
}

func discoverModels(ctx context.Context, client *http.Client, provider config.Provider) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint(provider.BaseURL, "models"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: models returned HTTP %d", classifyHTTPStatus(response.StatusCode), response.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}
	models := make([]string, 0, len(payload.Data))
	seen := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		model := strings.TrimSpace(item.ID)
		if model != "" {
			if _, ok := seen[model]; !ok {
				models = append(models, model)
				seen[model] = struct{}{}
			}
		}
	}
	sort.Strings(models)
	if len(models) == 0 {
		return nil, fmt.Errorf("models response contained no model IDs")
	}
	return models, nil
}

func configuredModels(provider config.Provider) []string {
	seen := map[string]struct{}{}
	models := make([]string, 0, len(provider.ModelAliases)+1)
	if model := strings.TrimSpace(provider.ModelAlias); model != "" {
		models = append(models, model)
		seen[model] = struct{}{}
	}
	for model := range provider.ModelAliases {
		model = strings.TrimSpace(model)
		if model != "" {
			if _, ok := seen[model]; !ok {
				models = append(models, model)
				seen[model] = struct{}{}
			}
		}
	}
	sort.Strings(models)
	return models
}

func testModel(ctx context.Context, client *http.Client, provider config.Provider, model string, round int, options Options) RunResult {
	started := options.Now().UTC()
	result := RunResult{Model: model, Round: round, StartedAt: started, Status: "error"}
	nonceBytes := make([]byte, 8)
	if _, err := options.Rand(nonceBytes); err != nil {
		result.ErrorCategory = "nonce"
		return result
	}
	nonce := fmt.Sprintf("%d-%s-%d-%s", started.UnixNano(), model, round, hex.EncodeToString(nonceBytes))
	body, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{{
			"role":    "user",
			"content": options.PromptPrefix + " Test nonce: " + nonce,
		}},
		"max_tokens":  options.MaxTokens,
		"temperature": 0,
		"stream":      false,
	})
	if err != nil {
		result.ErrorCategory = "request-encode"
		return result
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(provider.BaseURL, "chat/completions"), strings.NewReader(string(body)))
	if err != nil {
		result.ErrorCategory = "request-create"
		return result
	}
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	req.Header.Set("Content-Type", "application/json")
	requestStarted := time.Now()
	response, err := client.Do(req)
	if err != nil {
		result.TotalLatencyMS = time.Since(requestStarted).Milliseconds()
		result.ErrorCategory = classifyError(err)
		return result
	}
	result.HeaderLatencyMS = time.Since(requestStarted).Milliseconds()
	result.StatusCode = response.StatusCode
	firstByteAt := time.Time{}
	reader := &firstByteReader{reader: response.Body, onFirstByte: func() { firstByteAt = time.Now() }}
	responseBody, readErr := io.ReadAll(reader)
	response.Body.Close()
	result.TotalLatencyMS = time.Since(requestStarted).Milliseconds()
	if !firstByteAt.IsZero() {
		result.FirstByteLatencyMS = firstByteAt.Sub(requestStarted).Milliseconds()
	}
	result.ResponseBytes = int64(len(responseBody))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.ErrorCategory = classifyHTTPStatus(response.StatusCode)
		return result
	}
	if readErr != nil {
		result.ErrorCategory = classifyError(readErr)
		return result
	}
	var payload struct {
		Choices []json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil || len(payload.Choices) == 0 {
		result.ErrorCategory = "invalid-response"
		return result
	}
	result.Status = "success"
	return result
}

type firstByteReader struct {
	reader      io.Reader
	onFirstByte func()
	seen        bool
}

func (r *firstByteReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if n > 0 && !r.seen {
		r.seen = true
		r.onFirstByte()
	}
	return n, err
}

func endpoint(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + "/" + path
}

func classifyHTTPStatus(status int) string {
	switch {
	case status == http.StatusTooManyRequests:
		return "rate-limit"
	case status == http.StatusPaymentRequired:
		return "payment-required"
	case status >= 500:
		return "server-error"
	case status >= 400:
		return "client-error"
	default:
		return "http-error"
	}
}

func classifyError(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return "timeout"
	}
	for _, category := range []string{"rate-limit", "payment-required", "server-error", "client-error", "http-error"} {
		if strings.Contains(err.Error(), category) {
			return category
		}
	}
	return "transport-error"
}
