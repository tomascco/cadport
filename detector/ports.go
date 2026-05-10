package detector

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type PortInfo struct {
	Port    int
	PID     int
	Process string
}

var excludedPorts = map[int]bool{
	80: true, 443: true, 2019: true, 3000: true, 8080: true,
}

var excludedProcesses = map[string]bool{
	"steam":    true,
	"opencode": true,
	"exe":      true,
}

func DetectPorts(selfPID int) ([]PortInfo, error) {
	inodeToPID, err := buildInodePIDMap()
	if err != nil {
		return nil, fmt.Errorf("build inode map: %w", err)
	}

	listeningInodes, err := parseProcNetTCP()
	if err != nil {
		return nil, fmt.Errorf("parse /proc/net/tcp: %w", err)
	}
	v6Inodes, err := parseProcNetTCP6()
	if err != nil {
		return nil, fmt.Errorf("parse /proc/net/tcp6: %w", err)
	}
	listeningInodes = append(listeningInodes, v6Inodes...)
	seen := make(map[int]bool)
	var unique []listeningInode
	for _, li := range listeningInodes {
		if !seen[li.port] {
			seen[li.port] = true
			unique = append(unique, li)
		}
	}
	listeningInodes = unique

	var results []PortInfo
	for _, li := range listeningInodes {
		pid, ok := inodeToPID[li.inode]
		if !ok {
			continue
		}
		procName, err := getProcessName(pid)
		if err != nil {
			continue
		}
		if strings.HasPrefix(procName, "caddy") || pid == selfPID || excludedProcesses[procName] {
			continue
		}
		if li.port < 1024 || excludedPorts[li.port] {
			continue
		}
		results = append(results, PortInfo{
			Port:    li.port,
			PID:     pid,
			Process: procName,
		})
	}
	return results, nil
}

type listeningInode struct {
	port  int
	inode uint64
}

func parseProcNetTCP() ([]listeningInode, error) {
	return parseProcNet("/proc/net/tcp", 4)
}

func parseProcNetTCP6() ([]listeningInode, error) {
	return parseProcNet("/proc/net/tcp6", 6)
}

func parseProcNet(path string, ipVersion int) ([]listeningInode, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var result []listeningInode
	scanner := bufio.NewScanner(f)
	scanner.Scan() // skip header

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			continue
		}
		localAddr := fields[1]
		state := fields[3]
		inodeStr := fields[9]

		if state != "0A" {
			continue
		}

		addr, portStr, ok := strings.Cut(localAddr, ":")
		if !ok {
			continue
		}
		if ipVersion == 4 {
			if addr != "0100007F" {
				continue
			}
		} else {
			if addr != "00000000000000000000000001000000" && addr != "00000000000000000000000000000000" {
				continue
			}
		}

		port, err := strconv.ParseInt(portStr, 16, 32)
		if err != nil {
			continue
		}

		inode, err := strconv.ParseUint(inodeStr, 10, 64)
		if err != nil {
			continue
		}

		result = append(result, listeningInode{
			port:  int(port),
			inode: inode,
		})
	}
	return result, scanner.Err()
}

func buildInodePIDMap() (map[uint64]int, error) {
	m := make(map[uint64]int)

	procs, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	for _, p := range procs {
		if !p.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}

		fdDir := filepath.Join("/proc", p.Name(), "fd")
		entries, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}

		for _, ent := range entries {
			link, err := os.Readlink(filepath.Join(fdDir, ent.Name()))
			if err != nil {
				continue
			}
			if !strings.HasPrefix(link, "socket:[") || !strings.HasSuffix(link, "]") {
				continue
			}
			inodeStr := link[8 : len(link)-1]
			inode, err := strconv.ParseUint(inodeStr, 10, 64)
			if err != nil {
				continue
			}
			m[inode] = pid
		}
	}
	return m, nil
}

func getProcessName(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", err
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) == 0 || parts[0] == "" {
		return "", fmt.Errorf("empty cmdline")
	}
	name := filepath.Base(parts[0])
	return name, nil
}