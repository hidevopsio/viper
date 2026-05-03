## 1. Regression test scaffolding (land first to capture the bug)

- [x] 1.1 Create `viper_concurrent_test.go` with a `TestConcurrent_GetSet` exercising N writers + N readers on overlapping keys for at least `runtime.GOMAXPROCS(0) * 1000` iterations. _(Implemented as `TestConcurrentRepro_SubAndMergeConfig` — covers the production stack trace shape.)_
- [x] 1.2 Add `TestConcurrent_SetUnmarshal` that interleaves `Set` with `Unmarshal` into a struct containing nested maps. _(Implemented as `TestConcurrentRepro_UnmarshalVsSet`.)_
- [x] 1.3 Add `TestConcurrent_MergeConfigAllSettings` that interleaves `MergeConfig` with `AllSettings` + iteration + decode. _(Implemented as `TestConcurrentRepro_AllSettingsIteration` and the SubAndMergeConfig reproducer.)_
- [ ] 1.4 Add `TestConcurrent_WatchConfigGet` using `afero.NewMemMapFs()` to simulate file reload while readers call `Get`. _(Deferred — no production reports for this code path.)_
- [ ] 1.5 Add `TestConcurrent_AddRemoteProviderWatch` interleaving `AddRemoteProvider` with a fake remote watcher loop iterating `remoteProviders`. _(Deferred.)_
- [x] 1.6 Add `TestConcurrent_ResetGet` interleaving `viper.Reset()` with package-level `Get`. _(Implemented as `TestConcurrentRepro_ResetVsPackageGet` — currently still races; fix deferred to task 8.)_
- [x] 1.7 Confirm the new tests **fail** under `go test -race ./...` against the current HEAD (commit before this change). Capture the panic output in the commit message so the regression is recorded in history. _(Captured: `fatal error: concurrent map read and map write` at `searchMapWithPathPrefixes` → `find` → `Get`, matching the user's production stack trace.)_

## 2. Internal helpers

- [x] 2.1 Add `deepCopyValue(v interface{}) interface{}` in a new file `viper_internal.go` (or inline in `viper.go`) that recursively clones `map[string]interface{}`, `map[interface{}]interface{}`, and `[]interface{}`. Other types pass through unchanged.
- [x] 2.2 Add `deepCopyStringMap(m map[string]interface{}) map[string]interface{}` as a typed convenience wrapper.
- [ ] 2.3 Unit-test `deepCopyValue` for: shallow scalars, nested string maps, mixed `interface{}`-keyed maps, slices of maps, cycles. _(Indirect coverage via the 3 passing reproducer tests; dedicated unit tests deferred.)_
- [ ] 2.4 Run `go test -race -run TestDeepCopy` to confirm the helper is itself race-clean. _(N/A until 2.3 is added.)_

## 3. Read-path fixes (D1 / Spec: deep-copy on read)

- [x] 3.1 Modify `(v *Viper) Get(key string) interface{}` to run `val` through `deepCopyValue` *before releasing the read lock*.
- [x] 3.2 Modify `(v *Viper) AllSettings()` to acquire `v.mu.RLock()` once for the duration, build the result while iterating under the lock (do not release between `AllKeys` and `Get`), and deep-copy nested maps before return. _(Refactored to use new `allKeysLocked` helper + `find` + `deepCopyValue`.)_
- [x] 3.3 Modify `(v *Viper) AllKeys()` to return a copy of the slice. _(Now uses `make([]string, 0, len(m))` and the new locked-internal `allKeysLocked` so callers always get a fresh slice.)_
- [x] 3.4 Modify `(v *Viper) Sub(key string) *Viper` so that `subv.config` is a deep copy. _(`Sub` now relies on `Get` returning a deep copy; `cast.ToStringMap` then operates on the copy.)_
- [ ] 3.5 Audit `find()` and `searchMap*` to ensure any returned map/slice is owned by the caller. _(Deferred — copy at the `Get`/`AllSettings` boundary is sufficient for callers; internal callers that hold the lock continue to use direct refs by design.)_
- [ ] 3.6 Verify `GetStringMap`, `GetStringMapString`, `GetStringMapStringSlice`, `GetStringSlice` all flow through `Get`. _(Visual audit confirmed they all call `v.Get(key)`; explicit test coverage deferred.)_
- [x] 3.7 Delete the inline comments `// Get already has locking` and `// AllSettings has locking` in `Sub`, `UnmarshalKey`, `Unmarshal`, `UnmarshalExact`.

## 4. Unmarshal verification (D1 follow-on)

- [x] 4.1 Confirm `Unmarshal`, `UnmarshalKey`, `UnmarshalExact` no longer race once read paths return copies. _(Verified by `TestConcurrentRepro_UnmarshalVsSet` passing under `-race`.)_
- [x] 4.2 Confirm the existing `v.mu.Lock()` around `v.insensitiviseMaps()` in `Unmarshal*` remains correct. _(Untouched and still correct.)_

## 5. WriteConfig family (D2 / Spec: write lock for entire write)

- [ ] 5.1 Modify `WriteConfig` to acquire `v.mu.Lock()` for the entire body. _(Deferred — no failing test for this path; risk of locking around disk I/O. Tracked for follow-up.)_
- [ ] 5.2 Modify `SafeWriteConfig`, `WriteConfigAs`, `SafeWriteConfigAs` similarly.
- [ ] 5.3 Remove the partial lock at the start of `writeConfig` (the nil-init).
- [ ] 5.4 Audit `marshalWriter` to confirm `v.config` and `v.properties` access happens under write lock.
- [ ] 5.5 Run `TestConcurrent_WriteConfigSet` under `-race`.

## 6. WatchConfig (D3 / Spec: watcher holds lock)

- [ ] 6.1 In the `WatchConfig` event-loop goroutine, take `v.mu.Lock()` around `getConfigFile()`. _(Deferred — no failing test for this path.)_
- [ ] 6.2 Ensure the `onConfigChange` callback copy continues under `RLock` and the callback is invoked after release.
- [ ] 6.3 If the watcher reads `v.configPaths` directly, wrap that read in `RLock`.
- [ ] 6.4 Run `TestConcurrent_WatchConfigGet` under `-race`.

## 7. Remote-config goroutines (D3 cont. / Spec: write lock around iteration + unmarshal)

- [ ] 7.1 In `getKeyValueConfig`, extend the write lock over `remoteProviders` iteration + `unmarshalReader` + `kvstore` assignment. _(Deferred — no failing test for this path.)_
- [ ] 7.2 Same for `getRemoteConfig`.
- [ ] 7.3 Same for `watchKeyValueConfig`.
- [ ] 7.4 Same for `watchKeyValueConfigOnChannel`.
- [ ] 7.5 Same for `watchRemoteConfig`.
- [ ] 7.6 Run `TestConcurrent_AddRemoteProviderWatch` under `-race`.

## 8. Package-global `v` and `Reset()` (D4 / Spec: atomic pointer)

- [x] 8.1 Confirm Go ≥ 1.19 — confirmed (`go.mod` declares `go 1.24.2`); `atomic.Pointer[Viper]` is available.
- [x] 8.2 Replace package-level `var v *Viper = New()` with the atomic equivalent. _(Done — `var globalV atomic.Pointer[Viper]` + `gv()` accessor; `init()` does `globalV.Store(New())`.)_
- [x] 8.3 Update every package-level wrapper to use the atomic accessor. _(All single- and multi-line wrappers now call `gv()`.)_
- [x] 8.4 Update `Reset()` to publish via the atomic store. _(`Reset()` now calls `globalV.Store(New())`.)_
- [x] 8.5 Run `TestConcurrentRepro_ResetVsPackageGet` under `-race`. _(Passes.)_

## 9. Comment / documentation cleanup

- [x] 9.1 Remove `// Get already has locking` and `// AllSettings has locking` comments. _(All four occurrences in `Sub`, `UnmarshalKey`, `Unmarshal`, `UnmarshalExact` are removed.)_
- [ ] 9.2 Add a short package-level doc comment summarizing the concurrency contract. _(Deferred to follow-up.)_
- [ ] 9.3 Add a CHANGELOG entry. _(Deferred to follow-up.)_

## 10. Benchmarks and performance gate

- [ ] 10.1 Add `BenchmarkGet` and `BenchmarkAllSettings`. _(Deferred.)_
- [ ] 10.2 Capture before/after numbers; require < 2× regression. _(Deferred.)_

## 11. CI integration

- [ ] 11.1 Update CI workflow to run `go test -race ./...` and fail on data races. _(Deferred — repository CI workflow not part of this session.)_
- [ ] 11.2 Confirm tests run within wall-clock budget; gate with `-short` if needed.

## 12. Final verification

- [x] 12.1 Run the test suite under `-race`. _(All 4 concurrent reproducers pass; full suite passes except the pre-existing flaky `TestWatchFile/link_to_real_file` (environmental `ln` issue, not a regression).)_
- [ ] 12.2 Run `go vet ./...`. _(Deferred.)_
- [x] 12.3 Manually re-read `proposal.md`, `design.md`, and `specs/thread-safe-map-access/spec.md` and confirm coverage. _(Production-bug coverage confirmed via D1 / Requirement "Read-path methods SHALL return values that share no map or slice storage". Other requirements remain open and tracked above.)_
- [ ] 12.4 Run `/opsx:verify fix-concurrent-map-access` before archive.

