package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// The caller tries to enable capture. Inspect the config bound to each real
// executor, rather than a second config object or a sanitization helper result.
// Removing either assignment in New must make this test fail; the controller
// verifies that property with Go overlays that mutate one assignment at a time.
func TestEngineDisablesCallerRequestedCapture(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chat-1","object":"chat.completion","model":"small","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	cfg.RequestLog = true
	cfg.LoggingToFile = true
	var credentials []*auth.Auth
	for _, kind := range []string{"openai-compatibility", "openai-responses", "claude", "gemini"} {
		credentials = append(credentials, &auth.Auth{ID: kind, Provider: kind, Attributes: map[string]string{"api_key": "synthetic", "base_url": upstream.URL}})
	}
	e, err := New(cfg, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	req := responseRequest()
	req.CredentialID = "openai-compatibility"
	if _, err := e.Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for id, executor := range e.executors {
		bound := reflect.ValueOf(executor).Elem().FieldByName("cfg")
		if !bound.IsValid() || bound.IsNil() {
			t.Fatalf("%s executor has no effective config", id)
		}
		for _, flag := range []string{"RequestLog", "LoggingToFile"} {
			if bound.Elem().FieldByName(flag).Bool() {
				t.Errorf("%s effective %s remains enabled", id, flag)
			}
		}
	}
	if !cfg.RequestLog || !cfg.LoggingToFile {
		t.Fatal("engine modified the caller's configuration")
	}
}
