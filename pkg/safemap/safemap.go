// Package safemap provides a concurrency-safe generic map.
package safemap

import (
	"maps"
	"sync"
)

// SafeMap is a threadsafe map[K]V.
type SafeMap[K comparable, V any] struct {
	mu sync.RWMutex
	m  map[K]V
}

// New returns an empty SafeMap.
func New[K comparable, V any]() *SafeMap[K, V] {
	return &SafeMap[K, V]{m: make(map[K]V)}
}

// NewFrom initializes the map from an existing map (copied).
func NewFrom[K comparable, V any](src map[K]V) *SafeMap[K, V] {
	cp := make(map[K]V, len(src))
	for k, v := range src {
		cp[k] = v
	}
	return &SafeMap[K, V]{m: cp}
}

func (s *SafeMap[K, V]) Get(key K) (V, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[key]
	return v, ok
}

func (s *SafeMap[K, V]) Set(key K, value V) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[K]V)
	}
	s.m[key] = value
}

func (s *SafeMap[K, V]) Delete(key K) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
}

func (s *SafeMap[K, V]) Has(key K) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.m[key]
	return ok
}

func (s *SafeMap[K, V]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.m)
}

func (s *SafeMap[K, V]) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = make(map[K]V)
}

// Keys returns a snapshot of keys.
func (s *SafeMap[K, V]) Keys() []K {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]K, 0, len(s.m))
	for k := range s.m {
		keys = append(keys, k)
	}
	return keys
}

// Values returns a snapshot of values.
func (s *SafeMap[K, V]) Values() []V {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vals := make([]V, 0, len(s.m))
	for _, v := range s.m {
		vals = append(vals, v)
	}
	return vals
}

// Range iterates over entries; stop early by returning false.
func (s *SafeMap[K, V]) Range(fn func(K, V) bool) {
	s.mu.RLock()
	// Take a snapshot to keep lock duration bounded if fn is slow.
	cp := make(map[K]V, len(s.m))
	maps.Copy(cp, s.m)
	s.mu.RUnlock()

	for k, v := range cp {
		if !fn(k, v) {
			return
		}
	}
}

// LoadOrStore returns the existing value if present; otherwise stores and returns the given value.
func (s *SafeMap[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[K]V)
	}
	if v, ok := s.m[key]; ok {
		return v, true
	}
	s.m[key] = value
	return value, false
}

// Compute atomically updates the value for key using fn.
// If fn returns delete=true, the entry is removed.
func (s *SafeMap[K, V]) Compute(key K, fn func(prev V, ok bool) (next V, delete bool)) (V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.m[key]
	next, del := fn(prev, ok)
	if del {
		delete(s.m, key)
		var zero V
		return zero, false
	}
	if s.m == nil {
		s.m = make(map[K]V)
	}
	s.m[key] = next
	return next, true
}
