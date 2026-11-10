package main

import (
	"strings"
	"testing"
	"time"
)

// testStore returns a store with a clock the test controls.
func testStore() (*Store, *time.Time) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	s := NewStore()
	s.now = func() time.Time { return now }
	return s, &now
}

// run executes a command and returns the reply as text.
func run(s *Store, args ...string) string {
	return string(s.Execute(args))
}

func expect(t *testing.T, s *Store, want string, args ...string) {
	t.Helper()
	if got := run(s, args...); got != want {
		t.Errorf("%v = %q, want %q", args, got, want)
	}
}

func TestStrings(t *testing.T) {
	s, _ := testStore()
	expect(t, s, "+PONG\r\n", "ping")
	expect(t, s, "$2\r\nhi\r\n", "PING", "hi")
	expect(t, s, "$-1\r\n", "GET", "a")
	expect(t, s, "+OK\r\n", "SET", "a", "1")
	expect(t, s, "$1\r\n1\r\n", "get", "a")
	expect(t, s, ":1\r\n", "EXISTS", "a", "b")
	expect(t, s, ":1\r\n", "DEL", "a", "b")
	expect(t, s, ":0\r\n", "DEL", "a")
	expect(t, s, "$-1\r\n", "GET", "a")
}

func TestErrors(t *testing.T) {
	s, _ := testStore()
	expect(t, s, "-ERR wrong number of arguments for 'get' command\r\n", "GET")
	expect(t, s, "-ERR wrong number of arguments for 'set' command\r\n", "SET", "a")
	expect(t, s, "-ERR unknown command 'NOPE'\r\n", "NOPE")
	expect(t, s, "-ERR syntax error\r\n", "SET", "a", "1", "ZZ", "5")
	expect(t, s, "-ERR value is not an integer or out of range\r\n", "SET", "a", "1", "EX", "abc")
	expect(t, s, "-ERR invalid expire time in 'set' command\r\n", "SET", "a", "1", "EX", "0")
	run(s, "SET", "s", "text")
	expect(t, s, "-ERR value is not an integer or out of range\r\n", "INCR", "s")
	run(s, "SET", "max", "9223372036854775807")
	expect(t, s, "-ERR increment or decrement would overflow\r\n", "INCR", "max")
}

func TestIncr(t *testing.T) {
	s, _ := testStore()
	expect(t, s, ":1\r\n", "INCR", "n")
	expect(t, s, ":2\r\n", "INCR", "n")
	expect(t, s, "$1\r\n2\r\n", "GET", "n")
}

func TestExpiry(t *testing.T) {
	s, now := testStore()
	run(s, "SET", "a", "1", "EX", "10")
	expect(t, s, ":10\r\n", "TTL", "a")
	*now = now.Add(4 * time.Second)
	expect(t, s, ":6\r\n", "TTL", "a")
	*now = now.Add(6 * time.Second) // exactly at the deadline
	expect(t, s, "$-1\r\n", "GET", "a")
	expect(t, s, ":-2\r\n", "TTL", "a")
	expect(t, s, ":0\r\n", "EXISTS", "a")

	run(s, "SET", "p", "1", "PX", "1500")
	expect(t, s, ":2\r\n", "TTL", "p") // 1.5s rounds to 2
	*now = now.Add(2 * time.Second)
	expect(t, s, "$-1\r\n", "GET", "p")
}

func TestExpireAndTTL(t *testing.T) {
	s, _ := testStore()
	expect(t, s, ":0\r\n", "EXPIRE", "missing", "10")
	run(s, "SET", "a", "1")
	expect(t, s, ":-1\r\n", "TTL", "a")
	expect(t, s, ":1\r\n", "EXPIRE", "a", "100")
	expect(t, s, ":100\r\n", "TTL", "a")
	run(s, "INCR", "a") // changing the value keeps the deadline
	expect(t, s, ":100\r\n", "TTL", "a")
	run(s, "SET", "a", "2") // a plain SET clears it
	expect(t, s, ":-1\r\n", "TTL", "a")
	expect(t, s, ":1\r\n", "EXPIRE", "a", "-1") // a deadline in the past deletes
	expect(t, s, "$-1\r\n", "GET", "a")
}

func TestPurgeExpired(t *testing.T) {
	s, now := testStore()
	run(s, "SET", "a", "1", "EX", "5")
	run(s, "SET", "b", "1", "EX", "50")
	*now = now.Add(10 * time.Second)
	s.purgeExpired() // no command touched "a", the cleanup removes it
	if _, ok := s.data["a"]; ok {
		t.Error("a should have been purged")
	}
	if _, ok := s.data["b"]; !ok {
		t.Error("b should still exist")
	}
	if len(s.expires) != 1 {
		t.Errorf("expires has %d entries, want 1", len(s.expires))
	}
}

func TestCaseInsensitive(t *testing.T) {
	s, _ := testStore()
	run(s, "sEt", "a", "1", "eX", "5")
	if got := run(s, "TTL", "a"); !strings.HasPrefix(got, ":5") {
		t.Errorf("TTL = %q", got)
	}
}
