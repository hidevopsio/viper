## Context

Viper uses a single `sync.RWMutex` (`mu`) on the `Viper` struct to protect concurrent access to its internal maps (`config`, `override`, `defaults`, `kvstore`, `pflags`, `env`, `aliases`) and scalar fields. Many public methods already acquire the appropriate lock, but an audit reveals several gaps where fields are read or written without holding any lock. The `concurrent map read and map write` panic occurs when an unlocked writer races with a locked reader.

### Current locking audit

**Properly locked methods** (no changes needed):
- `Get`, `IsSet`, `InConfig`, `AllKeys` — hold `RLock`
- `Set`, `SetDefault`, `BindFlagValue`, `BindEnv`, `RegisterAlias`, `ReadConfig`, `MergeConfig` — hold `Lock`
- `ReadInConfig` — locks around the `v.config` assignment (but see gap below)

**Methods missing locks entirely** (read or write fields without any lock):
- `SetConfigFile` — writes `v.configFile`
- `SetEnvPrefix` — writes `v.envPrefix`
- `AddConfigPath` — reads/writes `v.configPaths`
- `AllowEmptyEnv` — writes `v.allowEmptyEnv`
- `AutomaticEnv` — writes `v.automaticEnvApplied`
- `SetEnvKeyReplacer` — writes `v.envKeyReplacer`
- `SetTypeByDefaultValue` — writes `v.typeByDefValue`
- `SetConfigName` — writes `v.configName`, `v.configFile`
- `SetConfigType` — writes `v.configType`
- `SetFs` — writes `v.fs`
- `ConfigFileUsed` — reads `v.configFile`
- `OnConfigChange` — writes `v.onConfigChange`
- `AddRemoteProvider` / `AddSecureRemoteProvider` — read/write `v.remoteProviders`
- `getEnv` — reads `v.envKeyReplacer`, `v.allowEmptyEnv`
- `getConfigType` — reads `v.configType`, `v.configFile`
- `getConfigFile` — reads/writes `v.configFile`

**Methods with partial or incorrect locking**:
- `ReadInConfig` — reads `v.configFile`, `v.configType`, `v.fs` outside the lock, only locks for the final `v.config` assignment
- `MergeInConfig` — reads `v.configFile`, `v.configType`, `v.fs` outside the lock
- `WatchConfig` — spawns a goroutine that calls `v.ReadInConfig()` and reads `v.onConfigChange` without locks
- `watchKeyValueConfigOnChannel` — spawns a goroutine that calls `v.unmarshalReader(reader, v.kvstore)` which mutates `v.kvstore` without any lock
- `getKeyValueConfig` — writes `v.kvstore = val` without lock
- `watchKeyValueConfig` — writes `v.kvstore = val` without lock
- `getRemoteConfig` — passes `v.kvstore` to `unmarshalReader` (mutates it) without lock
- `AllSettings` — calls `v.AllKeys()` and `v.Get()` in a loop, each acquiring their own lock separately, but the overall iteration is not atomic
- `writeConfig` — reads `v.config`, `v.configType` without lock
- `Sub` — calls `v.Get()` (locked) but then accesses the returned map — safe if caller doesn't hold the lock

## Goals / Non-Goals

**Goals:**
- Eliminate all `concurrent map read and map write` panics by ensuring every access to Viper's internal maps and shared fields is protected by `mu`
- Maintain the existing `sync.RWMutex` strategy — no new lock types or `sync.Map`
- Keep the public API unchanged
- Avoid deadlocks by ensuring no method re-acquires `mu` while already holding it

**Non-Goals:**
- Making individual operations atomic at a higher level (e.g., `AllSettings` iterating atomically is not required — it already wasn't)
- Switching to `sync.Map` or other concurrent data structures
- Refactoring the file structure or splitting `viper.go`
- Adding benchmarks or performance optimization

## Decisions

### Decision 1: Use the existing `sync.RWMutex` — do not introduce `sync.Map`

**Rationale:** `sync.Map` is optimized for two patterns (stable key sets with mostly reads, or disjoint key sets per goroutine). Viper's maps are deeply nested `map[string]interface{}` — `sync.Map` only protects top-level access and would not prevent races on nested maps. The existing `RWMutex` is the correct tool; it just needs consistent application.

**Alternative considered:** Per-map `sync.RWMutex` (one lock per internal map). Rejected because the `find()` method reads multiple maps in priority order within a single call — separate locks would require acquiring multiple locks and increase deadlock risk without meaningful concurrency benefit.

### Decision 2: Split methods into locked public API and unlocked internal helpers

**Rationale:** Several methods call other methods that also acquire locks (e.g., `AllSettings` calls `Get` in a loop). To avoid recursive lock acquisition (which causes deadlocks with `sync.RWMutex`), the pattern is:
- Public methods acquire the lock and call an unlocked internal helper
- Internal helpers (prefixed or unexported) assume the caller holds the lock

For example, `ReadInConfig` should hold `Lock` for its entire duration and call an internal `unmarshalReader` that assumes the lock is held.

**Methods needing this split:**
- `ReadInConfig` → hold `Lock` throughout, call internal helpers that don't re-lock
- `MergeInConfig` → same pattern
- `BindEnv` → currently reads `v.envPrefix` (via `mergeWithEnvPrefix`) outside the lock, then locks to write `v.env`; should lock first

### Decision 3: Lock all setter/getter methods for scalar fields

**Rationale:** Even though scalar field writes are often "atomic" at the hardware level for simple types, Go's memory model requires synchronization for concurrent access. All setter methods (`SetConfigFile`, `SetEnvPrefix`, etc.) must hold `Lock`, and all getter methods reading these fields must hold `RLock`.

### Decision 4: Wrap the `watchKeyValueConfigOnChannel` goroutine with locks

**Rationale:** This goroutine calls `v.unmarshalReader(reader, v.kvstore)` in a loop, mutating `v.kvstore` without any synchronization. The fix is to acquire `v.mu.Lock()` around the `unmarshalReader` call inside the goroutine.

### Decision 5: Keep `WatchConfig` calling `ReadInConfig` — ensure `ReadInConfig` is self-contained with locking

**Rationale:** `WatchConfig` spawns a goroutine that calls `ReadInConfig` on file change events. Rather than adding locking in `WatchConfig`, ensure `ReadInConfig` acquires the write lock for its full duration (file read + unmarshal + config swap). The `onConfigChange` callback should be called after releasing the lock to avoid holding the lock during user code execution.

## Risks / Trade-offs

**[Increased lock contention]** → Adding locks to previously-unlocked setter methods (called during init) adds minimal overhead since these are typically called during single-threaded setup. The hot path (`Get`/`find`) already uses `RLock` correctly.

**[Potential deadlocks from recursive locking]** → `sync.RWMutex` is not reentrant. If a locked method calls another method that also locks, it will deadlock. Mitigated by the locked/unlocked helper split (Decision 2). Must verify the full call graph before implementation.

**[`WatchConfig` holding lock during file I/O]** → `ReadInConfig` will now hold the write lock while reading the file from disk, which could block readers briefly. Acceptable because config reloads are infrequent events, and correctness takes priority over read availability during reload.

**[`AllSettings` non-atomic iteration]** → `AllSettings` calls `Get` per key in a loop. Making this fully atomic would require holding `RLock` for the entire iteration, which is a behavioral change. Current approach is acceptable since config changes during iteration are rare and partial staleness is tolerable.
