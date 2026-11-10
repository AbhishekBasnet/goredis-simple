package main

import (
	"sync"
	"time"
)

// Store holds all the data. Every client goroutine shares one Store, so
// every access goes through mu. One lock for everything is simple and easy
// to reason about; the cost is that commands run one at a time.
type Store struct {
	mu      sync.Mutex
	data    map[string]string
	expires map[string]time.Time // only keys that have a deadline

	now func() time.Time // replaced in tests
}

func NewStore() *Store {
	return &Store{
		data:    make(map[string]string),
		expires: make(map[string]time.Time),
		now:     time.Now,
	}
}

// The methods below must be called with s.mu held.

// live reports whether key exists. A key past its deadline is deleted on the
// spot ("lazy" expiry), so no command ever sees an expired value.
func (s *Store) live(key string) bool {
	if t, ok := s.expires[key]; ok && !s.now().Before(t) {
		s.remove(key)
		return false
	}
	_, ok := s.data[key]
	return ok
}

// remove deletes a key and its deadline.
func (s *Store) remove(key string) {
	delete(s.data, key)
	delete(s.expires, key)
}

// purgeExpired deletes every key that is past its deadline.
func (s *Store) purgeExpired() {
	now := s.now()
	for key, t := range s.expires {
		if !now.Before(t) {
			s.remove(key)
		}
	}
}

// StartCleanup runs purgeExpired every interval in the background.
// Lazy expiry alone would leave keys that are never read in memory forever;
// this loop is what eventually frees them.
func (s *Store) StartCleanup(interval time.Duration) {
	go func() {
		for range time.Tick(interval) {
			s.mu.Lock()
			s.purgeExpired()
			s.mu.Unlock()
		}
	}()
}
