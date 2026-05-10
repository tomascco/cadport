package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tomascco/cadport/caddy"
	"github.com/tomascco/cadport/detector"
	"github.com/tomascco/cadport/server"
)

const (
	pollInterval = 3 * time.Second
	stableCount  = 2
	debugAddr    = ":9090"
	caddyAddr    = "http://localhost:2019"
	daemonEnv    = "CADPORT_DAEMON"
	readyFD      = 3
	readyTimeout = 5 * time.Second
)

func main() {
	if os.Getenv(daemonEnv) == "1" {
		runChild()
		return
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "run":
		foreground := false
		for _, a := range os.Args[2:] {
			if a == "--foreground" || a == "-f" {
				foreground = true
			}
		}
		if foreground {
			runForeground()
			return
		}
		if err := daemonize(); err != nil {
			fmt.Fprintf(os.Stderr, "cadport: %v\n", err)
			os.Exit(1)
		}
	case "stop":
		stopCmd()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: cadport <command>")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  run [--foreground]   start daemon (default backgrounds)")
	fmt.Fprintln(os.Stderr, "  stop                 signal all running instances")
}

type portState struct {
	info        server.PortInfo
	count       int
	added       bool
	removeCount int
}

func runForeground() {
	log.Printf("cadport started (pid=%d)", os.Getpid())
	runCore(func(err error) error { return nil })
}

func runChild() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	readyPipe := os.NewFile(readyFD, "ready")
	pidPath := pidFilePath()
	defer removePidFile(pidPath)

	signalReady := func(ok bool) {
		if readyPipe == nil {
			return
		}
		if ok {
			readyPipe.Write([]byte("1"))
		} else {
			readyPipe.Write([]byte("0"))
		}
		readyPipe.Close()
		readyPipe = nil
	}

	runCore(func(err error) error {
		if err != nil {
			signalReady(false)
			return nil
		}
		if werr := writePidFile(pidPath, os.Getpid()); werr != nil {
			signalReady(false)
			return werr
		}
		signalReady(true)
		return nil
	})
}

func runCore(ready func(error) error) {
	selfPID := os.Getpid()
	log.Printf("cadport runCore (pid=%d)", selfPID)

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

	listener, err := net.Listen("tcp", debugAddr)
	if err != nil {
		ready(fmt.Errorf("debug listen %s: %w", debugAddr, err))
		log.Fatalf("debug listen %s: %v", debugAddr, err)
	}
	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("debug server: %v", err)
		}
	}()
	log.Printf("debug server on %s", debugAddr)

	srvName, err := caddyClient.DiscoverServer(ctx)
	if err != nil {
		ready(fmt.Errorf("discover caddy server: %w", err))
		log.Fatalf("discover caddy server: %v", err)
	}
	log.Printf("caddy server: %s", srvName)

	if err := ready(nil); err != nil {
		log.Fatalf("ready: %v", err)
	}

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

func daemonize() error {
	pidPath := pidFilePath()
	if existing, err := readPidFile(pidPath); err == nil {
		if pidAlive(existing) {
			return fmt.Errorf("already running, pid=%d", existing)
		}
		os.Remove(pidPath)
	}

	logPath, err := logFilePath()
	if err != nil {
		return fmt.Errorf("log path: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer logFile.Close()

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve self: %w", err)
	}

	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("pipe: %w", err)
	}

	cmd := exec.Cmd{
		Path:        exe,
		Args:        []string{exe, "run"},
		Env:         append(os.Environ(), daemonEnv+"=1"),
		Stdin:       nil,
		Stdout:      logFile,
		Stderr:      logFile,
		ExtraFiles:  []*os.File{pipeW},
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}
	if err := cmd.Start(); err != nil {
		pipeR.Close()
		pipeW.Close()
		return fmt.Errorf("spawn: %w", err)
	}
	pipeW.Close()

	type result struct {
		b   byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := io.ReadFull(pipeR, buf)
		ch <- result{buf[0], err}
	}()

	select {
	case r := <-ch:
		pipeR.Close()
		if r.err == nil && r.b == '1' {
			fmt.Printf("cadport started (pid=%d), logs: %s\n", cmd.Process.Pid, logPath)
			cmd.Process.Release()
			return nil
		}
		cmd.Process.Kill()
		cmd.Wait()
		return fmt.Errorf("daemon failed to start, see %s", logPath)
	case <-time.After(readyTimeout):
		pipeR.Close()
		cmd.Process.Kill()
		cmd.Wait()
		return fmt.Errorf("daemon start timed out, see %s", logPath)
	}
}

func stopCmd() {
	selfPID := os.Getpid()
	pidPath := pidFilePath()
	signaled := map[int]bool{}

	if pid, err := readPidFile(pidPath); err == nil {
		if pidAlive(pid) {
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				fmt.Fprintf(os.Stderr, "cadport: kill pid=%d: %v\n", pid, err)
			} else {
				signaled[pid] = true
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) && pidAlive(pid) {
					time.Sleep(100 * time.Millisecond)
				}
			}
		}
		os.Remove(pidPath)
	}

	for _, pid := range scanStragglers(selfPID) {
		if signaled[pid] {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			fmt.Fprintf(os.Stderr, "cadport: kill pid=%d: %v\n", pid, err)
			continue
		}
		signaled[pid] = true
	}

	if len(signaled) == 0 {
		fmt.Println("cadport: no running instances")
		return
	}
	pids := make([]int, 0, len(signaled))
	for p := range signaled {
		pids = append(pids, p)
	}
	fmt.Printf("cadport: signaled %v\n", pids)
}

func pidFilePath() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "cadport.pid")
	}
	return "/tmp/cadport.pid"
}

func logFilePath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "cadport")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "cadport.log"), nil
}

func writePidFile(path string, pid int) error {
	write := func() error {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintf(f, "%d\n", pid)
		return err
	}
	err := write()
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	if existing, rerr := readPidFile(path); rerr == nil && pidAlive(existing) {
		return fmt.Errorf("already running, pid=%d", existing)
	}
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return rerr
	}
	return write()
}

func readPidFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func removePidFile(path string) {
	os.Remove(path)
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func scanStragglers(selfPID int) []int {
	var pids []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == selfPID {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) == "cadport" {
			pids = append(pids, pid)
		}
	}
	return pids
}
