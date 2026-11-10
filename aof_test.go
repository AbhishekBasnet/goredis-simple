package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// restart opens the log at path in a brand new store, like a process restart.
func restart(t *testing.T, path string, now func() time.Time) *Store {
	t.Helper()
	s := NewStore()
	if now != nil {
		s.now = now
	}
	if err := s.LoadAOF(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.aof.Close() })
	return s
}

func TestDataSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aof")
	s := restart(t, path, nil)
	run(s, "SET", "a", "hello")
	run(s, "SET", "b", "x")
	run(s, "INCR", "n")
	run(s, "INCR", "n")
	run(s, "DEL", "b")
	s.aof.Close()

	s2 := restart(t, path, nil)
	expect(t, s2, "$5\r\nhello\r\n", "GET", "a")
	expect(t, s2, "$-1\r\n", "GET", "b")
	expect(t, s2, "$1\r\n2\r\n", "GET", "n")
}

// The log stores deadlines as absolute times, so a restart does not
// hand out a fresh 10 seconds.
func TestExpiryIsAbsolute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aof")
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	s := restart(t, path, func() time.Time { return now })
	run(s, "SET", "a", "1", "EX", "10")
	run(s, "SET", "b", "1", "EX", "1000")
	run(s, "SET", "c", "1")
	run(s, "EXPIRE", "c", "10")
	s.aof.Close()

	now = start.Add(60 * time.Second) // "restart" a minute later
	s2 := restart(t, path, func() time.Time { return now })
	expect(t, s2, "$-1\r\n", "GET", "a")
	expect(t, s2, "$-1\r\n", "GET", "c")
	expect(t, s2, ":940\r\n", "TTL", "b")
}

// A key that expired and was then re-created without a deadline must stay
// alive after replay, and an expired key must not be revived by a later INCR.
func TestExpiryHistoryReplaysCorrectly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aof")
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	clock := func() time.Time { return now }
	s := restart(t, path, clock)

	run(s, "SET", "k", "5", "EX", "10")
	now = start.Add(20 * time.Second)
	run(s, "INCR", "k") // k expired at +10s, so this makes a fresh key: 1
	expect(t, s, "$1\r\n1\r\n", "GET", "k")
	expect(t, s, ":-1\r\n", "TTL", "k")
	s.aof.Close()

	now = start.Add(time.Hour)
	s2 := restart(t, path, clock)
	expect(t, s2, "$1\r\n1\r\n", "GET", "k")
	expect(t, s2, ":-1\r\n", "TTL", "k")
}

func TestIncompleteLastCommandIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aof")
	s := restart(t, path, nil)
	run(s, "SET", "a", "1")
	s.aof.Close()

	// Simulate a crash in the middle of writing "SET b 22".
	full := string(encodeCommand([]string{"SET", "b", "22"}))
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(full[:len(full)-4])
	f.Close()

	s2 := restart(t, path, nil)
	expect(t, s2, "$1\r\n1\r\n", "GET", "a")
	expect(t, s2, "$-1\r\n", "GET", "b")

	// New writes must land cleanly after the cut, not glued to the stub.
	run(s2, "SET", "c", "3")
	s2.aof.Close()
	s3 := restart(t, path, nil)
	expect(t, s3, "$1\r\n3\r\n", "GET", "c")
	expect(t, s3, "$1\r\n1\r\n", "GET", "a")
}

func TestCorruptLogIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aof")
	os.WriteFile(path, []byte("*1\r\n$4\r\nPING\r\n*zz\r\n"), 0o644)
	if err := NewStore().LoadAOF(path); err == nil {
		t.Fatal("expected an error for a corrupt log")
	}
}

// Text that parses as a command but is not one we wrote must also be refused.
func TestGarbageInLogIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aof")
	os.WriteFile(path, []byte("*3\r\n$3\r\nSET\r\n$1\r\na\r\n$1\r\n1\r\nthis is not a command\r\n"), 0o644)
	if err := NewStore().LoadAOF(path); err == nil {
		t.Fatal("expected an error for garbage in the log")
	}
}

// SET with an expiry is one log entry. Wherever a crash cuts the log, the
// key must never come back without its deadline.
func TestCrashNeverDropsTheDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aof")
	s := restart(t, path, nil)
	run(s, "SET", "k", "v", "EX", "1000")
	s.aof.Close()
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for cut := 0; cut < len(full); cut++ {
		os.WriteFile(path, full[:cut], 0o644)
		s2 := restart(t, path, nil)
		if got := run(s2, "TTL", "k"); got == ":-1\r\n" {
			t.Fatalf("log cut at byte %d: key came back with no deadline", cut)
		}
		s2.aof.Close()
	}
}

// startServer runs the real TCP server on a random port.
func startServer(t *testing.T, aofPath string) (addr string, stop func()) {
	t.Helper()
	s := NewStore()
	if err := s.LoadAOF(aofPath); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { serve(ln, s); close(done) }()
	return ln.Addr().String(), func() {
		ln.Close()
		<-done
		s.mu.Lock()
		s.aof.Close()
		s.mu.Unlock()
	}
}

// send writes raw text to the server and reads until it has n bytes back.
func send(t *testing.T, addr, text string, n int) string {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 0, n)
	tmp := make([]byte, 1024)
	for len(buf) < n {
		m, err := c.Read(tmp)
		buf = append(buf, tmp[:m]...)
		if err != nil {
			break
		}
	}
	return string(buf)
}

func TestServerWriteRestartRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aof")
	addr, stop := startServer(t, path)
	got := send(t, addr, "*3\r\n$3\r\nSET\r\n$4\r\nname\r\n$3\r\nbob\r\n", 5)
	if got != "+OK\r\n" {
		t.Fatalf("SET reply = %q", got)
	}
	stop()

	addr, stop = startServer(t, path)
	defer stop()
	got = send(t, addr, "GET name\r\n", len("$3\r\nbob\r\n"))
	if got != "$3\r\nbob\r\n" {
		t.Fatalf("GET after restart = %q", got)
	}
}

func TestServerPipelining(t *testing.T) {
	addr, stop := startServer(t, filepath.Join(t.TempDir(), "aof"))
	defer stop()
	got := send(t, addr, "SET a 1\r\nINCR a\r\nGET a\r\nPING\r\n", len("+OK\r\n:2\r\n$1\r\n2\r\n+PONG\r\n"))
	if want := "+OK\r\n:2\r\n$1\r\n2\r\n+PONG\r\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// 100 clients all INCR the same key: the final count must be exact.
// Run with -race to also check the locking.
func TestConcurrentIncr(t *testing.T) {
	addr, stop := startServer(t, filepath.Join(t.TempDir(), "aof"))
	defer stop()

	const clients, each = 100, 20
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			send(t, addr, strings.Repeat("INCR counter\r\n", each), len(":1\r\n")*each)
		}()
	}
	wg.Wait()

	want := "$4\r\n2000\r\n"
	if got := send(t, addr, "GET counter\r\n", len(want)); got != want {
		t.Fatalf("counter = %q, want %q", got, want)
	}
}
