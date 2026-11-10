package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Limits so a bad or hostile client cannot make us allocate without bound.
const (
	maxBulkLen  = 64 << 20 // bytes in one argument
	maxArrayLen = 1 << 20  // arguments in one command
)

var errProtocol = errors.New("protocol error")

// readLine reads up to the next \n and returns the line without \r\n.
// A line that does not fit in the reader's buffer is a protocol error.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadSlice('\n')
	switch {
	case err == bufio.ErrBufferFull:
		return "", fmt.Errorf("%w: line too long", errProtocol)
	case err == io.EOF && len(line) > 0:
		return "", io.ErrUnexpectedEOF // the data stopped in the middle of a line
	case err != nil:
		return "", err
	}
	return strings.TrimRight(string(line), "\r\n"), nil
}

// readCommand reads one command and returns it as a list of strings,
// for example ["SET", "name", "bob"].
//
// Clients send commands as a RESP array of bulk strings:
//
//	*3\r\n$3\r\nSET\r\n$4\r\nname\r\n$3\r\nbob\r\n
//
// For convenience with telnet/nc it also accepts a plain line like "PING".
//
// It returns io.EOF when the input ends cleanly between commands, and
// io.ErrUnexpectedEOF when it ends in the middle of one.
func readCommand(r *bufio.Reader) ([]string, error) {
	for {
		line, err := readLine(r)
		if err != nil {
			return nil, err
		}

		if line == "" || line[0] != '*' {
			// Inline command (or a blank line, which we skip).
			if args := strings.Fields(line); len(args) > 0 {
				return args, nil
			}
			continue
		}

		n, err := strconv.Atoi(line[1:])
		if err != nil || n < 0 || n > maxArrayLen {
			return nil, fmt.Errorf("%w: bad array length %q", errProtocol, line)
		}
		if n == 0 {
			continue
		}

		args := make([]string, 0, min(n, 16))
		for i := 0; i < n; i++ {
			arg, err := readBulk(r)
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
		}
		return args, nil
	}
}

// readBulk reads one bulk string: $<length>\r\n<bytes>\r\n
func readBulk(r *bufio.Reader) (string, error) {
	hdr, err := readLine(r)
	if err != nil {
		return "", noEOF(err)
	}
	if hdr == "" || hdr[0] != '$' {
		return "", fmt.Errorf("%w: expected '$', got %q", errProtocol, hdr)
	}
	size, err := strconv.Atoi(hdr[1:])
	if err != nil || size < 0 || size > maxBulkLen {
		return "", fmt.Errorf("%w: bad bulk length %q", errProtocol, hdr)
	}

	buf := make([]byte, size+2) // +2 for the trailing \r\n
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", noEOF(err)
	}
	if buf[size] != '\r' || buf[size+1] != '\n' {
		return "", fmt.Errorf("%w: bulk string not terminated by CRLF", errProtocol)
	}
	return string(buf[:size]), nil
}

// noEOF turns a plain EOF into ErrUnexpectedEOF: once a command has started,
// running out of input means the command was cut short.
func noEOF(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// The append* functions build replies in RESP. They add to b and return it.

func appendSimple(b []byte, s string) []byte { return append(b, "+"+s+"\r\n"...) }
func appendError(b []byte, s string) []byte  { return append(b, "-"+s+"\r\n"...) }
func appendInt(b []byte, n int64) []byte     { return append(b, ":"+strconv.FormatInt(n, 10)+"\r\n"...) }
func appendNull(b []byte) []byte             { return append(b, "$-1\r\n"...) }

func appendBulk(b []byte, s string) []byte {
	b = append(b, "$"+strconv.Itoa(len(s))+"\r\n"...)
	b = append(b, s...)
	return append(b, "\r\n"...)
}

// encodeCommand writes a command in the same array-of-bulk-strings form that
// clients send. The append-only file uses this format, so reading the file
// back is just readCommand again.
func encodeCommand(args []string) []byte {
	b := []byte("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, a := range args {
		b = appendBulk(b, a)
	}
	return b
}
