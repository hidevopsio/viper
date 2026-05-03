## Why

Recent commits (`0952d21`, `b8e37bb`) added a `sync.RWMutex` to `Viper` and bracketed most public read/write entry points with `Lock`/`RLock`. **The crashes have not stopped.** Users still see Go's `fatal error: concurrent map iteration and map write` (and its sibling `concurrent map read and map write`) under load, often emitted *repeatedly* because the `WatchConfig` goroutine and remote-config watchers retry on a timer.

Root cause — the existing fix is treating a symptom, not the underlying contract:

1. **Maps escape the lock by reference.** `Get`, `Sub`, `AllSettings`, and the `searchMap` family return `map[string]interface{}` values that are *direct aliases of the storage in `v.config` / `v.override` / `v.defaults` / `v.kvstore`*. The mutex protects the lookup, then the caller iterates the returned map *after the lock is released*, while another goroutine writing through `Set` / `MergeConfig` / `ReadInConfig` mutates that same nested map. Adding `RLock` inside `Get` cannot prevent this — the returned reference outlives the lock.

2. **`Unmarshal` / `UnmarshalKey` / `UnmarshalExact` rely on those leaked references.** They call `v.AllSettings()` or `v.Get(key)` and then hand the result to `mapstructure.Decode`, which recursively walks every nested map *outside* any lock. The inline comments "// Get already has locking" / "// AllSettings has locking" at `viper.go:837,856,902` document the misconception precisely.

3. **`WatchConfig`, `WriteConfig`, `SafeWriteConfig`, `WriteConfigAs`, and the remote-config goroutines** call `getConfigFile()` (which writes `v.configFile`), iterate `v.configPaths`, and iterate `v.remoteProviders` *with no lock at all*, racing against `AddConfigPath` / `SetConfigFile` / `Add[Secure]RemoteProvider`.

4. **`writeConfig` releases the write lock before `marshalWriter` runs**, then reads `v.config` and read/writes `v.properties` unprotected.

5. **The package-level `v` pointer is replaced by `Reset()` with no synchronization**, racing against every package-level wrapper.

In short: the recent commits added the *right primitive* but applied it to the *wrong boundary*. Locking must extend to **every place a map reference is observed**, which means either (a) deep-copying values on the way out of the lock, or (b) holding the lock for the full duration of the consuming operation (e.g., the entire `Unmarshal`). Until that boundary moves, the data race remains and Go's runtime detector will continue to abort the process.

## What Changes

- **Deep-copy on read at the lock boundary.** `Get`, `Sub`, `AllSettings`, and any path that returns `map[string]interface{}` to a caller MUST return a copy that shares no map or slice storage with the registries. New internal helper `deepCopyValue(interface{}) interface{}` (or equivalent) used by every read path.
- **Hold the lock across the full `Unmarshal*` operation.** Inline the `Get` / `AllSettings` body under a single `RLock` that spans the `mapstructure.Decode` call, OR (preferred, less contention) snapshot a deep copy under `RLock`, release, then decode the copy. Remove the misleading "Get already has locking" comments.
- **Cover the remote / watch / write paths.**
  - `WatchConfig` goroutine: take `RLock` (or compute the filename under `Lock`) before each call to `v.getConfigFile()`; copy `v.onConfigChange` under `RLock` (already done in `b8e37bb`) — keep that and extend to `configFile`/`configPaths` reads.
  - `WriteConfig`, `SafeWriteConfig`, `WriteConfigAs`, `SafeWriteConfigAs`: acquire `Lock` (writer) for the whole body, including `getConfigFile()` and `writeConfig` → `marshalWriter`. Do **not** drop the lock between resolving the filename, marshaling, and writing.
  - `getKeyValueConfig`, `watchKeyValueConfig`, `watchKeyValueConfigOnChannel`, `watchRemoteConfig`: hold the appropriate lock around iteration of `v.remoteProviders` and around `v.kvstore` mutation (today the lock is taken only for the assignment, not the iteration).
- **Fix `writeConfig` lock scope.** Do not unlock between the nil-check at line 1419 and the `marshalWriter` call. `marshalWriter` itself reads `v.config` and read/writes `v.properties` — both must be lock-protected.
- **Protect `v.properties` consistently.** Either include it in the existing `mu` discipline (current direction) or give it its own mutex; `marshalWriter` (1530–1533) currently reads/writes it with no lock.
- **Make package-global `Reset()` safe.** Either guard the global `v` pointer with a dedicated `sync.Mutex` / `atomic.Pointer`, or document `Reset` as not safe for concurrent use and move it behind a build tag / test-only export. **BREAKING** if we restrict `Reset`.
- **Add a race-test suite.** `viper_concurrent_test.go` running under `go test -race` that exercises: concurrent `Set` + `Unmarshal`, concurrent `MergeConfig` + `AllSettings`, concurrent `WatchConfig` reload + `Get`, concurrent `Add*RemoteProvider` + remote watcher loop, concurrent `Reset` + `Get`. Each test must complete without `DATA RACE` reports for at least N iterations.
- Remove the inline comments at `viper.go:837`, `:856`, `:902` ("Get already has locking" / "AllSettings has locking") — they encode the bug.

## Capabilities

### New Capabilities

_None._ This change strengthens existing behavior; it does not introduce new user-facing capability surface.

### Modified Capabilities

- `concurrent-access`: the public read/write contract of `Viper` (Get / Set / Sub / AllSettings / Unmarshal* / WriteConfig* / WatchConfig / Reset / remote-config watchers) is tightened so that **every documented method is safe for concurrent use from multiple goroutines, including iteration of any `map[string]interface{}` value returned to the caller**. The current spec implicitly promises thread-safety via the `RWMutex` field but the contract leaks references; this change closes the contract.

> Note: if no `concurrent-access` spec exists yet under `openspec/specs/`, the specs phase will create it as a new capability spec rather than a delta. The proposal lists it under "Modified" because the *behavioral promise* (thread-safety of public API) already exists in the codebase via the recent mutex commits — we are tightening, not inventing, that promise.

## Impact

- **Affected code** (all under `/home/ide/workspace/viper/viper.go` unless noted):
  - `Get` (693), `find` (~1000–1090), `searchMap` / `searchMapWithPathPrefixes` / `searchIndexableWithPathPrefixes` — return values must be deep-copied at the boundary.
  - `Sub` (738) — `subv.config` must not alias parent.
  - `AllSettings` (1848), `AllKeys` (1767) — return deep copies / stable snapshots.
  - `Unmarshal`, `UnmarshalKey`, `UnmarshalExact` (832–910) — restructure locking; remove misleading comments.
  - `WriteConfig`, `SafeWriteConfig`, `WriteConfigAs`, `SafeWriteConfigAs`, `writeConfig`, `marshalWriter` (1378–1533) — lock for full operation, including `v.properties` access.
  - `WatchConfig` (~280–340) — extend lock coverage to `getConfigFile`, `configPaths` reads.
  - `getKeyValueConfig`, `getRemoteConfig`, `watchKeyValueConfig`, `watchKeyValueConfigOnChannel`, `watchRemoteConfig` (~1690–1760) — lock around `remoteProviders` iteration.
  - `getConfigFile` (1918) — document/enforce that callers hold the lock; OR make the lazy write atomic.
  - Package-globals: `v` (line ~67) and `Reset` (line ~233) — synchronize or restrict.
- **Public API**: no signature changes. Behavior change: callers that *mutated* a map returned by `Get` and expected that mutation to be visible to the next `Get` will no longer see it (because reads now return copies). This is consistent with the documented immutability contract but should be called out in release notes.
- **Performance**: deep copying on every `Get` / `AllSettings` increases allocation. The cost is acceptable for a config library (reads are not in hot loops); benchmark must confirm < 2× regression on `BenchmarkGet`.
- **Dependencies**: none added. Possibly `sync/atomic` for the package-global pointer.
- **Tests**: new `viper_concurrent_test.go`; existing tests must still pass under `-race`.
- **Release notes**: document that `Reset()` is no longer safe to call concurrently with other `Viper` calls (or, if we synchronize it, that there is no behavioral change).
