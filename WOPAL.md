# Wopal tracking fork

Upstream: https://github.com/router-for-me/CLIProxyAPI

Fork: https://github.com/sampx/CLIProxyAPI

Initial baseline: `v8.0.29`, commit `5fc580b1d72d87a7bcfe5cfb6f479c1aea5625b3`.
The complete upstream source, Git history, MIT license and copyright notices
remain in this repository.

## Branches

- `main` mirrors upstream. Product patches never land on it.
- `wopal` holds the small execution and translation patch set.
- Commits referenced by a Gateway release are immutable. Merge an updated
  upstream baseline into `wopal`; never rebase or force-push published history.

## Local patches

- `sdk/engine` constructs an in-memory AuthManager and the selected Provider
  executors without Service, listener, watcher, plugin loading or discovery.
  The caller supplies exact credential/model routes. Requests bind their own
  model capability snapshot instead of publishing a global model registry.
  Close cancels active execution, releases stream senders and closes owned idle
  HTTP connections. Request-body logging and global usage recording are disabled.
- The portable `ResponsesExecutor` reuses the existing translators and payload
  rules for a standard Responses upstream. It preserves native Items and opaque
  reasoning, supports actual non-streaming HTTP and SSE, and applies no Codex
  account policy, replay cache, forced streaming or hosted-tool injection.
- `sdk/client/codex` exposes optional portable input preparation for the host
  caller, outside Engine execution. Upstream client-policy helpers remain intact.
- `auth.BindResolvedModelInfo` and `registry.CloneModelInfo` reuse upstream's
  capability representation and clone logic at the public execution seam.
- `usage.WithoutRecording` lets an embedded caller keep execution separate from
  global usage plugins. Provider response usage is unchanged.
- Responses-to-Chat conversion emits only actual reasoning text. Empty reasoning
  does not create placeholder text. Chat-to-Responses conversion does not create
  empty encrypted signatures. Recognized Claude/Gemini reasoning carriers replay through their matching
  translators. Unknown or mismatched opaque state fails explicitly before
  upstream execution.

Gateway routing, capabilities, quota, model roles, catalog, client policy and
public API ownership stay in Gateway. They do not belong in this fork.

## Upgrade

1. Fetch `upstream` and update the mirror `main`.
2. Merge the mirror into `wopal`, keeping previously published commits intact.
3. Run `go test ./... -count=1` and build `./cmd/server`.
4. Run Gateway's four-protocol compatibility matrix: text, tools/tool replay,
   reasoning, structured output, streaming, cancellation and declared compact.
5. Publish the validated `wopal` commit; update Gateway's submodule pin only
   after its own full tests and isolated release validation pass.

The initial integration uses the existing baseline rather than combining a
dependency upgrade with the in-process execution change.
