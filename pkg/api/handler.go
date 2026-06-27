package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Shaohan-He/game-server-orchestrator/api/v1alpha1"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/controller"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/metrics"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/pool"
)

// Handler holds references to the domain services needed by the REST API.
type Handler struct {
	allocator  controller.Allocator
	poolMgr    *pool.PoolManager
	middleware *Middleware
}

func NewHandler(allocator controller.Allocator, poolMgr *pool.PoolManager, mw *Middleware) *Handler {
	return &Handler{
		allocator:  allocator,
		poolMgr:    poolMgr,
		middleware: mw,
	}
}

type allocateRequest struct {
	Fleet      string   `json:"fleet"`
	Namespace  string   `json:"namespace"`
	Strategy   string   `json:"strategy,omitempty"`
	Regions    []string `json:"regions,omitempty"`
	TTLSeconds int32    `json:"ttlSeconds,omitempty"`
}

type allocateResponse struct {
	ServerName string `json:"serverName"`
	Endpoint   string `json:"endpoint"`
	Node       string `json:"node"`
	SessionID  string `json:"sessionId"`
}

func (h *Handler) Allocate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start := time.Now()

	var req allocateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.Fleet == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "fleet is required"})
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}

	strategy := v1alpha1.AllocationFewestPlayers
	if req.Strategy != "" {
		strategy = v1alpha1.AllocationStrategy(req.Strategy)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := h.allocator.Allocate(ctx, req.Fleet, req.Namespace, strategy, req.Regions)
	if err != nil {
		log.Printf("[ERROR] [api] allocation failed: %v", err)
		metrics.RecordAllocation(req.Fleet, string(strategy), "Failed", time.Since(start).Seconds())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	metrics.RecordAllocation(req.Fleet, string(strategy), "Allocated", time.Since(start).Seconds())

	writeJSON(w, http.StatusOK, allocateResponse{
		ServerName: result.ServerName,
		Endpoint:   result.Endpoint,
		Node:       result.Node,
		SessionID:  fmt.Sprintf("session-%d", time.Now().UnixNano()),
	})
}

type releaseRequest struct {
	Fleet      string `json:"fleet"`
	Namespace  string `json:"namespace"`
	ServerName string `json:"serverName"`
}

func (h *Handler) Release(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req releaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.Fleet == "" || req.ServerName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "fleet and serverName are required"})
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := h.allocator.Release(ctx, req.Fleet, req.Namespace, req.ServerName); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

func (h *Handler) GetFleetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Path: /api/v1/fleets/{namespace}/{name}/status
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/fleets/"), "/")
	if len(parts) != 3 || parts[2] != "status" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path must be /api/v1/fleets/{namespace}/{name}/status"})
		return
	}

	namespace := parts[0]
	fleetName := parts[1]
	stats := h.poolMgr.Stats(fleetName)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"namespace": namespace,
		"pool":      stats,
		"endpoint":  r.URL.Path,
	})
}

func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
