package main

import (
	"strconv"
	"strings"
	"time"
)

const (
	errNotInt = "ERR value is not an integer or out of range"
	errSyntax = "ERR syntax error"
)

// Execute runs one command and returns the reply, already encoded as RESP.
// It holds the store lock for the whole command, so a command is never seen
// half done by another client.
func (s *Store) Execute(args []string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	cmd := strings.ToUpper(args[0])
	n := len(args) // including the command name

	// arity reports a Redis-style error if the argument count is wrong.
	arity := func(ok bool) []byte {
		if ok {
			return nil
		}
		return appendError(nil, "ERR wrong number of arguments for '"+strings.ToLower(cmd)+"' command")
	}

	switch cmd {
	case "PING":
		if n == 1 {
			return appendSimple(nil, "PONG")
		}
		if e := arity(n == 2); e != nil {
			return e
		}
		return appendBulk(nil, args[1])

	case "ECHO":
		if e := arity(n == 2); e != nil {
			return e
		}
		return appendBulk(nil, args[1])

	case "SET":
		// SET key value [EX seconds | PX milliseconds]
		if e := arity(n == 3 || n == 5); e != nil {
			return e
		}
		var ttl time.Duration
		if n == 5 {
			amount, err := strconv.ParseInt(args[4], 10, 64)
			if err != nil {
				return appendError(nil, errNotInt)
			}
			if amount <= 0 || amount > 1<<40 {
				return appendError(nil, "ERR invalid expire time in 'set' command")
			}
			switch strings.ToUpper(args[3]) {
			case "EX":
				ttl = time.Duration(amount) * time.Second
			case "PX":
				ttl = time.Duration(amount) * time.Millisecond
			default:
				return appendError(nil, errSyntax)
			}
		}
		s.data[args[1]] = args[2]
		delete(s.expires, args[1]) // a plain SET clears any old deadline
		entries := [][]string{{"SET", args[1], args[2]}}
		if ttl > 0 {
			deadline := s.now().Add(ttl)
			s.expires[args[1]] = deadline
			// The log stores the deadline as an absolute time. "EX 10" replayed
			// tomorrow would otherwise give the key another 10 seconds.
			entries = append(entries, []string{"PEXPIREAT", args[1], strconv.FormatInt(deadline.UnixMilli(), 10)})
		}
		if e := s.persist(entries...); e != nil {
			return e
		}
		return appendSimple(nil, "OK")

	case "GET":
		if e := arity(n == 2); e != nil {
			return e
		}
		if !s.live(args[1]) {
			return appendNull(nil)
		}
		return appendBulk(nil, s.data[args[1]])

	case "DEL":
		if e := arity(n >= 2); e != nil {
			return e
		}
		var deleted int64
		for _, k := range args[1:] {
			if s.live(k) {
				s.remove(k)
				deleted++
			}
		}
		if deleted > 0 {
			if e := s.persist(args); e != nil {
				return e
			}
		}
		return appendInt(nil, deleted)

	case "EXISTS":
		if e := arity(n >= 2); e != nil {
			return e
		}
		var count int64
		for _, k := range args[1:] {
			if s.live(k) {
				count++
			}
		}
		return appendInt(nil, count)

	case "INCR":
		if e := arity(n == 2); e != nil {
			return e
		}
		cur := int64(0)
		if s.live(args[1]) {
			var err error
			cur, err = strconv.ParseInt(s.data[args[1]], 10, 64)
			if err != nil {
				return appendError(nil, errNotInt)
			}
		}
		if cur == 1<<63-1 {
			return appendError(nil, "ERR increment or decrement would overflow")
		}
		cur++
		s.data[args[1]] = strconv.FormatInt(cur, 10) // keeps any deadline
		if e := s.persist(args); e != nil {
			return e
		}
		return appendInt(nil, cur)

	case "EXPIRE":
		// EXPIRE key seconds
		if e := arity(n == 3); e != nil {
			return e
		}
		secs, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil || secs > 1<<40 || secs < -(1<<40) {
			return appendError(nil, errNotInt)
		}
		return s.expireAt(args[1], s.now().Add(time.Duration(secs)*time.Second))

	case "PEXPIREAT":
		// PEXPIREAT key unix-milliseconds
		if e := arity(n == 3); e != nil {
			return e
		}
		ms, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return appendError(nil, errNotInt)
		}
		return s.expireAt(args[1], time.UnixMilli(ms))

	case "TTL":
		if e := arity(n == 2); e != nil {
			return e
		}
		if !s.live(args[1]) {
			return appendInt(nil, -2) // no such key
		}
		t, ok := s.expires[args[1]]
		if !ok {
			return appendInt(nil, -1) // key has no deadline
		}
		left := t.Sub(s.now())
		return appendInt(nil, int64((left+500*time.Millisecond)/time.Second)) // rounded
	}

	return appendError(nil, "ERR unknown command '"+args[0]+"'")
}

// expireAt sets the deadline of a key. A deadline already in the past
// deletes the key. It replies 1 if the key existed and 0 if not.
func (s *Store) expireAt(key string, t time.Time) []byte {
	if !s.live(key) {
		return appendInt(nil, 0)
	}
	var entry []string
	if !s.now().Before(t) {
		s.remove(key)
		entry = []string{"DEL", key}
	} else {
		s.expires[key] = t
		entry = []string{"PEXPIREAT", key, strconv.FormatInt(t.UnixMilli(), 10)}
	}
	if e := s.persist(entry); e != nil {
		return e
	}
	return appendInt(nil, 1)
}
