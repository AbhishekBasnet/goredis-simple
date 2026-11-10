package main

import (
	"bufio"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestReadCommand(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
		err   error // checked with errors.Is
	}{
		{"array", "*2\r\n$3\r\nGET\r\n$1\r\nk\r\n", []string{"GET", "k"}, nil},
		{"inline", "PING\r\n", []string{"PING"}, nil},
		{"inline with args", "SET a b\r\n", []string{"SET", "a", "b"}, nil},
		{"blank lines skipped", "\r\n\r\nPING\r\n", []string{"PING"}, nil},
		{"value with CRLF inside", "*2\r\n$4\r\nECHO\r\n$4\r\na\r\nb\r\n", []string{"ECHO", "a\r\nb"}, nil},
		{"empty bulk", "*2\r\n$4\r\nECHO\r\n$0\r\n\r\n", []string{"ECHO", ""}, nil},
		{"clean end", "", nil, io.EOF},
		{"cut in header", "*2\r\n$3\r\nGE", nil, io.ErrUnexpectedEOF},
		{"cut between args", "*2\r\n$3\r\nGET\r\n", nil, io.ErrUnexpectedEOF},
		{"cut in line", "*2\r", nil, io.ErrUnexpectedEOF},
		{"bad array length", "*x\r\n", nil, errProtocol},
		{"negative bulk length", "*1\r\n$-5\r\n", nil, errProtocol},
		{"missing dollar", "*1\r\nGET\r\n", nil, errProtocol},
		{"bulk not terminated", "*1\r\n$3\r\nGETxx", nil, errProtocol},
		{"huge bulk", "*1\r\n$999999999999\r\n", nil, errProtocol},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readCommand(bufio.NewReader(strings.NewReader(tc.input)))
			if !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Several commands sent back to back must come out one at a time.
func TestReadCommandPipelined(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("*1\r\n$4\r\nPING\r\nGET k\r\n*2\r\n$4\r\nECHO\r\n$2\r\nhi\r\n"))
	for _, want := range [][]string{{"PING"}, {"GET", "k"}, {"ECHO", "hi"}} {
		got, err := readCommand(r)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	if _, err := readCommand(r); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

// What we write to the log must read back as the same command.
func TestEncodeRoundTrip(t *testing.T) {
	want := []string{"SET", "key", "va\r\nlue", ""}
	got, err := readCommand(bufio.NewReader(strings.NewReader(string(encodeCommand(want)))))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestReplies(t *testing.T) {
	var b []byte
	b = appendSimple(b, "OK")
	b = appendError(b, "ERR x")
	b = appendInt(b, -2)
	b = appendBulk(b, "hey")
	b = appendNull(b)
	if want := "+OK\r\n-ERR x\r\n:-2\r\n$3\r\nhey\r\n$-1\r\n"; string(b) != want {
		t.Fatalf("got %q, want %q", b, want)
	}
}
