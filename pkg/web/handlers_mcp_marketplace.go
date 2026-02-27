package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudbro-kube-ai/k13d/pkg/config"
	"github.com/cloudbro-kube-ai/k13d/pkg/db"
	"github.com/cloudbro-kube-ai/k13d/pkg/web/marketplace"
)

// ==========================================
// MCPAgent interface implementation for marketplace
// ==========================================

// AddMCPServer implements marketplace.MCPAgent.
// It saves the server config and attempts to connect. Connection failures are
// returned as a non-nil ConnectWarning rather than a hard error, because the
// server is already persisted and can be connected later (e.g. after setting
// required env vars like API tokens).
func (s *Server) AddMCPServer(ctx context.Context, server config.MCPServer) error {
	s.aiMu.Lock()
	s.cfg.AddMCPServer(server)
	s.aiMu.Unlock()

	if err := s.cfg.Save(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	if server.Enabled {
		connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := s.mcpClient.Connect(connectCtx, server); err != nil {
			return &ConnectWarning{ServerName: server.Name, Err: err}
		}
		s.registerMCPTools(server.Name)
	}

	return nil
}

// ConnectWarning indicates that the server was registered but the initial
// connection attempt failed (e.g. missing API token).
type ConnectWarning struct {
	ServerName string
	Err        error
}

func (w *ConnectWarning) Error() string {
	return fmt.Sprintf("server registered but connection failed: %v", w.Err)
}

// IsMCPServerInstalled implements marketplace.MCPAgent.
func (s *Server) IsMCPServerInstalled(name string) bool {
	for _, srv := range s.cfg.MCP.Servers {
		if srv.Name == name {
			return true
		}
	}
	return false
}

// ==========================================
// Marketplace Handlers
// ==========================================

// handleMCPMarketplace returns the marketplace catalog
func (s *Server) handleMCPMarketplace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	data := marketplace.GetMarketplaceYAML()
	market, err := marketplace.LoadMarketplaceYAML(data)
	if err != nil {
		http.Error(w, fmt.Sprintf("loading marketplace: %v", err), http.StatusInternalServerError)
		return
	}

	// Enrich items with installation status
	type itemWithStatus struct {
		marketplace.MarketplaceItem
		Installed bool `json:"installed"`
	}

	items := make([]itemWithStatus, len(market.Items))
	for i, item := range market.Items {
		items[i] = itemWithStatus{
			MarketplaceItem: item,
			Installed:       s.IsMCPServerInstalled(item.Config.ServerName),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"version": market.Version,
		"items":   items,
	})
}

// handleMCPMarketplaceInstall starts an installation job
func (s *Server) handleMCPMarketplaceInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ItemID string `json:"itemId"`
		Method string `json:"method"` // "binary" or "archive"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.ItemID == "" {
		http.Error(w, "itemId is required", http.StatusBadRequest)
		return
	}

	// Load marketplace catalog to find the item
	data := marketplace.GetMarketplaceYAML()
	market, err := marketplace.LoadMarketplaceYAML(data)
	if err != nil {
		http.Error(w, fmt.Sprintf("loading marketplace: %v", err), http.StatusInternalServerError)
		return
	}

	var item *marketplace.MarketplaceItem
	for i := range market.Items {
		if market.Items[i].ID == req.ItemID {
			item = &market.Items[i]
			break
		}
	}
	if item == nil {
		http.Error(w, fmt.Sprintf("item %q not found", req.ItemID), http.StatusNotFound)
		return
	}

	// Check if already installed
	if s.IsMCPServerInstalled(item.Config.ServerName) {
		http.Error(w, fmt.Sprintf("server %q already installed", item.Config.ServerName), http.StatusConflict)
		return
	}

	// Create a job and run installation in background
	jobID := fmt.Sprintf("install-%s-%d", req.ItemID, time.Now().UnixMilli())
	job := s.installJobs.NewInstallJob(jobID)

	installCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	go func() {
		defer cancel()
		_ = s.installer.RunInstallation(installCtx, job, item, req.Method)
	}()

	// Audit
	username := r.Header.Get("X-Username")
	_ = db.RecordAudit(db.AuditEntry{
		User:     username,
		Action:   "mcp_marketplace_install",
		Resource: "mcp",
		Details:  fmt.Sprintf("Installing MCP server from marketplace: %s (method: %s)", item.Name, req.Method),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"jobId":  jobID,
		"status": "started",
	})
}

// handleMCPMarketplaceInstallStream streams installation progress via SSE
func (s *Server) handleMCPMarketplaceInstallStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	jobID := r.URL.Query().Get("jobId")
	if jobID == "" {
		http.Error(w, "jobId parameter is required", http.StatusBadRequest)
		return
	}

	job, ok := s.installJobs.GetJob(jobID)
	if !ok {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	lastLogIndex := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		snap := job.GetSnapshot()

		// Send new log lines as individual events
		for i := lastLogIndex; i < len(snap.Logs); i++ {
			data := map[string]interface{}{
				"type":     "log",
				"message":  snap.Logs[i],
				"progress": snap.Progress,
			}
			jsonData, _ := json.Marshal(data)
			fmt.Fprintf(w, "data: %s\n\n", jsonData)
			flusher.Flush()
		}
		lastLogIndex = len(snap.Logs)

		// Send progress update
		progressData := map[string]interface{}{
			"type":     "progress",
			"progress": snap.Progress,
			"status":   snap.Status,
		}
		jsonData, _ := json.Marshal(progressData)
		fmt.Fprintf(w, "data: %s\n\n", jsonData)
		flusher.Flush()

		// If done, send final event and close
		if snap.Status == "completed" || snap.Status == "failed" {
			doneData := map[string]interface{}{
				"type":   "done",
				"status": snap.Status,
			}
			if snap.Error != "" {
				doneData["error"] = snap.Error
			}
			jsonData, _ := json.Marshal(doneData)
			fmt.Fprintf(w, "data: %s\n\n", jsonData)
			flusher.Flush()
			return
		}

		time.Sleep(500 * time.Millisecond)
	}
}
