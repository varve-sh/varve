package embedding

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- CosineSimilarity ---

func TestCosineSimilarity_Identical(t *testing.T) {
	v := []float64{1, 0, 0}
	got := CosineSimilarity(v, v)
	if got < 0.999 || got > 1.001 {
		t.Errorf("identical vectors: want ~1.0, got %f", got)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float64{1, 0, 0}
	b := []float64{0, 1, 0}
	got := CosineSimilarity(a, b)
	if got > 0.001 {
		t.Errorf("orthogonal vectors: want ~0.0, got %f", got)
	}
}

func TestCosineSimilarity_Opposite(t *testing.T) {
	a := []float64{1, 0, 0}
	b := []float64{-1, 0, 0}
	got := CosineSimilarity(a, b)
	if got > -0.999 {
		t.Errorf("opposite vectors: want ~-1.0, got %f", got)
	}
}

func TestCosineSimilarity_LengthMismatch(t *testing.T) {
	got := CosineSimilarity([]float64{1, 2}, []float64{1, 2, 3})
	if got != 0 {
		t.Errorf("mismatched lengths: want 0, got %f", got)
	}
}

func TestCosineSimilarity_Empty(t *testing.T) {
	got := CosineSimilarity([]float64{}, []float64{})
	if got != 0 {
		t.Errorf("empty vectors: want 0, got %f", got)
	}
}

func TestCosineSimilarity_ZeroVector(t *testing.T) {
	got := CosineSimilarity([]float64{0, 0, 0}, []float64{1, 2, 3})
	if got != 0 {
		t.Errorf("zero vector: want 0, got %f", got)
	}
}

// --- NewClient ---

func TestNewClient_KeyTakesPrecedence(t *testing.T) {
	c := NewClient("primary-key", "fallback-key", "", "")
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.apiKey != "primary-key" {
		t.Errorf("want primary-key, got %s", c.apiKey)
	}
}

func TestNewClient_FallbackKey(t *testing.T) {
	c := NewClient("", "fallback-key", "", "")
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.apiKey != "fallback-key" {
		t.Errorf("want fallback-key, got %s", c.apiKey)
	}
}

func TestNewClient_BothEmpty(t *testing.T) {
	c := NewClient("", "", "", "")
	if c != nil {
		t.Error("expected nil client when both keys are empty")
	}
}

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient("k", "", "", "")
	if c.baseURL != "https://api.openai.com/v1" {
		t.Errorf("want default baseURL, got %s", c.baseURL)
	}
	if c.model != "text-embedding-3-small" {
		t.Errorf("want default model, got %s", c.model)
	}
}

func TestNewClient_CustomValues(t *testing.T) {
	c := NewClient("k", "", "http://localhost:11434/v1/", "nomic-embed-text")
	if c.baseURL != "http://localhost:11434/v1" {
		t.Errorf("trailing slash not stripped, got %s", c.baseURL)
	}
	if c.model != "nomic-embed-text" {
		t.Errorf("want nomic-embed-text, got %s", c.model)
	}
}

// --- NewClientFromEnv ---

func TestNewClientFromEnv_NoKey(t *testing.T) {
	t.Setenv("VARVE_EMBED_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	c := NewClientFromEnv()
	if c != nil {
		t.Error("expected nil client when no API key is set")
	}
}

func TestNewClientFromEnv_WithKey(t *testing.T) {
	t.Setenv("VARVE_EMBED_KEY", "test-key")

	c := NewClientFromEnv()
	if c == nil {
		t.Fatal("expected non-nil client with API key")
	}
	if c.apiKey != "test-key" {
		t.Errorf("want apiKey=test-key, got %s", c.apiKey)
	}
}

func TestNewClientFromEnv_Defaults(t *testing.T) {
	t.Setenv("VARVE_EMBED_KEY", "k")
	t.Setenv("VARVE_EMBED_URL", "")
	t.Setenv("VARVE_EMBED_MODEL", "")

	c := NewClientFromEnv()
	if c.baseURL != "https://api.openai.com/v1" {
		t.Errorf("want default baseURL, got %s", c.baseURL)
	}
	if c.model != "text-embedding-3-small" {
		t.Errorf("want default model, got %s", c.model)
	}
}

func TestNewClientFromEnv_CustomValues(t *testing.T) {
	t.Setenv("VARVE_EMBED_KEY", "k")
	t.Setenv("VARVE_EMBED_URL", "http://localhost:11434/v1")
	t.Setenv("VARVE_EMBED_MODEL", "nomic-embed-text")

	c := NewClientFromEnv()
	if c.baseURL != "http://localhost:11434/v1" {
		t.Errorf("unexpected baseURL: %s", c.baseURL)
	}
	if c.model != "nomic-embed-text" {
		t.Errorf("unexpected model: %s", c.model)
	}
}

// --- Client.Embed ---

func TestClient_Embed_Success(t *testing.T) {
	vec := []float64{0.1, 0.2, 0.3}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		resp := embedResponse{
			Data: []struct {
				Embedding []float64 `json:"embedding"`
			}{{Embedding: vec}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{
		baseURL: srv.URL,
		model:   "test-model",
		apiKey:  "test-key",
		http:    srv.Client(),
	}

	got, err := c.Embed("hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 || got[0] != 0.1 {
		t.Errorf("unexpected embedding: %v", got)
	}
}

func TestClient_Embed_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := embedResponse{
			Error: &struct {
				Message string `json:"message"`
			}{Message: "model not found"},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, model: "bad", apiKey: "k", http: srv.Client()}
	_, err := c.Embed("text")
	if err == nil || err.Error() == "" {
		t.Error("expected error for API error response")
	}
}

func TestClient_Embed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, model: "m", apiKey: "k", http: srv.Client()}
	_, err := c.Embed("text")
	// HTTP 500 returns invalid JSON — should get a parse error
	if err == nil {
		t.Error("expected error for HTTP 500")
	}
}
