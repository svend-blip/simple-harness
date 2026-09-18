package model

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeContextLimit_VLLMModelsList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[{"id":"other","max_model_len":4096},{"id":"qwen","max_model_len":32768}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	p := ProbeContextLimit(context.Background(), srv.URL, "qwen", "", time.Second)
	if p.Limit != 32768 || p.Source == "" {
		t.Fatalf("probe = %+v, want 32768 from /v1/models", p)
	}
}

func TestProbeContextLimit_LlamaCppProps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"qwen","object":"model"}]}`)
		case "/props":
			fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":16384}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := ProbeContextLimit(context.Background(), srv.URL, "qwen", "", time.Second)
	if p.Limit != 16384 {
		t.Fatalf("probe = %+v, want 16384 from /props", p)
	}
}

// Ollama: the served window is num_ctx in the Modelfile parameters;
// the architecture's context_length (262144 here) must not be used —
// a limit above the served window would make the budget unsafe.
func TestProbeContextLimit_OllamaServedWindowNotArchitectureMax(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"qwen3.6-27b-64k:latest","object":"model"}]}`)
		case "/api/show":
			fmt.Fprint(w, `{"parameters":"temperature 0.6\nnum_ctx 65536\n","model_info":{"qwen35.context_length":262144}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := ProbeContextLimit(context.Background(), srv.URL, "qwen3.6-27b-64k", "", time.Second)
	if p.Limit != 65536 {
		t.Fatalf("probe = %+v, want 65536 (num_ctx), never the 262144 architecture figure", p)
	}
}

func TestProbeContextLimit_NothingReliableIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"qwen","object":"model"}]}`)
		case "/api/show":
			fmt.Fprint(w, `{"parameters":"temperature 0.6\n","model_info":{"llama.context_length":131072}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	if p := ProbeContextLimit(context.Background(), srv.URL, "qwen", "", time.Second); p.Limit != 0 {
		t.Fatalf("probe = %+v, want unknown", p)
	}
	// An unreachable runtime is unknown too, within the timeout.
	start := time.Now()
	if p := ProbeContextLimit(context.Background(), "http://127.0.0.1:1", "qwen", "", 500*time.Millisecond); p.Limit != 0 {
		t.Fatalf("probe = %+v, want unknown", p)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("an unreachable runtime took too long to give up on")
	}
}
