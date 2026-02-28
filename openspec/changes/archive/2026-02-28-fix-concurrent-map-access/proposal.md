## Why

Viper panics with `fatal error: concurrent map read and map write` when multiple goroutines call `Get()` while another goroutine writes to internal maps (via `Set()`, `ReadInConfig()`, `WatchConfig()`, or remote config watchers). The existing `sync.RWMutex` is present but not applied consistently across all code paths that read or mutate the internal configuration maps.

## What Changes

- Audit and fix all methods on `*Viper` that access internal maps (`config`, `override`, `defaults`, `kvstore`, `pflags`, `env`, `aliases`) to ensure they hold the appropriate lock (`RLock` for reads, `Lock` for writes)
- Fix `watchKeyValueConfigOnChannel()` which spawns a goroutine that writes to `v.kvstore` via `unmarshalReader` without holding any lock
- Fix `insensitiviseMaps()` which mutates maps in-place and is called from paths that may not hold the write lock
- Fix `WatchConfig()` which calls `ReadInConfig()` on fsnotify events — ensure the config reload path holds the write lock during the full read-and-swap operation
- Ensure methods like `getKeyValueConfig()`, `watchKeyValueConfig()`, and `getRemoteConfig()` that assign to `v.kvstore` hold the write lock
- Fix setter methods (`SetConfigFile`, `SetEnvPrefix`, `AddConfigPath`, `SetConfigName`, `SetConfigType`, `SetFs`, `AllowEmptyEnv`, `AutomaticEnv`, `SetEnvKeyReplacer`, `SetTypeByDefaultValue`) that mutate Viper fields without locks

## Capabilities

### New Capabilities

- `thread-safe-map-access`: Consistent mutex coverage for all internal map reads and writes across the Viper struct

### Modified Capabilities

(none — no existing specs)

## Impact

- **Code**: `viper.go` — all methods accessing internal maps need lock auditing; `watchKeyValueConfigOnChannel` goroutine needs lock wrapping
- **APIs**: No public API changes; this is an internal locking fix
- **Behavior**: Eliminates `concurrent map read and map write` panics under concurrent access
- **Risk**: Low — adding locks to previously-unprotected paths could surface deadlocks if any method re-acquires the mutex recursively (need to verify no recursive lock acquisition exists)
