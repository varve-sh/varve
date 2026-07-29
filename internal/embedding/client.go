// Package embedding provides an interface and HTTP client for computing
// text embeddings via any OpenAI-compatible embeddings endpoint.
//
// Configuration (environment variables):
//
//	VARVE_EMBED_URL   Base URL of the embeddings API (default: https://api.openai.com/v1)
//	VARVE_EMBED_MODEL Embedding model name        (default: text-embedding-3-small)
//	VARVE_EMBED_KEY   API key (falls back to OPENAI_API_KEY)
package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

// Embedder computes a dense vector embedding for a text string.
type Embedder interface {
	Embed(text string) ([]float64, error)
}

// Client is an OpenAI-compatible embeddings client.
type Client struct {
	baseURL  string
	model    string
	apiKey   string
	http     *http.Client
	provider string // "openai", "ollama", "local", "custom"
}

// Provider returns a label for the embedding backend ("openai", "ollama", "local", "custom").
func (c *Client) Provider() string { return c.provider }

// Model returns the model name used by this client.
func (c *Client) Model() string { return c.model }

// NewClientFromEnv creates a Client from environment variables.
// Returns nil if no API key is configured — callers treat nil as "embeddings disabled".
func NewClientFromEnv() *Client {
	return NewClient(
		os.Getenv("VARVE_EMBED_KEY"),
		os.Getenv("OPENAI_API_KEY"),
		os.Getenv("VARVE_EMBED_URL"),
		os.Getenv("VARVE_EMBED_MODEL"),
	)
}

// NewClient creates a Client from explicit values. envKey and envFallbackKey are
// tried in order; the first non-empty value is used as the API key.
// Callers that merge config-file values with env vars should prefer this over NewClientFromEnv.
// Returns nil if no API key is resolved — callers treat nil as "embeddings disabled".
func NewClient(key, fallbackKey, baseURL, model string) *Client {
	apiKey := key
	if apiKey == "" {
		apiKey = fallbackKey
	}
	if apiKey == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if model == "" {
		model = "text-embedding-3-small"
	}

	provider := "custom"
	switch {
	case strings.Contains(baseURL, "api.openai.com"):
		provider = "openai"
	case strings.Contains(baseURL, "11434"):
		provider = "ollama"
	case strings.Contains(baseURL, "localhost") || strings.Contains(baseURL, "127.0.0.1"):
		provider = "local"
	}

	return &Client{
		baseURL:  baseURL,
		model:    model,
		apiKey:   apiKey,
		http:     &http.Client{Timeout: 10 * time.Second},
		provider: provider,
	}
}

type embedRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed sends a single text to the embeddings API and returns the vector.
func (c *Client) Embed(text string) ([]float64, error) {
	body, err := json.Marshal(embedRequest{Input: text, Model: c.model})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result embedResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parsing embedding response: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("embedding API error: %s", result.Error.Message)
	}
	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding returned for model %s", c.model)
	}
	return result.Data[0].Embedding, nil
}

// CosineSimilarity returns the cosine similarity between two equal-length vectors.
// Returns 0 if either vector is zero-length or lengths differ.
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
