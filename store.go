package main

import (
	"log"
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

	aof     *AOF // nil means no persistence
	loading bool // true while the log is being replayed

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

// persist appends commands to the log. It returns a ready-made error reply
// if that fails, or nil on success (or when there is no log).
func (s *Store) persist(cmds ...[]string) []byte {
	if s.aof == nil {
		return nil
	}
	if err := s.aof.Append(cmds...); err != nil {
		log.Printf("append to log failed: %v", err)
		return appendError(nil, "ERR could not write to the append-only file")
	}
	return nil
}

// live reports whether key exists. A key past its deadline is deleted on the
// spot ("lazy" expiry), so no command ever sees an expired value.
func (s *Store) live(key string) bool {
	// While replaying, nothing expires: the log already records every
	// deletion that happened, and we must not decide differently now.
	if t, ok := s.expires[key]; ok && !s.loading && !s.now().Before(t) {
		s.remove(key)
		// Log the deletion. Otherwise, replaying the log later would run
		// commands against a key that had already expired at this point.
		s.persist([]string{"DEL", key})
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
	var dels [][]string
	for key, t := range s.expires {
		if !now.Before(t) {
			s.remove(key)
			dels = append(dels, []string{"DEL", key})
		}
	}
	if len(dels) > 0 {
		s.persist(dels...)
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
