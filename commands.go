package main

import (
	"strconv"
	"strings"
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
		if e := arity(n == 3); e != nil {
			return e
		}
		s.data[args[1]] = args[2]
		return appendSimple(nil, "OK")

	case "GET":
		if e := arity(n == 2); e != nil {
			return e
		}
		v, ok := s.data[args[1]]
		if !ok {
			return appendNull(nil)
		}
		return appendBulk(nil, v)

	case "DEL":
		if e := arity(n >= 2); e != nil {
			return e
		}
		var deleted int64
		for _, k := range args[1:] {
			if _, ok := s.data[k]; ok {
				delete(s.data, k)
				deleted++
			}
		}
		return appendInt(nil, deleted)

	case "EXISTS":
		if e := arity(n >= 2); e != nil {
			return e
		}
		var count int64
		for _, k := range args[1:] {
			if _, ok := s.data[k]; ok {
				count++
			}
		}
		return appendInt(nil, count)

	case "INCR":
		if e := arity(n == 2); e != nil {
			return e
		}
		cur := int64(0)
		if v, ok := s.data[args[1]]; ok {
			var err error
			cur, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				return appendError(nil, "ERR value is not an integer or out of range")
			}
		}
		if cur == 1<<63-1 {
			return appendError(nil, "ERR increment or decrement would overflow")
		}
		cur++
		s.data[args[1]] = strconv.FormatInt(cur, 10)
		return appendInt(nil, cur)
	}

	return appendError(nil, "ERR unknown command '"+args[0]+"'")
}
