## ADDED Requirements

### Requirement: Read-path methods SHALL return values that share no map or slice storage with internal registries

Every public method on `*Viper` that returns `interface{}`, `map[string]interface{}`, `[]interface{}`, or any nested structure derived from `v.config`, `v.override`, `v.defaults`, or `v.kvstore` SHALL return a deep copy. The returned value MUST be safe for the caller to iterate, marshal, decode, or store from any goroutine without further coordination.

This applies to:
- `Get` and all typed wrappers (`GetString`, `GetStringMap`, `GetStringMapString`, `GetStringMapStringSlice`, `GetStringSlice`, etc.) that may return aggregate values.
- `AllSettings`
- `AllKeys` (returned slice MUST be a copy)
- `Sub` — the returned `*Viper`'s internal `config` MUST NOT alias the parent's storage.
- Any internal helper (`find`, `searchMap`, `searchMapWithPathPrefixes`, `searchIndexableWithPathPrefixes`) used to populate a return value.

Deep copying SHALL be performed *while the read lock is still held*. Once the value is returned, the caller observes a stable snapshot.

#### Scenario: Concurrent Unmarshal and Set do not panic
- **WHEN** goroutine A calls `Unmarshal(&dst)` repeatedly and goroutine B calls `Set("nested.key", "value")` repeatedly on the same `*Viper`, for at least 1000 iterations under `go test -race`
- **THEN** no `concurrent map iteration and map write` panic occurs and no data race is reported

#### Scenario: Concurrent AllSettings iteration and MergeConfig
- **WHEN** goroutine A calls `AllSettings()` and iterates the returned map (including nested maps) while goroutine B calls `MergeConfig(reader)` concurrently
- **THEN** no panic occurs and goroutine A's iteration completes against a stable snapshot

#### Scenario: Get returns a map that is safe to mutate
- **WHEN** a caller invokes `Get("section")` where the value is a `map[string]interface{}`, then mutates the returned map
- **THEN** subsequent calls to `Get("section")` from any goroutine return the original, unmutated value (because the returned map is a copy)

#### Scenario: Sub does not alias parent storage
- **WHEN** `parent.Sub("section")` returns `child`, then `parent.Set("section.key", "new")` is called
- **THEN** `child.Get("key")` returns the value present at the time `Sub` was called, and no concurrent map access panic occurs even if `parent` is being mutated by another goroutine while `child` is iterated

### Requirement: WriteConfig family SHALL hold the write lock for the entire operation

`WriteConfig`, `SafeWriteConfig`, `WriteConfigAs`, `SafeWriteConfigAs`, and the internal `writeConfig` SHALL acquire `v.mu.Lock()` for the full body of the operation, covering:
- the call to `getConfigFile()` (which lazily writes `v.configFile`),
- the call to `marshalWriter()` (which reads `v.config` and reads/writes `v.properties`),
- any access to `v.configType`, `v.fs`, or `v.properties`.

The lock SHALL NOT be released between resolving the filename, marshaling, and writing.

#### Scenario: Concurrent WriteConfig and Set
- **WHEN** goroutine A calls `WriteConfig()` and goroutine B calls `Set("key", "value")` concurrently for at least 1000 iterations under `go test -race`
- **THEN** no data race is reported on `v.config`, `v.properties`, `v.configFile`, `v.configType`, or `v.fs`

#### Scenario: marshalWriter accesses properties under lock
- **WHEN** `marshalWriter` reads or writes `v.properties` during a `WriteConfig*` call
- **THEN** the access occurs while `v.mu` is held in write mode

### Requirement: WatchConfig goroutine SHALL hold the read lock when accessing shared state

The goroutine spawned by `WatchConfig` SHALL acquire `v.mu.RLock()` (or `Lock` where mutation is required) before:
- calling `v.getConfigFile()` (which lazily writes `v.configFile` — therefore requires `Lock`),
- reading `v.configPaths`,
- reading `v.onConfigChange` for the purpose of invoking the callback.

The user-supplied `onConfigChange` callback SHALL be invoked **after** the lock is released, preserving the existing requirement that callbacks not execute while `v.mu` is held.

#### Scenario: WatchConfig reload while Get is in flight
- **WHEN** the file watcher triggers a reload and a separate goroutine is calling `Get("key")` continuously
- **THEN** no `concurrent map read and map write` panic occurs and the reload completes successfully

#### Scenario: getConfigFile from watcher does not race with SetConfigFile
- **WHEN** the watcher goroutine resolves the config file path while another goroutine calls `SetConfigFile("other.yaml")`
- **THEN** no data race is reported on `v.configFile`

### Requirement: Remote-config goroutines SHALL hold the write lock around remoteProviders iteration and unmarshalReader

`getKeyValueConfig`, `getRemoteConfig`, `watchKeyValueConfig`, `watchKeyValueConfigOnChannel`, and `watchRemoteConfig` SHALL hold `v.mu.Lock()` for:
- iteration of `v.remoteProviders`,
- the call to `unmarshalReader` (which writes `v.properties` and reads `v.configType`),
- assignment to `v.kvstore`.

These methods SHALL NOT release the lock between iteration, unmarshal, and assignment.

#### Scenario: Concurrent AddRemoteProvider and remote watcher loop
- **WHEN** goroutine A calls `AddRemoteProvider(...)` and the remote-config watcher goroutine concurrently iterates `v.remoteProviders`
- **THEN** no data race is reported on `v.remoteProviders`

#### Scenario: Remote watcher unmarshalReader and Get are serialized
- **WHEN** the remote-config watcher invokes `unmarshalReader` while a separate goroutine calls `Get("key")`
- **THEN** no `concurrent map read and map write` panic occurs

### Requirement: The package-global Viper pointer SHALL be replaced atomically by Reset

The package-level `v *Viper` (used by package-level wrappers like `Get`, `Set`, `Unmarshal`, etc.) SHALL be stored in `atomic.Pointer[Viper]` (or `atomic.Value` if Go version constraints prevent `atomic.Pointer`). `Reset()` SHALL publish the new instance via a single atomic store. Every package-level wrapper SHALL retrieve the current instance via a single atomic load.

#### Scenario: Concurrent Reset and Get on the package-global
- **WHEN** goroutine A calls `viper.Reset()` and goroutine B calls `viper.Get("key")` concurrently
- **THEN** no data race is reported on the package-global pointer and goroutine B observes either the pre-reset or post-reset instance

### Requirement: A concurrent-stress test suite SHALL exist and run under -race in CI

The repository SHALL contain `viper_concurrent_test.go` with at least the following tests, each running for at least `runtime.GOMAXPROCS(0) * 1000` iterations and exiting cleanly under `go test -race ./...`:

1. `TestConcurrent_GetSet` — N writers calling `Set` and N readers calling `Get` on overlapping keys.
2. `TestConcurrent_SetUnmarshal` — N writers calling `Set` and 1 reader calling `Unmarshal` repeatedly on a struct that exercises nested maps.
3. `TestConcurrent_MergeConfigAllSettings` — 1 goroutine calling `MergeConfig` and N goroutines calling `AllSettings` followed by iteration and decode.
4. `TestConcurrent_WatchConfigGet` — 1 simulated reload (via afero in-memory filesystem) and N readers calling `Get`.
5. `TestConcurrent_AddRemoteProviderWatch` — N callers of `AddRemoteProvider` plus a simulated remote-watcher loop iterating `remoteProviders`.
6. `TestConcurrent_ResetGet` — N goroutines calling package-level `Get` while one goroutine calls `Reset`.

The CI workflow SHALL invoke `go test -race ./...` on every push; the build SHALL fail if any race is detected.

#### Scenario: CI rejects a regression that re-introduces a map race
- **WHEN** a contributor reverts the deep-copy logic in `Get` and pushes a PR
- **THEN** the CI race-detector job reports `DATA RACE` and the PR fails its required checks

#### Scenario: All concurrent tests pass on the fixed code
- **WHEN** `go test -race ./...` is run against the codebase after this change is implemented
- **THEN** the suite completes without `DATA RACE` reports and without `fatal error: concurrent map ...` panics

## REMOVED Requirements

### Requirement: writeConfig SHALL hold RLock when reading config state

**Reason**: Replaced by the stronger requirement "WriteConfig family SHALL hold the write lock for the entire operation". `RLock` is insufficient because `marshalWriter` writes `v.properties`; the operation must hold the write lock for the full duration including marshal and file write.

**Migration**: Implementations that took `RLock` in `writeConfig` MUST switch to `Lock` and extend the lock scope to cover `getConfigFile`, `marshalWriter`, and any `v.properties` access. See requirement "WriteConfig family SHALL hold the write lock for the entire operation" above.
