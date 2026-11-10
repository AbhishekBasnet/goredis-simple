package main

import (
	"strconv"
	"strings"
	"time"
)

const (
	errNotInt = "ERR value is not an integer or out of range"
	errSyntax = "ERR syntax error"

	// maxExpire is the largest EX, PX or EXPIRE amount accepted.
	maxExpire = 1 << 40
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
		// SET key value [EX seconds | PX milliseconds | PXAT unix-milliseconds]
		if e := arity(n == 3 || n == 5); e != nil {
			return e
		}
		var deadlineMs int64 // unix time in milliseconds; 0 means no deadline
		if n == 5 {
			amount, err := strconv.ParseInt(args[4], 10, 64)
			if err != nil {
				return appendError(nil, errNotInt)
			}
			option := strings.ToUpper(args[3])
			if amount <= 0 || (option != "PXAT" && amount > maxExpire) {
				return appendError(nil, "ERR invalid expire time in 'set' command")
			}
			switch option {
			case "EX":
				deadlineMs = s.now().UnixMilli() + amount*1000
			case "PX":
				deadlineMs = s.now().UnixMilli() + amount
			case "PXAT":
				deadlineMs = amount
			default:
				return appendError(nil, errSyntax)
			}
		}
		s.data[args[1]] = args[2]
		delete(s.expires, args[1]) // a plain SET clears any old deadline
		entry := []string{"SET", args[1], args[2]}
		if deadlineMs > 0 {
			s.expires[args[1]] = time.UnixMilli(deadlineMs)
			// The log stores the deadline as an absolute time. "EX 10" replayed
			// tomorrow would otherwise give the key another 10 seconds.
			// It goes in the same log entry as the value, so a crash can never
			// leave the value in the log without its deadline.
			entry = append(entry, "PXAT", strconv.FormatInt(deadlineMs, 10))
		}
		if e := s.persist(entry); e != nil {
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
		if err != nil || secs > maxExpire || secs < -maxExpire {
			return appendError(nil, errNotInt)
		}
		// Worked out in milliseconds: a time.Duration cannot hold that many seconds.
		return s.expireAt(args[1], time.UnixMilli(s.now().UnixMilli()+secs*1000))

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
		leftMs := t.UnixMilli() - s.now().UnixMilli()
		return appendInt(nil, (leftMs+500)/1000) // rounded to the nearest second
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
	if !s.loading && !s.now().Before(t) {
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
