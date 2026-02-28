### Requirement: All public read methods SHALL hold RLock

Every public method on `*Viper` that reads internal state (maps or scalar fields) SHALL acquire `v.mu.RLock()` before accessing any field and release it via `defer v.mu.RUnlock()`. Methods that already hold `RLock` correctly (`Get`, `IsSet`, `InConfig`, `AllKeys`) SHALL remain unchanged.

Methods that MUST gain `RLock`:
- `ConfigFileUsed` (reads `v.configFile`)
- `getEnv` (reads `v.envKeyReplacer`, `v.allowEmptyEnv`)
- `getConfigType` (reads `v.configType`, `v.configFile`)
- `getConfigFile` (reads `v.configFile`, `v.configName`, `v.configPaths`)

#### Scenario: Concurrent Get while Set is in progress
- **WHEN** goroutine A calls `Get("key")` and goroutine B calls `Set("key", value)` concurrently
- **THEN** no panic occurs, and `Get` returns either the old or new value

#### Scenario: ConfigFileUsed called concurrently with SetConfigFile
- **WHEN** goroutine A calls `ConfigFileUsed()` and goroutine B calls `SetConfigFile("new.yaml")` concurrently
- **THEN** no data race occurs and `ConfigFileUsed` returns either the old or new path

### Requirement: All public write methods SHALL hold Lock

Every public method on `*Viper` that writes internal state SHALL acquire `v.mu.Lock()` before mutating any field and release it via `defer v.mu.Unlock()`. Methods that already hold `Lock` correctly (`Set`, `SetDefault`, `BindFlagValue`, `BindEnv`, `RegisterAlias`, `ReadConfig`, `MergeConfig`) SHALL remain unchanged.

Methods that MUST gain `Lock`:
- `SetConfigFile` (writes `v.configFile`)
- `SetEnvPrefix` (writes `v.envPrefix`)
- `AddConfigPath` (writes `v.configPaths`)
- `AllowEmptyEnv` (writes `v.allowEmptyEnv`)
- `AutomaticEnv` (writes `v.automaticEnvApplied`)
- `SetEnvKeyReplacer` (writes `v.envKeyReplacer`)
- `SetTypeByDefaultValue` (writes `v.typeByDefValue`)
- `SetConfigName` (writes `v.configName`, `v.configFile`)
- `SetConfigType` (writes `v.configType`)
- `SetFs` (writes `v.fs`)
- `OnConfigChange` (writes `v.onConfigChange`)
- `AddRemoteProvider` (writes `v.remoteProviders`)
- `AddSecureRemoteProvider` (writes `v.remoteProviders`)

#### Scenario: Concurrent SetConfigName and ReadInConfig
- **WHEN** goroutine A calls `SetConfigName("app")` and goroutine B calls `ReadInConfig()` concurrently
- **THEN** no data race occurs

#### Scenario: Concurrent AddConfigPath calls
- **WHEN** two goroutines call `AddConfigPath` with different paths concurrently
- **THEN** no data race occurs and both paths are added to `configPaths`

### Requirement: ReadInConfig SHALL hold Lock for its full duration

`ReadInConfig` SHALL acquire `v.mu.Lock()` before reading any fields (`configFile`, `configType`, `fs`) and hold it through file read, unmarshal, and config assignment. It SHALL NOT release the lock between reading metadata fields and assigning the parsed config.

#### Scenario: Concurrent ReadInConfig and Get
- **WHEN** goroutine A calls `ReadInConfig()` (reloading config from file) and goroutine B calls `Get("key")` concurrently
- **THEN** no panic occurs; `Get` either blocks until reload completes or returns the pre-reload value

#### Scenario: ReadInConfig reads configFile and fs atomically
- **WHEN** `ReadInConfig` is called while another goroutine calls `SetConfigFile`
- **THEN** `ReadInConfig` reads a consistent snapshot of `configFile`, `configType`, and `fs`

### Requirement: MergeInConfig SHALL hold Lock for its full duration

`MergeInConfig` SHALL acquire `v.mu.Lock()` before reading metadata fields and hold it through the merge operation, following the same pattern as `ReadInConfig`.

#### Scenario: Concurrent MergeInConfig and Get
- **WHEN** goroutine A calls `MergeInConfig()` and goroutine B calls `Get("key")` concurrently
- **THEN** no panic occurs

### Requirement: Remote config watcher goroutines SHALL hold Lock when mutating maps

`watchKeyValueConfigOnChannel` SHALL acquire `v.mu.Lock()` around the `unmarshalReader` call inside its goroutine. `getKeyValueConfig`, `watchKeyValueConfig`, and `getRemoteConfig` SHALL hold `Lock` when assigning to `v.kvstore`.

#### Scenario: Remote config watcher updates while Get reads
- **WHEN** the remote config watcher goroutine receives an update and calls `unmarshalReader` while another goroutine calls `Get("key")`
- **THEN** no concurrent map read/write panic occurs

#### Scenario: getKeyValueConfig assigns kvstore under lock
- **WHEN** `getKeyValueConfig` assigns `v.kvstore = val`
- **THEN** the assignment is protected by `v.mu.Lock()`

### Requirement: No method SHALL re-acquire mu while already holding it

To prevent deadlocks, no method that holds `v.mu` (either `Lock` or `RLock`) SHALL call another method that also acquires `v.mu`. Internal helpers called from locked methods SHALL assume the caller holds the lock and MUST NOT acquire it themselves.

#### Scenario: ReadInConfig does not deadlock
- **WHEN** `ReadInConfig` holds `Lock` and calls `getConfigFile`, `getConfigType`, and `unmarshalReader`
- **THEN** none of those internal calls attempt to acquire `mu`, and no deadlock occurs

#### Scenario: BindEnv does not deadlock
- **WHEN** `BindEnv` holds `Lock` and calls `mergeWithEnvPrefix`
- **THEN** `mergeWithEnvPrefix` does not attempt to acquire `mu`

### Requirement: WatchConfig SHALL invoke onConfigChange outside the lock

When `WatchConfig` detects a file change and reloads config via `ReadInConfig`, the `onConfigChange` callback SHALL be invoked after the write lock is released. User callback code MUST NOT execute while `mu` is held, to avoid deadlocks if the callback calls `Get` or other Viper methods.

#### Scenario: onConfigChange callback calls Get
- **WHEN** a config file change triggers `WatchConfig` to reload, and the `onConfigChange` callback calls `Get("key")`
- **THEN** no deadlock occurs because the lock was released before the callback

### Requirement: writeConfig SHALL hold RLock when reading config state

`writeConfig` SHALL acquire `v.mu.RLock()` when reading `v.config` and `v.configType` for serialization.

#### Scenario: Concurrent writeConfig and Set
- **WHEN** goroutine A calls `WriteConfig()` and goroutine B calls `Set("key", value)` concurrently
- **THEN** no data race occurs
