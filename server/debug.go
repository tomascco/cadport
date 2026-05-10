package server

import (
	"encoding/json"
	"net/http"
)

type DebugServer struct {
	GetPorts func() []PortInfo
}

type PortInfo struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Process string `json:"process"`
}

func (ds *DebugServer) Register(mux *http.ServeMux) {
	mux.HandleFunc("/ports", ds.handlePorts)
	mux.HandleFunc("/health", ds.handleHealth)
}

func (ds *DebugServer) handlePorts(w http.ResponseWriter, r *http.Request) {
	ports := ds.GetPorts()
	if ports == nil {
		ports = []PortInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ports)
}

func (ds *DebugServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}