package viper

// Concurrent regression tests.
//
// These tests reproduce "fatal error: concurrent map read and map write" /
// "concurrent map iteration and map write" panics that occur on the *current*
// codebase (after commits 0952d21 "fixes concurrent issue" and b8e37bb
// "fixes concurrent map access").
//
// The recent commits added a sync.RWMutex to *Viper and bracketed most
// public methods with Lock/RLock. They do NOT fix the bug because:
//
//   1. Get / Sub / AllSettings / find / searchMap return map references
//      that ALIAS the internal storage. Once the read lock is released
//      (or once a different *Viper with a different mutex iterates the
//      shared map), a concurrent writer mutates the same map and the Go
//      runtime panics.
//
//   2. Unmarshal / UnmarshalKey / UnmarshalExact pass v.AllSettings() /
//      v.Get(key) directly to mapstructure.Decode, which recurses through
//      the nested aliased maps OUTSIDE any lock. The inline comments
//      "// Get already has locking" and "// AllSettings has locking"
//      (viper.go:837, 856, 902) encode this misconception precisely.
//
// Run these tests with:
//
//   go test -race -run TestConcurrentRepro_ -count=1 -timeout=60s
//
// Each test SHOULD currently fail under -race (or panic with the runtime
// fatal error) on HEAD. After the fix-concurrent-map-access change is
// applied, all tests must pass.

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Reproducer 1: Sub() aliases the parent's nested map across separate mutexes.
//
// matches the stack trace shape:
//   .../viper.(*Viper).searchMap(...)
//   .../viper.(*Viper).find(...)
//   .../viper.(*Viper).Get(...)
//
// Failure mode: parent.MergeConfig (under parent.mu.Lock) mutates the
// nested map at "section" in place via mergeMaps. child was created by
// parent.Sub("section"); child.config aliases parent.config["section"]
// but child.mu is a DIFFERENT mutex. child.Get("k") under child.mu.RLock
// then iterates that nested map while the parent goroutine writes it.
// ---------------------------------------------------------------------------

func TestConcurrentRepro_SubAndMergeConfig(t *testing.T) {
	parent := New()
	parent.SetConfigType("yaml")
	if err := parent.ReadConfig(bytes.NewBufferString(`
section:
  k1: v1
  k2: v2
  k3: v3
`)); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	// child shares storage with parent.config["section"] but has its own mu.
	child := parent.Sub("section")
	if child == nil {
		t.Fatal("Sub returned nil")
	}

	var stop atomic.Bool
	var wg sync.WaitGroup

	// Writer: MergeConfig mutates parent.config["section"] in place via mergeMaps.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for !stop.Load() {
			payload := fmt.Sprintf(`
section:
  k1: v%d
  k2: v%d
  k3: v%d
  k%d: v%d
`, i, i, i, i, i)
			_ = parent.MergeConfig(bytes.NewBufferString(payload))
			i++
		}
	}()

	// Readers: child.Get / child.AllSettings iterate the aliased map.
	readers := runtime.GOMAXPROCS(0)
	if readers < 4 {
		readers = 4
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				_ = child.Get("k1")
				_ = child.Get("k2")
				for k, v := range child.AllSettings() {
					_, _ = k, v
				}
			}
		}()
	}

	time.Sleep(2 * time.Second)
	stop.Store(true)
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Reproducer 2: AllSettings + Unmarshal vs. Set on a single *Viper.
//
// AllSettings releases the lock between AllKeys() and each Get(). More
// importantly, Get returns aliased nested maps. Unmarshal (line 856 / 902)
// calls v.AllSettings() and feeds the result to mapstructure.Decode, which
// recurses through nested maps OUTSIDE the lock. Concurrent Set on a
// nested key mutates the same map -> panic.
// ---------------------------------------------------------------------------

type unmarshalDst struct {
	Section map[string]interface{} `mapstructure:"section"`
	Other   map[string]string      `mapstructure:"other"`
}

func TestConcurrentRepro_UnmarshalVsSet(t *testing.T) {
	v := New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(bytes.NewBufferString(`
section:
  a: 1
  b: 2
  c: 3
other:
  x: hello
  y: world
`)); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup

	// Writer: Set mutates v.override["section"] in place via deepSearch.
	// find() looks up "section.X" in override first, returning that nested
	// map; AllSettings() builds output from those references.
	v.Set("section.seed", "seed")
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for !stop.Load() {
			v.Set(fmt.Sprintf("section.k%d", i%32), i)
			v.Set(fmt.Sprintf("other.k%d", i%32), fmt.Sprintf("v%d", i))
			i++
		}
	}()

	// Readers: Unmarshal walks nested aliased maps under mapstructure.Decode
	// after the lock is released.
	readers := runtime.GOMAXPROCS(0)
	if readers < 4 {
		readers = 4
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				var dst unmarshalDst
				_ = v.Unmarshal(&dst)
			}
		}()
	}

	time.Sleep(2 * time.Second)
	stop.Store(true)
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Reproducer 3: AllSettings iteration vs. Set.
//
// User-side iteration of AllSettings() (without holding any lock - the
// caller has no way to take v.mu) races with concurrent Set. This is the
// pattern most often triggered by templating engines, JSON marshalers,
// and logging code that walks the config map.
// ---------------------------------------------------------------------------

func TestConcurrentRepro_AllSettingsIteration(t *testing.T) {
	v := New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(bytes.NewBufferString(`
nested:
  one: 1
  two: 2
  three: 3
top: value
`)); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup

	// Writer: Set mutates the override registry's nested maps.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for !stop.Load() {
			v.Set(fmt.Sprintf("nested.k%d", i%64), i)
			i++
		}
	}()

	// Readers: AllSettings returns a top-level map whose VALUES are aliased
	// nested maps from the registries. Iterating those values with the
	// "range over interface{} -> map[string]interface{}" pattern races with
	// the writer.
	readers := runtime.GOMAXPROCS(0)
	if readers < 4 {
		readers = 4
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				all := v.AllSettings()
				walk(all)
			}
		}()
	}

	time.Sleep(2 * time.Second)
	stop.Store(true)
	wg.Wait()
}

// walk mimics what mapstructure.Decode, JSON marshaling, and template
// rendering all do internally: a recursive iteration of nested maps.
func walk(v interface{}) {
	switch t := v.(type) {
	case map[string]interface{}:
		for _, vv := range t {
			walk(vv)
		}
	case map[interface{}]interface{}:
		for _, vv := range t {
			walk(vv)
		}
	case []interface{}:
		for _, vv := range t {
			walk(vv)
		}
	}
}

// ---------------------------------------------------------------------------
// Reproducer 4: package-global Reset() vs. package-level Get().
//
// Reset() replaces the package-global v *Viper pointer without
// synchronization. Concurrent goroutines reading v race on the pointer.
// On most architectures this manifests as observing a torn or stale
// pointer; under -race it is reported as a data race.
// ---------------------------------------------------------------------------

func TestConcurrentRepro_ResetVsPackageGet(t *testing.T) {
	Reset()
	SetConfigType("yaml")
	if err := ReadConfig(bytes.NewBufferString("k: v\n")); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup

	// Resetter: replaces the package-global v.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			Reset()
			SetConfigType("yaml")
			_ = ReadConfig(bytes.NewBufferString("k: v\n"))
		}
	}()

	readers := runtime.GOMAXPROCS(0)
	if readers < 4 {
		readers = 4
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				_ = Get("k")
			}
		}()
	}

	time.Sleep(2 * time.Second)
	stop.Store(true)
	wg.Wait()
}
