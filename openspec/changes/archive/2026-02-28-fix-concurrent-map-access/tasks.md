## 1. Add Lock to setter methods missing locks entirely

- [x] 1.1 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `SetConfigFile`
- [x] 1.2 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `SetEnvPrefix`
- [x] 1.3 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `AddConfigPath`
- [x] 1.4 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `AllowEmptyEnv`
- [x] 1.5 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `AutomaticEnv`
- [x] 1.6 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `SetEnvKeyReplacer`
- [x] 1.7 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `SetTypeByDefaultValue`
- [x] 1.8 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `SetConfigName`
- [x] 1.9 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `SetConfigType`
- [x] 1.10 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `SetFs`
- [x] 1.11 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `OnConfigChange`
- [x] 1.12 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `AddRemoteProvider`
- [x] 1.13 Add `v.mu.Lock()`/`defer v.mu.Unlock()` to `AddSecureRemoteProvider`

## 2. Add RLock to getter methods missing locks

- [x] 2.1 Add `v.mu.RLock()`/`defer v.mu.RUnlock()` to `ConfigFileUsed`

## 3. Fix methods with partial or incorrect locking

- [x] 3.1 Refactor `ReadInConfig` to hold `Lock` for full duration (read fields, file I/O, unmarshal, and config assignment), ensuring internal calls (`getConfigFile`, `getConfigType`) do not re-acquire the lock
- [x] 3.2 Refactor `MergeInConfig` to hold `Lock` for full duration, same pattern as `ReadInConfig`
- [x] 3.3 Refactor `BindEnv` to hold `Lock` before calling `mergeWithEnvPrefix`, so reads of `v.envPrefix` are inside the lock
- [x] 3.4 Add `v.mu.RLock()`/`defer v.mu.RUnlock()` to `writeConfig` around reads of `v.config` and `v.configType`

## 4. Fix remote config methods

- [x] 4.1 Add `v.mu.Lock()` around `v.kvstore = val` assignment in `getKeyValueConfig`
- [x] 4.2 Add `v.mu.Lock()` around `v.kvstore = val` assignment in `watchKeyValueConfig`
- [x] 4.3 Add `v.mu.Lock()` around `unmarshalReader(reader, v.kvstore)` call in `getRemoteConfig`
- [x] 4.4 Add `v.mu.Lock()` around `unmarshalReader(reader, v.kvstore)` call inside the goroutine in `watchKeyValueConfigOnChannel`

## 5. Fix WatchConfig callback safety

- [x] 5.1 In `WatchConfig` goroutine, ensure `onConfigChange` callback is invoked after the write lock from `ReadInConfig` is released (read `v.onConfigChange` under `RLock`, then invoke outside the lock)

## 6. Verify no recursive lock acquisition

- [x] 6.1 Audit call graph: ensure `ReadInConfig` (locked) calls only unlocked internal helpers for `getConfigFile`, `getConfigType`, `unmarshalReader`
- [x] 6.2 Audit call graph: ensure `MergeInConfig` (locked) does not re-acquire `mu` through `MergeConfig` or other locked methods
- [x] 6.3 Audit call graph: ensure `BindEnv` (locked) calls `mergeWithEnvPrefix` which does not acquire `mu`
- [x] 6.4 Audit all other locked methods to confirm no transitive calls re-acquire `mu`

## 7. Testing

- [x] 7.1 Run existing test suite (`go test -race ./...`) and verify no race conditions detected
- [x] 7.2 Add a concurrent test: multiple goroutines calling `Get` while one goroutine calls `Set` in a loop, verify no panic with `-race`
- [x] 7.3 Add a concurrent test: goroutine calling `ReadInConfig` while others call `Get`, verify no panic with `-race`
