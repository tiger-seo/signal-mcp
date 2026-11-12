package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// --- session tracking ---
//
// Per the MCP "Streamable HTTP" transport spec: a server MAY assign a session
// ID when responding to "initialize", returned via the Mcp-Session-Id header.
// The client then includes that same header on every subsequent request.
// Sessions are in-memory only here (fine for a single-process server) - if
// you need multi-instance/HA, back this with something shared (Redis, etc.).

type sessionInfo struct {
	id              string
	protocolVersion string
	createdAt       time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionInfo
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*sessionInfo)}
}

func (s *sessionStore) create(protocolVersion string) *sessionInfo {
	info := &sessionInfo{id: newSessionID(), protocolVersion: protocolVersion, createdAt: time.Now()}
	s.mu.Lock()
	s.sessions[info.id] = info
	s.mu.Unlock()
	return info
}

func (s *sessionStore) get(id string) (*sessionInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.sessions[id]
	return info, ok
}

func (s *sessionStore) delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[id]
	delete(s.sessions, id)
	return ok
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- transport ---

// HTTPTransport implements the MCP "Streamable HTTP" transport on top of an
// MCPServer. It exposes one endpoint (default "/mcp") that accepts:
//   - POST: a single JSON-RPC message or a JSON-RPC batch (array). Responds
//     with application/json (this server never needs to push messages of its
//     own, so it never upgrades to an SSE stream).
//   - GET: reserved by the spec for servers that want to open a
//     server-initiated SSE stream. This server has nothing to push, so it
//     responds 405.
//   - DELETE: explicit session termination.
type HTTPTransport struct {
	mcp            *MCPServer
	logger         *slog.Logger
	sessions       *sessionStore
	path           string
	authToken      string
	allowedOrigins []string
	requireSession bool
	maxBodyBytes   int64
}

func NewHTTPTransport(mcp *MCPServer, logger *slog.Logger, cfg *Config) *HTTPTransport {
	var origins []string
	if cfg.AllowedOrigins != "" {
		for _, o := range strings.Split(cfg.AllowedOrigins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
	}
	return &HTTPTransport{
		mcp:            mcp,
		logger:         logger,
		sessions:       newSessionStore(),
		path:           cfg.HTTPPath,
		authToken:      cfg.MCPAuthToken,
		allowedOrigins: origins,
		requireSession: cfg.RequireSession,
		maxBodyBytes:   cfg.MaxBodyBytes,
	}
}

func (t *HTTPTransport) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc(t.path, t.handleMCP)
	mux.HandleFunc("/healthz", t.handleHealth)
	return mux
}

func (t *HTTPTransport) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (t *HTTPTransport) handleMCP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	t.logger.Info("http request received",
		"method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr, "origin", r.Header.Get("Origin"))
	defer func() {
		t.logger.Info("http request handled",
			"method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
	}()

	if !t.checkOrigin(w, r) {
		return
	}
	t.writeCORSHeaders(w, r)

	switch r.Method {
	case http.MethodOptions:
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		if !t.checkAuth(w, r) {
			return
		}
		t.handlePost(w, r)
	case http.MethodGet:
		if !t.checkAuth(w, r) {
			return
		}
		w.Header().Set("Allow", "POST, DELETE, OPTIONS")
		http.Error(w, "this server has no server-initiated events; only POST and DELETE are supported", http.StatusMethodNotAllowed)
	case http.MethodDelete:
		if !t.checkAuth(w, r) {
			return
		}
		t.handleDelete(w, r)
	default:
		w.Header().Set("Allow", "POST, GET, DELETE, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (t *HTTPTransport) checkOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || len(t.allowedOrigins) == 0 {
		return true
	}
	for _, o := range t.allowedOrigins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	t.logger.Warn("rejected request from disallowed origin", "origin", origin, "remote_addr", r.RemoteAddr)
	http.Error(w, "origin not allowed", http.StatusForbidden)
	return false
}

func (t *HTTPTransport) writeCORSHeaders(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	if len(t.allowedOrigins) == 0 {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	} else {
		for _, o := range t.allowedOrigins {
			if o == "*" || strings.EqualFold(o, origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				break
			}
		}
	}
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, MCP-Protocol-Version")
	w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
}

func (t *HTTPTransport) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if t.authToken == "" {
		return true
	}
	if r.Header.Get("Authorization") == "Bearer "+t.authToken {
		return true
	}
	t.logger.Warn("rejected request with missing/invalid auth token", "remote_addr", r.RemoteAddr)
	w.Header().Set("WWW-Authenticate", `Bearer realm="signal-mcp-server"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func (t *HTTPTransport) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, t.maxBodyBytes+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > t.maxBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		http.Error(w, "empty request body", http.StatusBadRequest)
		return
	}

	sessionID := r.Header.Get("Mcp-Session-Id")

	var requests []rpcRequest
	isBatch := trimmed[0] == '['

	if isBatch {
		if err := json.Unmarshal(trimmed, &requests); err != nil {
			t.writeParseError(w, err)
			return
		}
		if len(requests) == 0 {
			http.Error(w, "empty batch", http.StatusBadRequest)
			return
		}
	} else {
		var single rpcRequest
		if err := json.Unmarshal(trimmed, &single); err != nil {
			t.writeParseError(w, err)
			return
		}
		requests = []rpcRequest{single}
	}

	// "initialize" always starts a fresh session and never requires an
	// existing one; every other method does, unless enforcement is disabled.
	containsInitialize := false
	for _, req := range requests {
		if req.Method == "initialize" {
			containsInitialize = true
		}
	}

	if !containsInitialize && t.requireSession {
		if sessionID == "" {
			http.Error(w, "missing Mcp-Session-Id header (call \"initialize\" first)", http.StatusBadRequest)
			return
		}
		if _, ok := t.sessions.get(sessionID); !ok {
			t.logger.Warn("rejected request with unknown/expired session", "session_id", sessionID)
			http.Error(w, "unknown or expired session; re-initialize", http.StatusNotFound)
			return
		}
	}

	responses := make([]rpcResponse, 0, len(requests))
	var newSessionID string

	for _, req := range requests {
		isNotification := len(req.ID) == 0 || string(req.ID) == "null"

		t.logger.Info("dispatching rpc",
			"method", req.Method, "id", string(req.ID), "notification", isNotification, "session_id", sessionID)

		result, rpcErr := t.mcp.dispatch(r.Context(), req)

		if req.Method == "initialize" && rpcErr == nil {
			protoVersion := "2025-06-18"
			if ir, ok := result.(initializeResult); ok {
				protoVersion = ir.ProtocolVersion
			}
			info := t.sessions.create(protoVersion)
			newSessionID = info.id
			t.logger.Info("created new mcp session", "session_id", newSessionID, "protocol_version", protoVersion)
		}

		if isNotification {
			continue
		}

		resp := rpcResponse{JSONRPC: jsonrpcVersion, ID: req.ID}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
		responses = append(responses, resp)
	}

	switch {
	case newSessionID != "":
		w.Header().Set("Mcp-Session-Id", newSessionID)
	case sessionID != "":
		w.Header().Set("Mcp-Session-Id", sessionID)
	}

	if len(responses) == 0 {
		// Every message in the request was a notification: nothing to return.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	var out interface{} = responses[0]
	if isBatch {
		out = responses
	}
	if err := json.NewEncoder(w).Encode(out); err != nil {
		t.logger.Error("failed to write response body", "error", err)
	}
}

func (t *HTTPTransport) handleDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		http.Error(w, "missing Mcp-Session-Id header", http.StatusBadRequest)
		return
	}
	if ok := t.sessions.delete(sessionID); !ok {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	t.logger.Info("terminated mcp session", "session_id", sessionID)
	w.WriteHeader(http.StatusNoContent)
}

func (t *HTTPTransport) writeParseError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK) // JSON-RPC parse errors still travel as 200 + an error object
	resp := rpcResponse{
		JSONRPC: jsonrpcVersion,
		Error:   &rpcError{Code: errCodeParseError, Message: "parse error: " + err.Error()},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
