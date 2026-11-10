package main

import (
	"os"
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
