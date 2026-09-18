package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ContextLimitProbe is what ProbeContextLimit found: the served
// context window in tokens and where the figure came from. Limit 0
// means the runtime did not report one reliably.
type ContextLimitProbe struct {
	Limit  int
	Source string
}

// ProbeContextLimit asks the runtime what context window it actually
// serves for modelName (addendum §5: "from runtime/model metadata
// where reliable"). It tries, in order:
//
//   - GET <base>/v1/models — vLLM and SGLang report max_model_len,
//     some llama.cpp builds report meta.n_ctx / n_ctx / context_length;
//   - GET <base>/props — llama.cpp's default_generation_settings.n_ctx,
//     which is the KV cache actually allocated;
//   - POST <base>/api/show — Ollama's Modelfile parameters, whose
//     num_ctx is the window the model is served with.
//
// The figures it deliberately does NOT use are architecture maxima
// such as Ollama's model_info context_length (262144 for a model
// served at 65536): a limit larger than the served window would make
// the budget unsafe, and §4 says estimation prefers safety. A runtime
// that reports nothing usable yields Limit 0 and no error; the caller
// falls back to configuration or reports the limit as unknown.
//
// Every request is bounded by timeout and by ctx; no failure is fatal.
func ProbeContextLimit(ctx context.Context, baseURL, modelName, apiKey string, timeout time.Duration) ContextLimitProbe {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	client := &http.Client{Timeout: timeout, Transport: bearerTransport{key: apiKey}}
	base := strings.TrimRight(baseURL, "/")

	if n := probeModelsList(ctx, client, base, modelName); n > 0 {
		return ContextLimitProbe{Limit: n, Source: "runtime /v1/models"}
	}
	if n := probeLlamaProps(ctx, client, base); n > 0 {
		return ContextLimitProbe{Limit: n, Source: "runtime /props (llama.cpp n_ctx)"}
	}
	if n := probeOllamaShow(ctx, client, base, modelName); n > 0 {
		return ContextLimitProbe{Limit: n, Source: "runtime /api/show (ollama num_ctx)"}
	}
	return ContextLimitProbe{}
}

func getJSON(ctx context.Context, client *http.Client, url string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// probeModelsList reads the OpenAI-compatible listing and looks for
// the model's entry (exact id, or the id with a ":latest" tag, or the
// only entry when the runtime serves one model).
func probeModelsList(ctx context.Context, client *http.Client, base, modelName string) int {
	var listing struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := getJSON(ctx, client, base+"/v1/models", &listing); err != nil {
		return 0
	}
	var entry map[string]interface{}
	for _, m := range listing.Data {
		id, _ := m["id"].(string)
		if id == modelName || id == modelName+":latest" || strings.TrimSuffix(id, ":latest") == modelName {
			entry = m
			break
		}
	}
	if entry == nil && len(listing.Data) == 1 {
		entry = listing.Data[0]
	}
	if entry == nil {
		return 0
	}
	for _, key := range []string{"max_model_len", "context_length", "n_ctx", "max_context_length"} {
		if n := intField(entry[key]); n > 0 {
			return n
		}
	}
	if meta, ok := entry["meta"].(map[string]interface{}); ok {
		for _, key := range []string{"n_ctx", "n_ctx_train"} {
			if n := intField(meta[key]); n > 0 {
				return n
			}
		}
	}
	return 0
}

// probeLlamaProps reads llama.cpp's /props.
func probeLlamaProps(ctx context.Context, client *http.Client, base string) int {
	var props struct {
		Defaults struct {
			NCtx interface{} `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if err := getJSON(ctx, client, base+"/props", &props); err != nil {
		return 0
	}
	return intField(props.Defaults.NCtx)
}

// probeOllamaShow reads Ollama's /api/show and takes num_ctx from the
// Modelfile parameters — the served window — never the architecture's
// context_length.
func probeOllamaShow(ctx context.Context, client *http.Client, base, modelName string) int {
	body, _ := json.Marshal(map[string]string{"model": modelName})
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/api/show", bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0
	}
	var show struct {
		Parameters string `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&show); err != nil {
		return 0
	}
	sc := bufio.NewScanner(strings.NewReader(show.Parameters))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[0] == "num_ctx" {
			n, err := strconv.Atoi(fields[1])
			if err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// bearerTransport adds the endpoint's credential to probe requests:
// a cloud endpoint's /v1/models needs it, and without it the probe
// would report unknown for a runtime that could have answered.
type bearerTransport struct{ key string }

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if b.key != "" {
		req.Header.Set("Authorization", "Bearer "+b.key)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func intField(v interface{}) int {
	switch n := v.(type) {
	case float64:
		if n > 0 && n < 1<<31 {
			return int(n)
		}
	case string:
		if i, err := strconv.Atoi(n); err == nil && i > 0 {
			return i
		}
	}
	return 0
}
