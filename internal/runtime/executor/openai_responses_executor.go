package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/sjson"
)

// ResponsesExecutor executes portable Responses without Codex account policy,
// private input normalization, hosted-tool injection or replay caches.
type ResponsesExecutor struct{ *OpenAICompatExecutor }

func NewResponsesExecutor(cfg *config.Config) *ResponsesExecutor {
	return &ResponsesExecutor{NewOpenAICompatExecutor("openai-responses", cfg)}
}

func (e *ResponsesExecutor) prepare(ctx context.Context, a *auth.Auth, req executor.Request, opts executor.Options, stream bool) (*http.Request, []byte, error) {
	base, key := e.resolveCredentials(a)
	if base == "" {
		return nil, nil, statusErr{code: 401, msg: "provider base URL is required"}
	}
	to := translator.FromString("openai-response")
	original := opts.OriginalRequest
	if len(original) == 0 {
		original = req.Payload
	}
	translated := translator.TranslateRequest(opts.SourceFormat, to, req.Model, req.Payload, stream)
	var err error
	translated, err = sjson.SetBytes(translated, "model", req.Model)
	if err != nil {
		return nil, nil, err
	}
	endpoint := "/responses"
	if opts.Alt == "responses/compact" {
		if stream {
			return nil, nil, statusErr{code: 400, msg: "compact does not support streaming"}
		}
		endpoint = "/responses/compact"
		translated, err = sjson.DeleteBytes(translated, "stream")
	} else {
		translated, err = sjson.SetBytes(translated, "stream", stream)
	}
	if err != nil {
		return nil, nil, err
	}
	// Payload rules are the final semantic barrier; only HTTP framing follows.
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, req.Model, to.String(), opts.SourceFormat.String(), "", translated, original, req.Model, helps.PayloadRequestPath(opts), opts.Headers)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+endpoint, bytes.NewReader(translated))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	var attrs map[string]string
	if a != nil {
		attrs = a.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	return httpReq, translated, nil
}

func (e *ResponsesExecutor) Execute(ctx context.Context, a *auth.Auth, req executor.Request, opts executor.Options) (executor.Response, error) {
	httpReq, translated, err := e.prepare(ctx, a, req, opts, false)
	if err != nil {
		return executor.Response{}, err
	}
	resp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, a, 0).Do(httpReq)
	if err != nil {
		return executor.Response{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return executor.Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return executor.Response{}, statusErr{code: resp.StatusCode, msg: string(body)}
	}
	var state any
	payload := body
	to := executor.ResponseFormatOrSource(opts)
	if to != translator.FromString("openai-response") {
		payload = translator.TranslateNonStream(ctx, translator.FromString("openai-response"), to, req.Model, opts.OriginalRequest, translated, body, &state)
	}
	return executor.Response{Payload: payload, Headers: resp.Header.Clone()}, nil
}

func (e *ResponsesExecutor) ExecuteStream(ctx context.Context, a *auth.Auth, req executor.Request, opts executor.Options) (*executor.StreamResult, error) {
	httpReq, translated, err := e.prepare(ctx, a, req, opts, true)
	if err != nil {
		return nil, err
	}
	resp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, a, 0).Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, statusErr{code: resp.StatusCode, msg: string(body)}
	}
	chunks := make(chan executor.StreamChunk)
	go func() {
		defer close(chunks)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 4096), 32<<20)
		var state any
		send := func(chunk executor.StreamChunk) bool {
			select {
			case chunks <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			if executor.ResponseFormatOrSource(opts) == translator.FromString("openai-response") {
				if !send(executor.StreamChunk{Payload: append(line, '\n')}) {
					return
				}
			} else if bytes.HasPrefix(line, []byte("data:")) {
				for _, payload := range translator.TranslateStream(ctx, translator.FromString("openai-response"), executor.ResponseFormatOrSource(opts), req.Model, opts.OriginalRequest, translated, line, &state) {
					if !send(executor.StreamChunk{Payload: payload}) {
						return
					}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			send(executor.StreamChunk{Err: err})
		}
	}()
	return &executor.StreamResult{Headers: resp.Header.Clone(), Chunks: chunks}, nil
}
