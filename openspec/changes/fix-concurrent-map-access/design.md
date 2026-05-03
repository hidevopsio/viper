## Context

`Viper` is a singleton-style configuration registry holding several `map[string]interface{}` fields (`config`, `override`, `defaults`, `kvstore`) plus auxiliary maps (`pflags`, `env`, `aliases`) and shared state (`configFile`, `configPaths`, `remoteProviders`, `properties`, `onConfigChange`).

Two recent commits added `sync.RWMutex mu` to `Viper` and bracketed most public methods with `Lock`/`RLock`:

- `0952d21` — introduced the mutex; locked `Get`, `Set`, `IsSet`, `SetDefault`, `RegisterAlias`, `BindFlagValue`, `BindEnv`, `InConfig`, `AllKeys`, `ReadConfig`, `MergeConfig`, partial `ReadInConfig`, partial `Unmarshal*`.
- `b8e37bb` — broadened to `OnConfigChange`, `WatchConfig` (callback copy), simple-field setters/getters, and inlined `MergeInConfig` to avoid re-entrant lock. Bracketed `getKeyValueConfig` / `getRemoteConfig` / `watch*Config` around `kvstore` mutation only.

Despite this, production users still see `fatal error: concurrent map iteration and map write` (and its `read` sibling), often emitted in a tight loop because the watcher goroutines retry. The mutex *does* serialize the function calls; it does not serialize *use of the values they return*.

The existing tests do not run under `-race`. There is no concurrent-stress test suite. The bug is therefore invisible to CI even though it is reliable in production.

## Goals / Non-Goals

**Goals:**

- Eliminate every `concurrent map iteration and map write` / `concurrent map read and map write` panic from the public API surface, including `Get`, `Sub`, `AllSettings`, `AllKeys`, `Unmarshal`, `UnmarshalKey`, `UnmarshalExact`, `WriteConfig*`, `WatchConfig`, and the remote-config watcher loops.
- Make the *value* returned by every read-path method safe to use after the lock is released — i.e., callers may iterate, marshal, decode, or store the result without coordinating with the registry.
- Make the package-global `v` pointer and `Reset()` safe (or explicitly document/restrict `Reset` as not safe for concurrent use).
- Add a `-race`-clean concurrent stress suite covering the matrix of read × write × watch × remote-config × reset.
- Keep the public API signatures unchanged.

**Non-Goals:**

- Lock-free or sharded re-architecture. A single `RWMutex` plus copy-on-read is sufficient and matches the existing direction.
- Fixing performance regressions for callers that mutated returned maps in place expecting writes to be visible. That pattern was never documented as supported and is now explicitly broken.
- Reworking the public API (e.g., introducing a `Snapshot()` method) — additive surface is out of scope here; revisit in a follow-up if benchmarks demand it.
- Changing the `mapstructure` dependency or the encoder/decoder pipeline.

## Decisions

### D1. Deep-copy map/slice values at the lock boundary on read

**Decision:** Introduce an internal helper `deepCopyValue(v interface{}) interface{}` that recursively clones `map[string]interface{}`, `map[interface{}]interface{}`, and `[]interface{}` (other types pass through). Every read path that returns a value to a caller — `Get`, `Sub`, `AllSettings`, the value `find()` returns when it could be a map/slice — runs the result through `deepCopyValue` *while still holding the read lock*.

**Why:** This severs the alias between caller-visible values and internal storage. Once severed, the caller can iterate / decode / marshal the returned value at any time without coordinating with `Viper`. It also fixes `Unmarshal*` automatically because they consume `v.Get(key)` / `v.AllSettings()`.

**Alternatives considered:**

- *Hold the lock across the entire `Unmarshal*` / decode call.* Rejected: `mapstructure.Decode` invokes user-supplied decode hooks; running those under a mutex risks user re-entrancy / deadlock and starves writers for arbitrarily long during decoding of large structs.
- *Copy-on-write of the registries themselves.* Rejected: requires reworking every write path to publish a new map atomically and would be a bigger refactor than the bug warrants.
- *Document maps as read-only and rely on caller discipline.* Rejected: cannot prevent `mapstructure.Decode`'s internal iteration from racing with concurrent writers, and the panic is in the Go runtime, not user code.

### D2. Hold the write lock across `WriteConfig*` and across `marshalWriter`

**Decision:** `WriteConfig`, `SafeWriteConfig`, `WriteConfigAs`, `SafeWriteConfigAs`, and the internal `writeConfig` acquire the write lock for the *entire body*, including `getConfigFile()`, `marshalWriter()`, and any `v.properties` access. Drop the current pattern of acquiring the lock just for the nil-check and releasing it before marshaling.

**Why:** `marshalWriter` reads `v.config` and read/writes `v.properties`. The current `writeConfig` releases the lock at line 1423 before calling `marshalWriter`, leaving these reads unprotected. Writers are infrequent for config writing; lock-for-duration is acceptable.

**Alternatives considered:**

- *Snapshot under lock, marshal outside.* Possible but more complex than holding the lock; deferred unless benchmarks show a hot path. Config writes are not hot paths.

### D3. Lock the `WatchConfig` and remote-config goroutines around all shared reads

**Decision:**

- The `WatchConfig` event loop already copies `v.onConfigChange` under `RLock`. Extend that to also resolve `v.getConfigFile()` under the lock (because `getConfigFile` lazily writes `v.configFile`) and to read `v.configPaths` under the lock when used.
- `getKeyValueConfig`, `getRemoteConfig`, `watchKeyValueConfig`, `watchKeyValueConfigOnChannel`, `watchRemoteConfig` currently take the write lock only around the `kvstore = ...` assignment. Extend the lock to cover iteration of `v.remoteProviders` and the call to `unmarshalReader` (which writes `v.properties`).

**Why:** These are the goroutines that retry continuously — they are the most likely source of the *repeated* panic users report.

### D4. Synchronize the package-global `v` and `Reset()`

**Decision:** Use `atomic.Pointer[Viper]` (Go 1.19+) for the package-global. `Reset()` performs a single `Store`; every package-level wrapper does a single `Load`. If Go version constraints prevent `atomic.Pointer`, use `sync/atomic` `Value` or guard with a `sync.RWMutex`.

**Why:** `Reset()` mutates a word-sized pointer that other goroutines may be concurrently dereferencing. Even if the load is atomic on amd64, the Go memory model requires explicit synchronization. Using `atomic.Pointer` makes the contract explicit and is essentially free on the read path.

**Trade-off:** Adds one atomic load per package-level call. Negligible.

**Alternative considered:** Document `Reset` as not safe for concurrent use and add a runtime panic if called while other operations are in flight. Rejected as user-hostile; many tests rely on `Reset`.

### D5. Remove misleading inline comments

**Decision:** Delete the comments `// Get already has locking` (`viper.go:837`) and `// AllSettings has locking` (`viper.go:856,902`). They encode the bug-causing assumption that locking the lookup is sufficient.

**Why:** Documentation drift causes future contributors to repeat the mistake.

### D6. Add `viper_concurrent_test.go` and run the whole suite under `-race` in CI

**Decision:** New file with t.Parallel sub-tests covering:

1. N goroutines calling `Set` + N goroutines calling `Get` on overlapping keys.
2. N goroutines calling `Set` + 1 goroutine calling `Unmarshal` repeatedly.
3. 1 goroutine doing `MergeConfig` + N goroutines doing `AllSettings` + decode.
4. 1 `WatchConfig` simulated reload (rewrite the file via afero mem-fs) + N `Get`.
5. N `AddRemoteProvider` + simulated remote-watcher loop.
6. N `Get` + 1 `Reset()` (tests D4).

Each test runs for `runtime.GOMAXPROCS(0) * 1000` iterations (or a time budget) and must terminate cleanly under `go test -race ./...`.

**Why:** The bug is invisible to the current suite. Without a regression test, the fix will silently regress.

## Risks / Trade-offs

- **[Performance regression on read path]** → Deep-copy adds allocation proportional to nested map size. Mitigation: benchmark `BenchmarkGet` and a new `BenchmarkAllSettings`; require < 2× regression. If exceeded, switch read paths to lazy copy-on-iterate via a wrapper type, scoped to a follow-up change.
- **[Behavioral break for callers that mutated returned maps]** → Such callers will silently lose their writes. Mitigation: release notes call this out explicitly. The previous behavior was unspecified and unsafe; the contract is now "returned values are owned by the caller, not the registry".
- **[Lock contention on `WriteConfig*` under load]** → Holding the write lock through marshaling could stall readers during a slow disk write. Mitigation: writes are rare in normal operation; if measurements show contention, switch to snapshot-then-marshal in a follow-up. Documented as a known trade-off.
- **[`atomic.Pointer` requires Go 1.19+]** → Project already restricts Go version (commit `ee7ee79`). Verify the floor; if < 1.19, use `atomic.Value` storing `*Viper`.
- **[Decode hooks running under deep-copied maps may behave differently if they relied on identity]** → Extremely unlikely; decode hooks operate on values, not identity. No mitigation planned beyond the race-test suite catching surprises.
- **[`-race` doubles CI time]** → Acceptable for a config library. Limit to a single matrix entry if needed.

## Migration Plan

1. Land the fix behind no flag — this is a bug fix to internal locking and one documented behavior change (returned-map ownership).
2. Order of merge:
   1. `viper_concurrent_test.go` first (will fail under `-race`, demonstrating the bug). Land in a separate commit so the regression is captured in history.
   2. Implementation commits per spec requirement (deep-copy, write-lock scope, watch-loop locking, package-global atomic).
   3. CI workflow update to add `go test -race ./...`.
3. Rollback: pure revert. No data migrations, no on-disk format changes.
4. Release: minor version bump. CHANGELOG entry under "Fixed" with explicit note about returned-map ownership.

## Open Questions

- What is the project's minimum Go version after `ee7ee79`? Confirms whether `atomic.Pointer[T]` is available or we need `atomic.Value`.
- Should `Reset()` remain a public API at all? Several other config libraries have moved it behind test-only build tags. Defer to maintainers; default behavior of this change is to keep it and synchronize it.
- Is there a `Sub(...)` use case where callers *want* a live view into the parent? If so we need an additive `LiveSub` API; otherwise current `Sub` semantics (independent copy) are fine.
- Should `AllSettings` cache its result while no writes have occurred (versioned snapshot)? Optimization, deferred.
