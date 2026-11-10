package main

import "sync"

// Store holds all the data. Every client goroutine shares one Store, so
// every access goes through mu. One lock for everything is simple and easy
// to reason about; the cost is that commands run one at a time.
type Store struct {
	mu   sync.Mutex
	data map[string]string
}

func NewStore() *Store {
	return &Store{data: make(map[string]string)}
}
