package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func testEngine(t *testing.T, base string) *Engine {
	t.Helper()
	e, err := New(&config.Config{}, []*auth.Auth{{ID: "test", Provider: "openai-compatibility", Attributes: map[string]string{"api_key": "synthetic-key", "base_url": base}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestNativeResponsesPreserveOpaqueReasoningAndToolIDs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path=%s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), "opaque-input") || !strings.Contains(string(raw), "call-exact") {
			t.Errorf("replay lost reasoning/tool identity: %s", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp1","object":"response","status":"completed","model":"small","output":[{"id":"rs1","type":"reasoning","encrypted_content":"opaque-output","summary":[]},{"id":"fc1","type":"function_call","call_id":"call-exact","name":"read","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	cfg, err := config.ParseConfigBytes([]byte(fmt.Sprintf(`{"codex-api-key":[{"api-key":"synthetic-key","base-url":%q,"disable-codex-cloaking":true,"models":[{"name":"small","alias":"small","is-compat":true}]}]}`, upstream.URL)))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(cfg, []*auth.Auth{{ID: "native", Provider: "codex", Attributes: map[string]string{"api_key": "synthetic-key", "base_url": upstream.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	req := responseRequest()
	req.CredentialID = "native"
	req.ModelInfo = &ModelInfo{ID: "small", IsCompat: true}
	req.Request.Payload = []byte(`{"model":"small","input":[{"type":"reasoning","encrypted_content":"opaque-input","summary":[]},{"type":"function_call","id":"fc0","call_id":"call-exact","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call-exact","output":"ok"}],"store":false}`)
	resp, err := e.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp.Payload), "opaque-output") || !strings.Contains(string(resp.Payload), "call-exact") {
		t.Fatalf("native response lost opaque/item data: %s", resp.Payload)
	}
}
func responseRequest() Request {
	return Request{CredentialID: "test", Request: executor.Request{Model: "small", Payload: []byte(`{"model":"small","input":"hello","store":false}`)}, Options: executor.Options{SourceFormat: translator.FromString("openai-response"), ResponseFormat: translator.FromString("openai-response")}}
}

func TestEngineExecutesWithoutStartingService(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Error("credential not injected")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request["model"] != "small" {
			t.Errorf("model=%v", request["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"small","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	e := testEngine(t, upstream.URL)
	resp, err := e.Execute(context.Background(), responseRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(resp.Payload) || !strings.Contains(string(resp.Payload), "hello") {
		t.Fatalf("response=%s", resp.Payload)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), responseRequest()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed execute=%v", err)
	}
}

func TestEngineCancellationReleasesExecution(t *testing.T) {
	arrived := make(chan struct{})
	ended := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(arrived)
		<-r.Context().Done()
		close(ended)
	}))
	defer upstream.Close()
	e := testEngine(t, upstream.URL)
	done := make(chan error, 1)
	go func() { _, err := e.Execute(context.Background(), responseRequest()); done <- err }()
	<-arrived
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("cancelled call succeeded")
	}
	<-ended
}

func TestEngineCompactAndUpstreamErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/compact" {
			t.Errorf("compact path=%s", r.URL.Path)
		}
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"code":"unsupported","message":"compact unsupported"}}`)
	}))
	defer upstream.Close()
	e := testEngine(t, upstream.URL)
	req := responseRequest()
	req.Options.Alt = "responses/compact"
	if _, err := e.Execute(context.Background(), req); err == nil {
		t.Fatal("upstream error disappeared")
	}
}

func TestOpaqueReasoningFailsBeforeChatExecution(t *testing.T) {
	e := testEngine(t, "http://127.0.0.1:1")
	req := responseRequest()
	req.Request.Payload = []byte(`{"input":[{"type":"reasoning","encrypted_content":"opaque-signature"}]}`)
	_, err := e.Execute(context.Background(), req)
	var unsupported *UnsupportedReasoningError
	if !errors.As(err, &unsupported) || unsupported.StatusCode() != 400 {
		t.Fatalf("opaque conversion error=%v", err)
	}
}

func TestEngineStreamAndCloseReleaseBlockedConsumer(t *testing.T) {
	ended := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"small\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(ended)
	}))
	defer upstream.Close()
	e := testEngine(t, upstream.URL)
	req := responseRequest()
	stream, err := e.ExecuteStream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	chunk, ok := <-stream.Chunks
	if !ok || chunk.Err != nil || len(chunk.Payload) == 0 {
		t.Fatalf("stream chunk=%v %s", chunk.Err, chunk.Payload)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	for range stream.Chunks {
	} // no blocked sender survives closure
	<-ended
}
