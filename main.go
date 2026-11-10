package main

import (
	"bufio"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", ":6380", "address to listen on")
	aofPath := flag.String("aof", "appendonly.aof", "append-only file ('' to disable persistence)")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on %s", ln.Addr())

	store := NewStore()
	if *aofPath != "" {
		f, err := os.OpenFile(*aofPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		store.aof = &AOF{f: f}
	}
	store.StartCleanup(time.Second)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		// One goroutine per client, so a slow client never blocks the others.
		go handle(conn, store)
	}
}

// handle serves one client: read a command, run it, write the reply, repeat.
func handle(conn net.Conn, store *Store) {
	defer conn.Close()
	log.Printf("connected: %s", conn.RemoteAddr())
	defer log.Printf("disconnected: %s", conn.RemoteAddr())

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	for {
		args, err := readCommand(r)
		if err != nil {
			if errors.Is(err, errProtocol) {
				w.Write(appendError(nil, "ERR Protocol error: "+err.Error()))
				w.Flush()
			} else if err != io.EOF {
				log.Printf("read %s: %v", conn.RemoteAddr(), err)
			}
			return
		}

		reply := store.Execute(args)
		w.Write(reply)

		// Pipelining: if the client already sent more commands, keep going
		// and flush all the replies together. Otherwise flush now.
		if r.Buffered() == 0 {
			if err := w.Flush(); err != nil {
				return
			}
		}
	}
}
