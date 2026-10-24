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

## Antigravity 429 boundary (private RP mode)

With `CPA_PRIVATE_RP=1`, an Antigravity 429 terminates the current request. It
cannot retry another credential, a model pool, credits, or the SSE bootstrap.
After output has started, the existing stream error framing is used; an HTTP
status already committed as 200 cannot become 429. Other providers and mode-off
behavior retain upstream semantics.

Before returning the error, the manager persists an account-wide RP quarantine.
This is a conservative scheduling policy, not a statement that every model's
subscription quota is zero. All models and chats exclude the quarantined account.
The next client request, including an automatic client retry, may select another
eligible account. No explicit continuation signal is required.

One quota-only POST to `retrieveUserQuotaSummary` is queued per quarantined auth;
at most four are sent concurrently. It uses the existing account endpoint, proxy,
UA, and current access token. Quota observation does not rotate an expiring token
on an unpersisted clone: normal OAuth refresh owns credential acquisition.
Successful generation responses never trigger a quota query. There is no idle
quota polling; later requests revalidate accounts whose retry/reset boundary is due.

Remaining values are stored only from validated quota responses. Unknown is not
zero or full. A failed/empty/malformed query leaves quarantine intact. A positive
response immediately after a 429 is retained for observation but cannot release
quarantine before the retry boundary. Zero/disabled buckets extend revalidation
to their known reset times; no five-hour deadline is invented. Re-enabling requires
a fresh positive observation of all returned baseline buckets after the boundary.
This intentionally favors exclusion when bucket-to-model scope is uncertain.

Registration/credential versions and a new per-429 fence reject stale probe
results. Job cancellation is tied to manager shutdown. Quarantine and its latest
snapshot are persisted in auth metadata; a restart retains exclusion. Session
affinity itself is still upstream's in-memory model-scoped hint in this PR.
Session-only global balancing, durable session counts, and proactive 90% admission
are separate follow-up features, not implemented by this quota-boundary change.

The contract fixtures and provenance are under
`test/fixtures/antigravity-quota/`. They are reconstructed from client parsing and
protocol descriptors, not claimed as live captures. Tests exercise real CPA
OpenAI handlers and the Antigravity executor against local synthetic HTTP/SSE
responses, including no-eligible-account handling. They do not consume Google
quota or prove current backend acceptance of the quota endpoint for every account.

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

## Production deployment

The fork image is published by `private-rp-image.yml` after RP regression tests,
at `ghcr.io/bppleman/cliproxyapi-rp:sha-<full-commit>`. Deploy by digest. The image
runs as UID/GID 10001 with a read-only root filesystem. The runtime is a single
writer with a writable `/data` PVC containing `auth/`, `config.yaml`, the local
model catalog, and management assets. OAuth refresh updates persist on that PVC.

Since 2026-10-25, ArgoCD owns the formal deployment as Application `cliproxyapi`
in namespace `argocd`. Its wave-5 ApplicationSet element is
`convertor-gitops/argo-cd/argocd-bootstrap/apps/wave-5/cliproxyapi.yaml`; the chart
and pinned image digest live in `convertor-gitops/charts/cliproxyapi` on branch
`feature/argocd`. The Application adopted the existing namespace and OAuth PVC.

Deploy by validating the chart, committing/pushing the digest to that branch,
then running `cd argo-cd && just refresh-sync cliproxyapi` in the GitOps repo.
The ApplicationSet is bootstrapped through the repository's `just update` flow
when adding an element. Do not upgrade/rollback the formal CPA with direct Helm
commands: ArgoCD self-heal follows Git. Roll back the workload by reverting the
GitOps image change and synchronizing. The old manual Helm release history is
historical; do not uninstall it against the adopted resources.

Image rollback does not roll back mutable credential/config files. Take a state
backup before upgrades. Never restore stale OAuth files over newer refreshed
files without stopping the writer and reviewing the restore.

The bootstrap Secret is created outside Git and only seeds missing config/catalog
files. It does not replace live configuration on each restart and does not store
OAuth files. The PVC and namespace have deletion protection annotations; the
storage class uses `Retain`. These protect against accidental removal, not disk
failure. Keep an independent backup. Credentials are ordinary plaintext JSON on
storage; owner-only permissions do not constitute encryption.

Model API: `http://cliproxyapi.cliproxyapi.svc.cluster.local:8317/v1`.
Management is loopback-only, reached using:

```sh
kubectl --context admin@homelab-cluster -n cliproxyapi port-forward --address=127.0.0.1 service/cliproxyapi 18317:8317
```

The ingress NetworkPolicy admits only namespaces `open-webui`, `librechat`, and
`cliproxyapi`. There is no public Ingress or NodePort. On 2026-10-25 the disposable
`rp-lab-20261023` namespace, both test frontends, test MongoDB/data, and the proxy
bridge were deleted. Its legacy client API key was revoked from both live config
and the bootstrap seed; its local test directory/forwarding entrypoints were
retired. The two separate frontend keys remain available for formal integrations.
The existing production OpenWebUI/LibreChat deployments and databases were not
modified or migrated by this retirement.

Create an owner-only backup directory, then run (archives contain live secrets):

```sh
mkdir -m 700 /path/to/private-backups
python3 deploy/backup.py --destination /path/to/private-backups/cpa-state-YYYYMMDD.tar
```

The command refuses an existing archive or a directory readable by others,
checks OAuth JSON and catalog JSON, and reports only provider types/counts and
the archive SHA-256. Archive mode is 0600. No credentials are printed.

## Upstream maintenance procedure

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
