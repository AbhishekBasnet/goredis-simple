package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// AOF is the append-only file. Every command that changes data is written to
// it, in the same format clients use on the wire. Rebuilding the data after a
// restart is then just running the file again from the top.
type AOF struct {
	f *os.File
}

// Append writes the commands to the end of the file and fsyncs before
// returning, so once Append succeeds the data is on disk. fsync on every write
// is slow (it waits for the disk each time) but it is the simplest way to
// never lose an acknowledged write.
func (a *AOF) Append(cmds ...[]string) error {
	var buf []byte
	for _, c := range cmds {
		buf = append(buf, encodeCommand(c)...)
	}
	// One Write call for the whole batch, so commands that belong together
	// (like SET plus its expiry) are not split by a crash between two writes.
	if _, err := a.f.Write(buf); err != nil {
		return err
	}
	return a.f.Sync()
}

func (a *AOF) Close() error { return a.f.Close() }

// LoadAOF replays the file at path into the store, then opens it so that new
// writes are appended. It must be called before the server accepts clients.
// A missing file is fine: it is created and the store starts empty.
func (s *Store) LoadAOF(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	s.mu.Lock()
	s.loading = true
	s.mu.Unlock()

	// Run every command in the file against the (still empty) store.
	// s.aof is nil at this point, so replayed commands are not logged again.
	rd := bytes.NewReader(data)
	br := bufio.NewReader(rd)
	good := 0 // bytes of the file that hold complete commands
	count := 0
	for {
		args, err := readCommand(br)
		if err == io.EOF {
			break
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			// The last command is cut off: the server died while writing it.
			// It was never acknowledged to a client, so dropping it is correct.
			log.Printf("append-only file ends with an incomplete command; ignoring %d bytes", len(data)-good)
			break
		}
		if err != nil {
			return fmt.Errorf("append-only file is corrupt after byte %d: %w", good, err)
		}
		s.Execute(args)
		count++
		good = len(data) - rd.Len() - br.Buffered()
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	// Cut off the incomplete tail, otherwise the next append would be glued
	// onto it and corrupt the file.
	if good < len(data) {
		if err := f.Truncate(int64(good)); err != nil {
			return err
		}
	}
	// Make sure the file's directory entry is on disk too.
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		d.Sync()
		d.Close()
	}

	s.mu.Lock()
	s.loading = false
	s.aof = &AOF{f: f}
	s.purgeExpired() // drop keys whose deadline passed while we were down (logged as DELs)
	s.mu.Unlock()

	log.Printf("loaded %d commands from %s", count, path)
	return nil
}
