package main

import (
	"bufio"
	"flag"
	"log"
	"net"
)

func main() {
	addr := flag.String("addr", ":6380", "address to listen on")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on %s", ln.Addr())

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		// One goroutine per client, so a slow client never blocks the others.
		go handle(conn)
	}
}

// handle serves one client. For now it is a stub: every line the client
// sends is answered with +PONG. The real command parsing comes in step 2.
func handle(conn net.Conn) {
	defer conn.Close()
	log.Printf("connected: %s", conn.RemoteAddr())

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		conn.Write([]byte("+PONG\r\n"))
	}
	log.Printf("disconnected: %s", conn.RemoteAddr())
}
