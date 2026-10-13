package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DeepFal/PortRelay/server"
)

type peekedConn struct {
	net.Conn
	r io.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) {
	return c.r.Read(b)
}

// version 由发布构建通过 -ldflags "-X main.version=..." 注入
var version = "dev"

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	listenAddr := flag.String("listen", ":33000", "Control/relay listen address")
	portMin := flag.Int("port-min", 33000, "Minimum tunnel port")
	portMax := flag.Int("port-max", 34000, "Maximum tunnel port")
	token := flag.String("token", envOr("PORTRELAY_TOKEN", ""), "Shared secret the agent must present (or env PORTRELAY_TOKEN); required")
	publicHost := flag.String("public-host", "", "Host shown on the web auth page (default: the host the visitor used to open the link)")
	stateFile := flag.String("state", "/var/lib/portrelay/leases.json", "File that keeps temporary (web auth) leases across restarts; empty disables")
	trustProxy := flag.Bool("trust-proxy", false, "Trust X-Forwarded-For/X-Real-IP on /auth (only behind a trusted reverse proxy)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("portrelay-server", version)
		return
	}

	if *token == "" {
		log.Fatalf("Fatal: no token configured; set -token or PORTRELAY_TOKEN (e.g. openssl rand -hex 24)")
	}
	if len(*token) < 16 {
		log.Printf("WARNING: token is shorter than 16 characters; use a long random value")
	}

	log.Printf("==================================================")
	log.Printf("  PortRelay Relay Server %s", version)
	log.Printf("  Control/Web Port: %s", *listenAddr)
	log.Printf("  Allowed Range: %d - %d", *portMin, *portMax)
	log.Printf("==================================================")

	mgr := server.NewManager(*portMin, *portMax, *token)
	mgr.PublicHost = *publicHost
	mgr.TrustProxyHeaders = *trustProxy
	if *stateFile != "" {
		if err := mgr.EnableLeasePersistence(*stateFile); err != nil {
			log.Printf("WARNING: lease persistence disabled: %v", err)
		}
	}

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("Fatal: failed to listen on %s: %v", *listenAddr, err)
	}
	defer ln.Close()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Printf("\nShutting down PortRelay Server...")
		_ = ln.Close()
		os.Exit(0)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-sigChan:
				return
			default:
				log.Printf("Accept error: %v", err)
				time.Sleep(50 * time.Millisecond)
				continue
			}
		}

		go func(c net.Conn) {
			// 不发数据的空连接最多占用 10 秒
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			br := bufio.NewReader(c)
			peek, err := br.Peek(3)
			if err != nil {
				_ = c.Close()
				return
			}
			_ = c.SetReadDeadline(time.Time{})
			if string(peek) == "GET" || string(peek) == "POS" || string(peek) == "HEA" {
				mgr.HandleHTTP(&peekedConn{Conn: c, r: br})
				return
			}

			log.Printf("Incoming relay agent connection from %s", c.RemoteAddr())
			if err := mgr.HandleSession(&peekedConn{Conn: c, r: br}); err != nil {
				log.Printf("Session closed for %s: %v", c.RemoteAddr(), err)
			}
		}(conn)
	}
}
