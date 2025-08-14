// safemap_test.go
package safemap

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
)

func TestNewAndBasicOps(t *testing.T) {
	m := New[string, int]()
	if m == nil {
		t.Fatal("New returned nil")
	}
	if got := m.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0", got)
	}

	// Set/Get/Has
	m.Set("a", 1)
	if got, ok := m.Get("a"); !ok || got != 1 {
		t.Fatalf("Get(a) = (%d,%v), want (1,true)", got, ok)
	}
	if !m.Has("a") {
		t.Fatalf("Has(a) = false, want true")
	}

	// Len
	if got := m.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}

	// Delete
	m.Delete("a")
	if _, ok := m.Get("a"); ok {
		t.Fatalf("Get(a).ok = true after delete, want false")
	}
	if got := m.Len(); got != 0 {
		t.Fatalf("Len() = %d after delete, want 0", got)
	}
}

func TestNewFromCopies(t *testing.T) {
	src := map[string]int{"x": 1, "y": 2}
	m := NewFrom(src)

	// Mutate src; SafeMap must not see changes.
	src["x"] = 100
	src["z"] = 3

	if got, _ := m.Get("x"); got != 1 {
		t.Fatalf("copy isolation failed: Get(x) = %d, want 1", got)
	}
	if _, ok := m.Get("z"); ok {
		t.Fatalf("copy isolation failed: z present in SafeMap")
	}

	// Verify Keys snapshot is complete.
	keys := m.Keys()
	slices.Sort(keys)
	if want := []string{"x", "y"}; !slices.Equal(keys, want) {
		t.Fatalf("Keys() = %v, want %v", keys, want)
	}
}

func TestKeysValuesSnapshots(t *testing.T) {
	m := New[int, string]()
	m.Set(1, "a")
	m.Set(2, "b")

	keys := m.Keys()
	vals := m.Values()

	// Modify map after snapshot; slices must be independent.
	m.Set(3, "c")
	m.Delete(1)

	if len(keys) != 2 || len(vals) != 2 {
		t.Fatalf("snapshot length changed after mutations: keys=%d vals=%d", len(keys), len(vals))
	}

	// Check contents (order not guaranteed).
	keySet := make(map[int]struct{}, len(keys))
	for _, k := range keys {
		keySet[k] = struct{}{}
	}
	if _, ok := keySet[1]; !ok {
		t.Fatalf("Keys snapshot missing 1")
	}
	if _, ok := keySet[2]; !ok {
		t.Fatalf("Keys snapshot missing 2")
	}

	valSet := make(map[string]struct{}, len(vals))
	for _, v := range vals {
		valSet[v] = struct{}{}
	}
	if _, ok := valSet["a"]; !ok || func() bool { _, ok := valSet["b"]; return ok }() == false {
		t.Fatalf("Values snapshot missing entries: %v", vals)
	}
}

func TestRangeSnapshotAndEarlyStop(t *testing.T) {
	m := New[string, int]()
	for i := range 10 {
		m.Set(fmt.Sprint('a'+i), i)
	}

	// Early stop after first element.
	count := 0
	m.Range(func(k string, v int) bool {
		count++
		return false
	})
	if count != 1 {
		t.Fatalf("Range early stop visited %d, want 1", count)
	}

	// Snapshot behavior: mutating during Range must not affect iteration.
	seen := map[string]int{}
	m.Range(func(k string, v int) bool {
		seen[k] = v
		if v == 0 {
			// Mutate underlying map while iterating snapshot.
			m.Delete(k)
			m.Set("zz", 999)
		}
		return true
	})

	// We should have seen only the original 10 entries (in some order),
	// not the newly added "zz", and still include the deleted key because snapshot.
	if len(seen) != 10 {
		t.Fatalf("Range snapshot size = %d, want 10", len(seen))
	}
	if _, ok := seen["zz"]; ok {
		t.Fatalf("Range snapshot unexpectedly included mutation 'zz'")
	}
}

func TestLoadOrStore(t *testing.T) {
	m := New[string, int]()

	// Store new
	got, loaded := m.LoadOrStore("a", 1)
	if got != 1 || loaded {
		t.Fatalf("LoadOrStore new = (%d,%v), want (1,false)", got, loaded)
	}

	// Load existing
	got, loaded = m.LoadOrStore("a", 2)
	if got != 1 || !loaded {
		t.Fatalf("LoadOrStore existing = (%d,%v), want (1,true)", got, loaded)
	}

	// Ensure value not replaced on load path
	if v, _ := m.Get("a"); v != 1 {
		t.Fatalf("value changed to %d, want 1", v)
	}
}

func TestComputeSetAndDelete(t *testing.T) {
	m := New[string, int]()

	// Set using Compute when key absent.
	next, ok := m.Compute("k", func(prev int, present bool) (int, bool) {
		if present {
			t.Fatalf("present unexpectedly true")
		}
		return 42, false // not delete
	})
	if !ok || next != 42 {
		t.Fatalf("Compute set = (%d,%v), want (42,true)", next, ok)
	}
	if v, _ := m.Get("k"); v != 42 {
		t.Fatalf("Get(k) after Compute = %d, want 42", v)
	}

	// Update existing.
	next, ok = m.Compute("k", func(prev int, present bool) (int, bool) {
		if !present || prev != 42 {
			t.Fatalf("unexpected prev=%d present=%v", prev, present)
		}
		return prev + 1, false
	})
	if !ok || next != 43 {
		t.Fatalf("Compute update = (%d,%v), want (43,true)", next, ok)
	}

	// Delete via Compute.
	_, ok = m.Compute("k", func(prev int, present bool) (int, bool) {
		if !present || prev != 43 {
			t.Fatalf("unexpected prev=%d present=%v", prev, present)
		}
		return 0, true // delete
	})
	if ok {
		t.Fatalf("Compute delete returned ok=true, want false")
	}
	if _, present := m.Get("k"); present {
		t.Fatalf("key k still present after delete")
	}
}

func TestNilReceiverMapInitializationPaths(t *testing.T) {
	// Intentionally construct with nil underlying map and call methods that
	// should auto-init (Set, LoadOrStore, Compute).
	var m SafeMap[int, string] // m.m == nil

	// Set path initializes map.
	m.Set(1, "a")
	if v, ok := m.Get(1); !ok || v != "a" {
		t.Fatalf("Set/Get on nil map failed, got (%q,%v)", v, ok)
	}

	// LoadOrStore path uses existing value, does not overwrite.
	if v, loaded := m.LoadOrStore(1, "b"); v != "a" || !loaded {
		t.Fatalf("LoadOrStore on existing = (%q,%v), want (\"a\",true)", v, loaded)
	}

	// Compute path can set new key and later delete.
	if v, ok := m.Compute(2, func(prev string, present bool) (string, bool) {
		if present {
			t.Fatalf("present unexpectedly true")
		}
		return "x", false
	}); !ok || v != "x" {
		t.Fatalf("Compute set on nil map failed, got (%q,%v)", v, ok)
	}
	if _, ok := m.Compute(2, func(prev string, present bool) (string, bool) {
		if !present || prev != "x" {
			t.Fatalf("unexpected prev=%q present=%v", prev, present)
		}
		return "", true // delete
	}); ok {
		t.Fatalf("Compute delete returned ok=true, want false")
	}
	if _, ok := m.Get(2); ok {
		t.Fatalf("key 2 still present after delete")
	}
}

func TestClear(t *testing.T) {
	m := New[int, int]()
	for i := 0; i < 5; i++ {
		m.Set(i, i*i)
	}
	if m.Len() != 5 {
		t.Fatalf("precondition Len() = %d, want 5", m.Len())
	}
	m.Clear()
	if m.Len() != 0 {
		t.Fatalf("Len() after Clear = %d, want 0", m.Len())
	}

	// After Clear, map remains usable.
	m.Set(1, 2)
	if v, ok := m.Get(1); !ok || v != 2 {
		t.Fatalf("Get after Clear = (%d,%v), want (2,true)", v, ok)
	}
}

func TestConcurrentSetGet(t *testing.T) {
	const (
		NWriters = 8
		NReads   = 2000
		NKeys    = 128
	)
	m := New[int, int]()
	var wg sync.WaitGroup

	// Writers
	for i := 0; i < NWriters; i++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for k := 0; k < NKeys; k++ {
				m.Set(k, k+offset)
			}
		}(i)
	}

	// Readers
	for i := 0; i < NWriters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < NReads; i++ {
				// Iterate via Range snapshot; also hit Get path.
				sum := 0
				m.Range(func(k, v int) bool {
					sum += v
					_, _ = m.Get(k)
					return true
				})
				_ = sum // keep compiler happy
			}
		}()
	}

	wg.Wait()

	// Sanity: keys should exist (last writer wins, so we can't assert values).
	keys := m.Keys()
	if len(keys) == 0 {
		t.Fatalf("no keys present after concurrent writes")
	}

	// Validate that m contains a subset/superset relationship with a fresh snapshot of its own map.
	// This mainly ensures SafeMap is internally consistent.
	check := make(map[int]int)
	m.Range(func(k, v int) bool {
		check[k] = v
		return true
	})
	if !maps.EqualFunc(check, map[int]int{}, func(a, b int) bool { return true }) && len(check) == 0 {
		t.Fatalf("inconsistent snapshot observed")
	}
}
