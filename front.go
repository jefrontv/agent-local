package main

import (
	"fmt"
	"io"
	"log"
	"net"
)

// RunFrontDaemon is the root process installed as a LaunchDaemon by
// `agent-local alias`. It binds 127.0.0.2:80/:443 (needs root: ports <1024)
// and pipes each connection to the daemon's router ports. Raw TCP — HTTP
// and TLS both pass through untouched.
func RunFrontDaemon() error {
	log.Printf("front-daemon: binding %s:80 -> :%d, %s:443 -> :%d",
		LoopbackAlias, DefaultHTTPPort, LoopbackAlias, DefaultHTTPSPort)
	errCh := make(chan error, 2)
	go func() { errCh <- pipeListener(LoopbackAlias+":80", DefaultHTTPPort) }()
	go func() { errCh <- pipeListener(LoopbackAlias+":443", DefaultHTTPSPort) }()
	return <-errCh
}

func pipeListener(listen string, dstPort int) error {
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go pipeConn(conn, dstPort)
	}
}

func pipeConn(client net.Conn, dstPort int) {
	defer client.Close()
	backend, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", dstPort))
	if err != nil {
		return
	}
	defer backend.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(backend, client); done <- struct{}{} }()
	go func() { io.Copy(client, backend); done <- struct{}{} }()
	<-done
}
