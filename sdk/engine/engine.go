// Package engine exposes provider execution without a proxy server, watcher,
// model discovery loop, or credential persistence.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtime "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	clauderesponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	geminiresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
)

var ErrClosed = errors.New("engine is closed")

type ModelInfo = registry.ModelInfo
type ThinkingSupport = registry.ThinkingSupport

type UnsupportedReasoningError struct{}

func (*UnsupportedReasoningError) Error() string {
	return "opaque reasoning cannot be translated to this upstream protocol"
}
func (*UnsupportedReasoningError) StatusCode() int       { return 400 }
func (*UnsupportedReasoningError) IsRequestScoped() bool { return true }

// Request names an already-selected credential and upstream model. The host
// owns routing; execution never consults the process-global model registry.
type Request struct {
	CredentialID string
	Request      executor.Request
	Options      executor.Options
	ModelInfo    *ModelInfo
}

type Engine struct {
	mu        sync.Mutex
	closed    bool
	cancel    context.CancelFunc
	ctx       context.Context
	wg        sync.WaitGroup
	manager   *auth.Manager
	executors map[string]auth.ProviderExecutor
	transport *http.Transport
}

func New(cfg *config.Config, credentials []*auth.Auth) (*Engine, error) {
	if cfg == nil {
		return nil, fmt.Errorf("engine config is required")
	}
	copyConfig := *cfg
	copyConfig.RequestLog = false
	copyConfig.LoggingToFile = false
	e := &Engine{manager: auth.NewManager(nil, nil, nil), executors: map[string]auth.ProviderExecutor{}}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.transport = &http.Transport{Proxy: http.ProxyFromEnvironment}
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		e.transport = transport.Clone()
	}
	e.manager.SetConfig(&copyConfig)
	for _, credential := range credentials {
		if credential == nil || credential.ID == "" || credential.Provider == "" {
			_ = e.Close()
			return nil, fmt.Errorf("engine credential identity is required")
		}
		if _, exists := e.executors[credential.ID]; exists {
			_ = e.Close()
			return nil, fmt.Errorf("duplicate engine credential identity")
		}
		var exec auth.ProviderExecutor
		switch credential.Provider {
		case "codex", "openai-responses":
			exec = runtime.NewResponsesExecutor(&copyConfig)
		case "claude":
			exec = runtime.NewClaudeExecutor(&copyConfig)
		case "gemini":
			exec = runtime.NewGeminiExecutor(&copyConfig)
		default:
			exec = runtime.NewOpenAICompatExecutor(credential.Provider, &copyConfig)
		}
		e.manager.RegisterExecutor(exec)
		if _, err := e.manager.Register(context.Background(), credential.Clone()); err != nil {
			_ = e.Close()
			return nil, err
		}
		e.executors[credential.ID] = exec
	}
	return e, nil
}

func (e *Engine) start(ctx context.Context, req Request) (context.Context, func(), *auth.Auth, auth.ProviderExecutor, executor.Request, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, nil, nil, nil, req.Request, ErrClosed
	}
	exec := e.executors[req.CredentialID]
	credential, ok := e.manager.GetByID(req.CredentialID)
	if !ok || exec == nil {
		e.mu.Unlock()
		return nil, nil, nil, nil, req.Request, fmt.Errorf("engine credential not registered")
	}
	if credential.Provider != "codex" && credential.Provider != "openai-responses" {
		var body struct {
			Input []struct {
				Type      string `json:"type"`
				Encrypted string `json:"encrypted_content"`
			} `json:"input"`
		}
		if json.Unmarshal(req.Request.Payload, &body) == nil {
			for _, item := range body.Input {
				if item.Type == "reasoning" && item.Encrypted != "" {
					compatible := false
					switch credential.Provider {
					case "claude":
						compatible = clauderesponses.CompatibleReasoningCarrier(item.Encrypted)
					case "gemini":
						compatible = geminiresponses.CompatibleReasoningCarrier(item.Encrypted)
					}
					if compatible {
						continue
					}
					e.mu.Unlock()
					return nil, nil, nil, nil, req.Request, &UnsupportedReasoningError{}
				}
			}
		}
	}
	e.wg.Add(1)
	e.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	ctx = usage.WithoutRecording(ctx)
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", e.transport)
	ctx = helps.EnsureSessionContext(ctx, req.Options, req.Request.Payload)
	if !util.HasExplicitSessionID(ctx) {
		ctx = util.WithSessionID(ctx, uuid.NewString())
	}
	stop := context.AfterFunc(e.ctx, cancel)
	finish := func() { stop(); cancel(); e.wg.Done() }
	if err := ctx.Err(); err != nil {
		finish()
		return nil, nil, nil, nil, req.Request, err
	}
	request := req.Request
	request.Metadata = maps.Clone(request.Metadata)
	if request.Metadata == nil {
		request.Metadata = map[string]any{}
	}
	info := req.ModelInfo
	if info == nil {
		info = &ModelInfo{ID: request.Model}
	}
	request = auth.BindResolvedModelInfo(request, info)
	return ctx, finish, credential, exec, request, nil
}

func (e *Engine) Execute(ctx context.Context, req Request) (executor.Response, error) {
	ctx, finish, credential, exec, request, err := e.start(ctx, req)
	if err != nil {
		return executor.Response{}, err
	}
	defer finish()
	return exec.Execute(ctx, credential, request, req.Options)
}

func (e *Engine) ExecuteStream(ctx context.Context, req Request) (*executor.StreamResult, error) {
	ctx, finish, credential, exec, request, err := e.start(ctx, req)
	if err != nil {
		return nil, err
	}
	opts := req.Options
	opts.Stream = true
	result, err := exec.ExecuteStream(ctx, credential, request, opts)
	if err != nil {
		finish()
		return nil, err
	}
	if result == nil {
		finish()
		return nil, fmt.Errorf("executor returned no stream")
	}
	chunks := make(chan executor.StreamChunk)
	go func() {
		defer finish()
		defer close(chunks)
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-result.Chunks:
				if !ok {
					return
				}
				select {
				case chunks <- chunk:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return &executor.StreamResult{Headers: result.Headers.Clone(), Chunks: chunks}, nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	if !e.closed {
		e.closed = true
		e.cancel()
	}
	e.mu.Unlock()
	e.wg.Wait()
	e.transport.CloseIdleConnections()
	return nil
}
