// Package codex exposes optional client compatibility transformations. Hosts
// choose where to call them; the generic Engine never applies client policy.
package codex

import (
	"context"
	"net/http"

	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/optimize-multi-agent-v2"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func PrepareInput(ctx context.Context, headers http.Header, body []byte) ([]byte, bool) {
	cfg := &config.Config{}
	cfg.Client.Codex.OptimizeMultiAgentV2 = true
	// Prepare tools at the Responses boundary, then rewrite portable input.
	// Namespace optimization stays with the authenticated Codex executor, which
	// can also restore the namespace in its own response.
	prepared, optimized := multiagentv2.PrepareCodexMultiAgentV2Tools(ctx, headers, body, true, cfg.Home.Enabled)
	updated := multiagentv2.RewriteCodexMultiAgentV2Input(ctx, headers, prepared, cfg, true)
	return multiagentv2.RewriteCodexMultiAgentV2Input(ctx, headers, updated, cfg, true), optimized
}
func RestoreResponse(body []byte, optimized bool) []byte {
	return multiagentv2.RestoreCodexMultiAgentV2Response(body, optimized)
}
