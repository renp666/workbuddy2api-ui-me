# Upstream patches

Baseline: Sliverkiss/workbuddy2api commit
`c576b489fa22e3c156e960ee6336c4e653a0d95c`. Apply only through
`python3 scripts/overlay.py prepare --output ABS_NEW_DIRECTORY`; never edit
`upstream/`. New source and tests live in `extensions/`, not in these patches.
`series` is the explicit application order: 0001, 0002, 0003, 0004, 0005, 0006, 0007, 0008, 0009, 0010, 0011.
For a deliberate upstream candidate, run
`python3 scripts/overlay.py update --ref COMMIT_OR_TAG`; it keeps the current
snapshot and lock until the candidate passes `scripts/check.sh` and isolated
mock acceptance. Review every patch against the resolved commit before
committing the resulting `upstream/` and `upstream.lock` changes.

| Patch | Purpose and old files changed | Verification after materializing |
| --- | --- | --- |
| 0001-auth-pool-consistency | `internal/auth/auth.go`: locked snapshots/save/replace, stale activation guard and persisted pending marker. `internal/pool/{cooldown,entry,pick,state}.go`: observed-credit flag, pending guards for ordinary/model-exempt/cooldown-fallback selection and status. Install stays an extension and persists before publishing while preserving the live Auth identity. | `go test ./internal/auth ./internal/pool ./internal/upstream` and their race tests |
| 0002-upstream-credential-snapshots | `internal/upstream/{client,global_models,headers,report,trial,travel}.go`: one refresh snapshot including explicit realm, stale-refresh rejection if either token changes, private per-request snapshots, pending chat rejection, cancellation-aware OAuth post-login requests, snapshot retained across billing fallback. `internal/scheduler/{scheduler,travel}.go`: four credential presence reads use snapshots (checkin, keepalive, activity, travel); no scheduler algorithm changes. | `go test ./internal/upstream ./internal/bridge`; `go test -race ./internal/auth ./internal/pool ./internal/upstream ./internal/scheduler ./internal/bridge` |
| 0003-core-wiring | `cmd/server/main.go`: early initialization before LoadDir, late wrapper after existing dependencies/context, wrapped config-not-found handling through errors.Is; opt-in Scheduled callback captures the local Runner before scheduler.New, then initializes history/Runner before scheduler.Run. `cmd/login/main.go`: reuse OAuth client, strict complete-account validation and pending classification. No admin implementation enters the original public Handler. | `go test ./cmd/server ./cmd/login ./internal/bridge ./internal/oauth`; `go test -race ./internal/taskrun ./internal/scheduler ./internal/bridge ./cmd/server` |
| 0004-scheduler-observation | `internal/scheduler/{scheduler,travel,school}.go`: optional scheduled callback, Beijing-time scheduling, short-lived per-instance observations and context-bound scripts using private CN snapshots. `scripts/{school_open_day_2026,task_runner}.py`: fixed structured events at existing result branches, preserving activity algorithms and limits. `scripts/task_common.py`: close the existing read-only auth handle. | `go test ./internal/scheduler`, `go test -race ./internal/scheduler`, `python3 -m unittest discover -s scripts -p test_task_events.py -v` |
| 0005-regression-tests | `internal/server/handler_test.go`: ledger reset time can be omitted when it equals until; accept either representation but retain the one-second timing assertion. `internal/scheduler/school_test.go`: existing fixed-command/dispatch tests use a valid CN pool and context/output-aware fake; empty pools no longer launch a child. Production ledger format is unchanged. | `go test ./internal/server -run TestStatusRateLimitedModelsLedger`, scheduler tests and full suite |
| 0006-waf-ip-failfast-and-usage-sentinel | `internal/upstream/client.go`: `ErrWafBlock` (`waf_block`) plus `IsWafBlocked`/`hasBusinessEnvelope`, evaluated after the 5xx layer so a 403 that still carries a business envelope keeps its existing kind. Same file also carries the upstream `145220d` backport merged into this patch: `Classify` checks `status==429` before `hardRule`, so a rate-limit body that happens to carry quota wording (`quota exceeded`/`额度不足`) classifies as `ErrSoftRate` instead of a next-04:00 `ErrHardCredit` cooldown; 402 still fires first and non-429 quota bodies keep the historical hard-credit kind (sessionDead/accountFault layers stay ahead of the 429 layer). `internal/server/handler.go`: breaker state field, rotation stopped on the threshold hit, and a local readable message only when the upstream body is empty; the streaming log row keeps the `-1` token sentinel when the tail frame has no usage (merged from the usage-sentinel task to keep one patch per handoff decision). The `wafIPGate` state machine itself is an extension file, not part of this patch. Account-level WAF soft cooldown and rotation backoff remain out of scope, so WAF 403 does not cool an account here. | `go test ./internal/upstream ./internal/server` (includes `TestWafIPGate*`, `TestChatWaf*`, `TestClassifyWaf*`, `TestClassify429*`, `TestClassifyNon429QuotaKeepsHardCredit`, `TestClassify402StillFirst`, `TestChatLogsStreamRowNoUsageShowsDash`), `go test -race ./internal/server ./internal/upstream` |
| 0007-session-gc-stop-race | `internal/session/session.go`: `StartGC` captures the stop channel into a local `stop` and the GC goroutine selects on that local instead of re-reading the shared `r.stop` field each loop. Fixes the upstream `2b8dba0` backport: a `StopGC` write to `r.stop` (setting it to `nil` after `close`) raced with the goroutine's unlocked read, so a goroutine that observed `nil` turned its `case <-r.stop` into a nil channel that never fires and leaked one goroutine per Start/Stop cycle. `StopGC` is unchanged; the goroutine simply no longer touches the shared field. The regression test is an extension file (`extensions/internal/session/session_gc_race_test.go`), not part of this patch. | `go test -race ./internal/session` (includes `TestStopGCStopsGoroutine`, `TestStartGCIdempotentAndRestartable`); red state must show `DATA RACE` plus a goroutine-leak `baseline!=now` on the pre-patch tree |
| 0008-call-usage-log | `internal/server/handler.go`: the two success paths (stream tail after the cost-ledger block, sync after `usageCreditTotal`) hand one `usagelog.RecordAuth` observation each — account, model, mode, prompt/completion tokens and explicit `usage.credit` — into the extension package `internal/usagelog` (`extensions/internal/usagelog/`, materialized copy), which appends per-day JSONL under the core data dir `usage/` and serves console queries through bridge `GET /internal/v1/usage`. Missing usage is a `-1` sentinel and missing credit stays absent (never zero); failed requests record nothing because rotation failures carry no usage observation. Wiring happens once in `wrapCore` (`extensions/cmd/server/extension.go`); with no ledger the calls are no-ops and the bridge endpoint returns 503. | `go test ./internal/usagelog ./internal/server ./internal/bridge` (includes `TestHandlerRecordsUsageLogForStreamAndSync`, `TestUsageEndpointValidatesAndSummarizes`, `TestUsageRangeBounds`), `go test -race ./internal/server ./internal/bridge` |
| 0009-model-availability-filter-and-account-attribution | `internal/server/handler.go`: `modelList` lists a model only when the pool holds at least one selectable same-realm account for it (static health view via extension `pool.AvailableAccountsForModelRealm` — disabled/account cooldown/breaker/6004 model cooldown, in-flight full excluded so the list never flickers with concurrency); a realm with zero available accounts disappears entirely, and the global branch is gated before any upstream probe (zero external calls). Every entry carries a `"realm"` tag (`cn`/`global`; console adds `glm` for the optional zcode upstream) and an `"accounts"` list (`uid` plus optional display `nickname`, UID-ascending, same static health view) so multi-account pools can see which accounts serve each model. Both `chatCompletions` success paths set `X-Account` (nickname, falling back to UID when not printable ASCII) and `X-Account-Realm` via extension `account_header.go`; sanitized against header injection, never set on the error-normalization path. Test files `handler_effort_models_test.go`, `handler_global_models_test.go`, `handler_global_test.go` move with the new semantics (empty pool lists nothing; no global account hides the global realm; `TestModelsGlobalListGating` uses a healthy g1 plus a failing probe fake). | `go test ./internal/server ./internal/pool ./internal/anthropic ./internal/bridge` (includes `TestModelListHidesFullyCooledCNModels`, `TestModelListEmptyPoolHidesAllStaticCN`, `TestModelListDisabledAccountHidesRealm`, `TestModelListRealmTagGlobal`, `TestModelListCarriesAccountRefs`, `TestChatSuccessCarriesAccountHeaders`, `TestChatErrorPathKeepsAccountHidden`, `TestSetAccountHeadersSanitizes`, `TestSuccessPathForwardsAttributionHeaders`, `TestStreamSuccessPathForwardsAttributionHeaders`) |
| 0010-account-pin-lock | `internal/server/handler.go`: optional `Config.Pinned Pinner` (`PinnedUID(realm) string`, nil-safe). When a realm is pinned, `chatCompletions` selects only that UID via `PickByUIDForModel` plus a realm cross-check — pin takes precedence over session stickiness, and no sticky binding is written while pinned. Disabled/cooldown/breaker/6004-model-cooldown/in-flight-full, CAS loss, token-refresh failure, network error, and upstream 4xx all break the rotation with an internal `errPinnedUnavailable` sentinel instead of trying another account; the error-normalization tail maps it to fixed `503 pinned_account_unavailable` with a Chinese explanation and never leaks raw upstream text. Unpinned realms keep the original rotation/stickiness path untouched. `cmd/server/main.go`: opens `pin.json` next to the state file (fail-closed on corrupt JSON/invalid fields, no silent wipe), injects the store into the handler config and `wrapCore`. Companion extension files: `internal/pin/pin.go` (atomic 0600 persistence, per-realm cn/global map, validation), `internal/bridge/pin.go` (`GET/POST /internal/v1/pin`, `POST /internal/v1/unpin`; realm is reverse-resolved from the pool so a pin POST cannot forge a realm), `internal/server/pin_lock_test.go` (end-to-end strict-pin regressions). | `go test ./internal/pin ./internal/bridge ./internal/server` (includes `TestPinnedRequestOnlyUsesPinnedAccount`, `TestPinnedUnavailableRejectsWithoutTouchingOthers`, `TestPinnedUpstreamFailureNeverRotates`, `TestUnpinRestoresAutomaticRotation`) plus the pin-package persistence/corrupt-file tests and the console pin UI tests in `console/web_test.cjs` |
| 0011-model-credit-listing | `internal/upstream/client.go`: `ModelInfo.Credits`; `FetchModels` parses the upstream top-level `credits` (env/dynEntry/out) and writes non-empty values into a per-realm `credits` bucket (`creditsSnapshot`/`storeCredits`/`GlobalCreditSnapshot`, sharing `effortsMu`). `internal/upstream/global_models.go`: the file-header note that credits never enter this package is narrowed to a **read-only pass-through**; `globalModelEntry.Credits`, `parseGlobalModelNames`/`probeGlobalModels`/`globalModelsOnce` return a fifth `credits` bucket, and `FetchGlobalModels` writes the global bucket (its public `[]string` signature is unchanged). `internal/server/handler.go`: `modelList` adds `entry["credits"]` on the CN dynamic branch and on the global branch (static fallback stays credit-less). `credits` is the upstream **pricing rule** string (e.g. `"x0.00 credits"` = limited-time free, `"x0.79"` = multiplier), not an accumulated ledger; it is display-only, never injected into cost tier, selection or scheduling, and a missing value omits the field (console shows 未知). | `go test ./internal/upstream ./internal/server` (includes `TestFetchModelsParsesCredits`, `TestParseGlobalModelNamesCredits`, `TestFetchGlobalModelsStoresCreditBucket`, plus the existing effort/global listing regressions) |

Remove each patch only when the pinned upstream supplies the corresponding
behavior and the named regression tests pass without that patch. For 0003,
upstream must provide equivalent early/late integration hooks and shared OAuth
validation; for 0005, upstream's test must accept its existing omitted reset_at
representation. Re-evaluate patches individually on an explicit upstream update.

Remove 0004 when upstream supplies equivalent scheduling callbacks, safe account
snapshots and explicit task results. A script exit code is not a per-account
success: nonzero exit preserves explicit events, fails accounts without events,
and returns a fixed process error for the run aggregator. Only explicit reward
fields are collected. Go TravelClaim cannot distinguish omitted reward from
zero, so both remain null. Ordinary script output is replaced with a bounded
omission summary; no process-wide logger is captured. The original auth loader
must remain read-only (the extension test rejects write access).

Remove 0006 when the pinned upstream supplies the `ErrWafBlock` classification,
the IP-level fail-fast breaker, the streaming usage sentinel and the `145220d`
429-before-hardRule ordering, and those named tests pass with the patch dropped
while `extensions/internal/server/wafip.go` moves into the upstream tree.
Half-applying it is not an option: the breaker counts on `ErrWafBlock`, so the
classification and the rotation break share one removal condition; the 429
ordering is verified by `TestClassify429*`/`TestClassifyNon429QuotaKeepsHardCredit`
and must be supplied by upstream before the whole patch can go. Dropping the
patch changes only the observable behaviour named in its tests and does not
touch 0001-0005.

Remove 0007 when the pinned upstream `StartGC` captures the stop channel into a
local and the GC goroutine selects on that local (upstream `2b8dba0`), and
`go test -race ./internal/session` passes with the patch dropped while
`extensions/internal/session/session_gc_race_test.go` still holds. The patch is
two lines of production code; its regression test is an extension file, so the
removal check is the race test alone, independent of 0001-0006.

Remove 0008 when the pinned upstream records a per-call usage observation
(account, model, prompt/completion tokens, explicit credit) on both success
paths with the same missing-vs-zero semantics, and the named tests pass with
the patch dropped while `extensions/internal/usagelog/` moves into the upstream
tree. Dropping it only stops call-statistics recording; the cost ledger and
rotation behaviour from 0001-0007 are untouched.

Remove 0009 when the pinned upstream `modelList` already filters by per-model
account availability with the same static health view, tags every entry with a
realm and an accounts list, and sets the two attribution headers on both chat
success paths only, with the named tests passing with the patch dropped while
`extensions/internal/pool/availability.go` and
`extensions/internal/server/account_header.go` move into the upstream tree.
Dropping it restores the previous always-list behaviour and empty-pool static
list; the anthropic adapter header passthrough and the console `glm` realm tag
are extension/console code and stay valid either way.

Remove 0010 when the pinned upstream supplies an equivalent deployment-scoped
per-realm account pin with strict (no-failover) selection semantics, persisted
next to the state file with fail-closed loading and a management API, and the
named tests pass with the patch dropped while `extensions/internal/pin/` and
`extensions/internal/bridge/pin.go` move into the upstream tree. Dropping it
restores automatic rotation even when a pin record exists; the console pin UI
then loses its backend and must be removed in the same change. `pin.json` is a
new data-dir file, not a migration of any existing format.

Remove 0011 when the pinned upstream itself outputs the per-model `credits`
pricing rule in its model listing (or the product no longer needs the credit
column and the multiplier-driven default switch), and `go test
./internal/upstream ./internal/server` passes with the patch dropped. It only
adds a read-only display field: dropping it removes the `credits` key from
`/v1/models` and changes nothing in selection, scheduling or the cost ledger,
so 0001-0010 stay valid either way. Console reads `credits` defensively, so it
degrades to 未知 rather than breaking when the field is absent.

Task 2 wiring handoff: `initializeCore(*Config) error` validates opt-in
`WB2A_BRIDGE_KEY` and creates account/state directories before loading accounts.
`wrapCore(ctx,cfg,p,up,sch,public,tasks,history,taskError,pins)` uses the same pool/upstream/scheduler; absent
bridge key returns the identical source-mode public handler. Task 4 extends the
early hook with persisted keys and Docker opt-in, not a second pool or scheduler.
Build metadata comes from linker variables `main.upstreamCommit` and
`main.patchIdentity`; empty source-build values mean unknown.

Task 6 wiring: `coreTaskSchedule(&tasks)` remains nil without Docker/bridge opt-in.
`newCoreTasks(ctx,cfg,sch)` opens `tasks/runs.json` alongside the pool state and
binds the same scheduler's catalog/executor to the core lifecycle. Corrupt history
leaves the opt-in callback installed but skipping execution, and passes TaskError
to the bridge without stopping the public handler. Task 7 owns HTTP task routes
and their 503 mapping. Remove this part of 0003 only when upstream provides an
equivalent durable single-runner hook, including fail-closed scheduled execution.
