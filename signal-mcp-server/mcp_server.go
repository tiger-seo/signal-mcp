package main

import (
	"context"
	"encoding/json"
	"log/slog"
)

// ToolHandler implements one MCP tool. It receives the raw JSON "arguments"
// object from the tools/call request and returns a result to send back.
// A returned Go error is treated as a tool execution failure and reported to
// the client as ToolCallResult{IsError: true}, NOT as a JSON-RPC protocol error.
type ToolHandler func(ctx context.Context, args json.RawMessage) (*ToolCallResult, error)

type registeredTool struct {
	def     ToolDef
	handler ToolHandler
}

// MCPServer is the transport-agnostic MCP protocol engine: tool registry plus
// JSON-RPC method dispatch (initialize, tools/list, tools/call, ping). It
// knows nothing about stdio, HTTP, or any other transport - see http_server.go
// for the transport that drives it.
type MCPServer struct {
	name    string
	version string
	logger  *slog.Logger
	tools   map[string]*registeredTool
	order   []string // preserves registration order for tools/list
}

func NewMCPServer(name, version string, logger *slog.Logger) *MCPServer {
	return &MCPServer{
		name:    name,
		version: version,
		logger:  logger,
		tools:   make(map[string]*registeredTool),
	}
}

// AddTool registers a tool definition and its handler.
func (s *MCPServer) AddTool(def ToolDef, handler ToolHandler) {
	s.tools[def.Name] = &registeredTool{def: def, handler: handler}
	s.order = append(s.order, def.Name)
	s.logger.Debug("registered tool", "name", def.Name)
}

// dispatch handles a single JSON-RPC request and returns either a result or
// an error, per JSON-RPC 2.0. The caller (a transport) decides whether/how to
// deliver the response, and is responsible for detecting notifications
// (requests with no "id") and not sending them a response at all.
func (s *MCPServer) dispatch(ctx context.Context, req rpcRequest) (interface{}, *rpcError) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.Params)
	case "notifications/initialized", "initialized":
		return nil, nil
	case "ping":
		return map[string]interface{}{}, nil
	case "tools/list":
		return s.handleToolsList()
	case "tools/call":
		return s.handleToolsCall(ctx, req.Params)
	default:
		s.logger.Warn("unknown method requested", "method", req.Method)
		return nil, &rpcError{Code: errCodeMethodNotFound, Message: "method not found: " + req.Method}
	}
}

func (s *MCPServer) handleInitialize(params json.RawMessage) (interface{}, *rpcError) {
	var p initializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: errCodeInvalidParams, Message: "invalid initialize params: " + err.Error()}
		}
	}
	protocolVersion := p.ProtocolVersion
	if protocolVersion == "" {
		protocolVersion = "2025-06-18"
	}
	s.logger.Info("client initialized",
		"client_name", p.ClientInfo.Name,
		"client_version", p.ClientInfo.Version,
		"protocol_version", protocolVersion,
	)

	return initializeResult{
		ProtocolVersion: protocolVersion,
		Capabilities: serverCapabilities{
			Tools: &toolsCapability{ListChanged: false},
		},
		ServerInfo: serverInfo{Name: s.name, Version: s.version},
	}, nil
}

func (s *MCPServer) handleToolsList() (interface{}, *rpcError) {
	defs := make([]ToolDef, 0, len(s.order))
	for _, name := range s.order {
		defs = append(defs, s.tools[name].def)
	}
	return toolsListResult{Tools: defs}, nil
}

func (s *MCPServer) handleToolsCall(ctx context.Context, params json.RawMessage) (interface{}, *rpcError) {
	var p toolsCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "invalid tools/call params: " + err.Error()}
	}

	tool, ok := s.tools[p.Name]
	if !ok {
		s.logger.Warn("unknown tool requested", "name", p.Name)
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "unknown tool: " + p.Name}
	}

	s.logger.Info("invoking tool", "name", p.Name, "arguments", string(p.Arguments))

	result, err := tool.handler(ctx, p.Arguments)
	if err != nil {
		s.logger.Error("tool handler returned error", "name", p.Name, "error", err)
		return &ToolCallResult{
			Content: []ContentBlock{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}, nil
	}

	s.logger.Info("tool call succeeded", "name", p.Name)
	return result, nil
}
