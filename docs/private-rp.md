# Private RP fork

This fork keeps the upstream Go module name for merge compatibility. Set
`CPA_PRIVATE_RP=1` to enable the private RP behavior. Leave it unset for upstream
behavior. No credentials, runtime config, user files, or chat exports belong in Git.

## Patch inventory

- Keep leading system/developer instructions; reject mid-conversation instructions
  instead of silently demoting them. Skip optional system-word obfuscation.
- Preserve the translated output-token limit and map OpenAI stop sequences.
- Disable Gemini reasoning replay and generate a fresh Antigravity upstream
  session ID per request. Routing affinity is separate from that upstream ID.
- Disable request/error body files and suppress the general Logrus message/fields.
  This does not cover every library, frontend, cluster audit, or provider log.
- Bind the local Antigravity OAuth callback to loopback.
- Scope explicit frontend affinity to frontend + user + conversation. Reject
  empty, unresolved, or duplicate identity headers on generation endpoints.
  Editing the prompt does not change that identity. Distinct conversations can
  legitimately select the same account; their bindings remain independent.

## Routing configuration (v8)

```yaml
routing:
  strategy: round-robin
  session-affinity: true
  session-affinity-ttl: 24h
  session-affinity-rebind-on: quota
  session-affinity-subagents: false
quota-exceeded:
  switch-project: false
  switch-preview-model: false
oauth:
  providers:
    antigravity:
      antigravity-credits: false
```

Keep model aliases one-to-one if automatic model substitution is unwanted.
`quota` also rebinds for authentication/eligibility failures; transient failures
may borrow another credential without replacing the original binding. Affinity
is an in-memory, expiring routing hint, not durable chat storage. Restarting CPA
loses bindings. One Antigravity account cannot fail over to a Meta account for a
Gemini model.

## Frontend headers

OpenWebUI provider connection `headers` (no user-info forwarding switch required):

```json
{
  "X-RP-Client": "openwebui",
  "X-RP-User-ID": "{{USER_ID}}",
  "X-RP-Chat-ID": "{{CHAT_ID}}"
}
```

LibreChat custom endpoint:

```yaml
headers:
  X-RP-Client: librechat
  X-RP-User-ID: "{{LIBRECHAT_USER_ID}}"
  X-RP-Chat-ID: "{{LIBRECHAT_BODY_CONVERSATIONID}}"
```

CPA hashes the tuple and gives it precedence over content-derived or legacy
session identities. These headers are routing input, not an authentication or
tenant authorization boundary; only trusted frontends should hold proxy keys.
Unmarked API clients retain upstream identity behavior. Temporary chats without
a stable ID fail explicitly rather than merging on identical opening text.

## Validation

```sh
go test ./internal/rpmode ./sdk/cliproxy/session ./sdk/cliproxy/auth ./internal/api/middleware -run 'Test(FrontendSession|PrivateRP|ManagerExecuteAndStreamRebindOnQuotaEndToEnd|ManagerSessionAffinityRebindOnQuota)' -count=1
go test ./internal/translator/antigravity/openai/chat-completions ./internal/logging ./internal/runtime/executor -run 'Test(PrivateRP|AntigravityBuildRequest|ConvertOpenAIRequestToAntigravity)' -count=1
go build -o /tmp/cliproxy-rp-check ./cmd/server
```

On 2026-10-24, live OpenWebUI v0.12.3 and LibreChat v0.8.8 backends were tested
against a temporary local mock with two synthetic credentials. Each frontend
created two chats with identical opening text. Four interleaved requests kept
independent bindings (B/A/B/A and A/B/A/B). LibreChat generated its conversation
ID before resolving the first outbound request headers. A simulated 429 caused
same-model account failover through OpenWebUI. The mock saw no raw X-RP headers.
No real Google/Meta generation or two-real-Antigravity-account quota exhaustion
was exercised. The broader upstream suite is not claimed as passing.

## Upstream maintenance

- `origin`: personal fork; `upstream`: router-for-me/CLIProxyAPI.
- `rp/main`: maintained custom branch. Base of this patch: `d946c4fba6fc9a5cfbca1f3d85184d62ad2fb683`.
- Keep upstream `main` as a reference; never force-sync `rp/main` over upstream.
- Use feature branches and PRs targeting `rp/main`. Keep RP changes gated and
  isolated; avoid modifying unrelated executors and frontend source trees.
- Fetch upstream when an update is wanted. For an initially shallow clone,
  first run `git fetch --unshallow upstream` to obtain merge ancestry.

Example manual synchronization, with a clean working tree:

```sh
git fetch upstream
git switch rp/main
git pull --ff-only origin rp/main
git switch -c sync/upstream-YYYYMMDD
git merge --no-ff upstream/main
# Resolve conflicts, run the validation commands, then test both frontends.
git push -u origin HEAD
gh pr create --repo BppleMan/CLIProxyAPI --base rp/main --head sync/upstream-YYYYMMDD
```

Review changes to translators, final payload rules, session extraction, logging,
auth refresh, model catalogs, and frontend versions especially carefully. Merge
the synchronization PR only after checks pass. Do not silently replace a running
instance just because upstream has changed. Use commit-addressed test builds.
