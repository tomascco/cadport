package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/tomascco/cadport/caddy"
	"github.com/tomascco/cadport/detector"
	"github.com/tomascco/cadport/server"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cadport <command>")
		fmt.Fprintln(os.Stderr, "commands: run")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "run":
		run()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

const (
	pollInterval = 3 * time.Second
	stableCount = 2
	debugAddr   = ":9090"
	caddyAddr   = "http://localhost:2019"
)

type portState struct {
	info        server.PortInfo
	count       int
	added       bool
	removeCount int
}

func run() {
	selfPID := os.Getpid()
	log.Printf("cadport started (pid=%d)", selfPID)

	caddyClient := caddy.NewClient(caddyAddr)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	states := make(map[int]*portState)
	var mu sync.Mutex

	ds := &server.DebugServer{
		GetPorts: func() []server.PortInfo {
			mu.Lock()
			defer mu.Unlock()
			var ports []server.PortInfo
			for _, s := range states {
				if s.added && s.removeCount == 0 {
					ports = append(ports, s.info)
				}
			}
			return ports
		},
	}

	mux := http.NewServeMux()
	ds.Register(mux)
	srv := &http.Server{Addr: debugAddr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("debug server: %v", err)
		}
	}()
	log.Printf("debug server on %s", debugAddr)

	srvName, err := caddyClient.DiscoverServer(ctx)
	if err != nil {
		log.Fatalf("discover caddy server: %v", err)
	}
	log.Printf("caddy server: %s", srvName)

	removed, err := caddyClient.RemoveAllRoutes(ctx, srvName)
	if err != nil {
		log.Printf("startup cleanup: %v", err)
	}
	for _, id := range removed {
		log.Printf("startup cleanup removed %s", id)
	}

	initialDetected, err := detector.DetectPorts(selfPID)
	if err != nil {
		log.Printf("initial detect: %v", err)
	}
	for _, p := range initialDetected {
		if err := caddyClient.AddRoute(ctx, srvName, p.Port); err != nil {
			log.Printf("add route port=%d: %v", p.Port, err)
			continue
		}
		log.Printf("added route port=%d proc=%s", p.Port, p.Process)
		states[p.Port] = &portState{
			info:  server.PortInfo{Port: p.Port, PID: p.PID, Process: p.Process},
			count: stableCount,
			added: true,
		}
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			removed, err := caddyClient.RemoveAllRoutes(cleanupCtx, srvName)
			if err != nil {
				log.Printf("cleanup: %v", err)
			}
			for _, id := range removed {
				log.Printf("shutdown removed %s", id)
			}
			srv.Shutdown(cleanupCtx)
			return
		case <-ticker.C:
			detected, err := detector.DetectPorts(selfPID)
			if err != nil {
				log.Printf("detect: %v", err)
				continue
			}
			detectedSet := make(map[int]detector.PortInfo, len(detected))
			for _, p := range detected {
				detectedSet[p.Port] = p
			}

			mu.Lock()
			for port, st := range states {
				if _, ok := detectedSet[port]; ok {
					st.removeCount = 0
					continue
				}
				st.removeCount++
				if st.removeCount >= stableCount {
					if err := caddyClient.RemoveRoute(ctx, srvName, port); err != nil {
						log.Printf("remove route port=%d: %v", port, err)
					} else {
						log.Printf("removed route port=%d", port)
					}
					delete(states, port)
				}
			}

			for port, info := range detectedSet {
				st, exists := states[port]
				if !exists {
					states[port] = &portState{
						info:  server.PortInfo{Port: info.Port, PID: info.PID, Process: info.Process},
						count: 1,
					}
					continue
				}
				if st.added || st.removeCount > 0 {
					continue
				}
				st.count++
				if st.count >= stableCount {
					if err := caddyClient.AddRoute(ctx, srvName, info.Port); err != nil {
						log.Printf("add route port=%d: %v", info.Port, err)
					} else {
						log.Printf("added route port=%d proc=%s", info.Port, info.Process)
					}
					st.added = true
				}
			}
			mu.Unlock()
		}
	}
}