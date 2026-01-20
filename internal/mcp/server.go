package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/version"
)

// Server implements the MCP protocol over stdio.
type Server struct {
	db          *db.DB
	provider    embed.Provider
	projectRoot string

	reader  *bufio.Reader
	writer  io.Writer
	writeMu sync.Mutex

	initialized bool
	initMu      sync.Mutex

	handlers map[string]Handler
	tools    *ToolsHandler
}

// Handler is a function that handles an MCP request.
type Handler func(ctx context.Context, params json.RawMessage) (interface{}, error)

// ServerConfig contains configuration for the MCP server.
type ServerConfig struct {
	DB          *db.DB
	Provider    embed.Provider
	ProjectRoot string
}

// NewServer creates a new MCP server.
func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		db:          cfg.DB,
		provider:    cfg.Provider,
		projectRoot: cfg.ProjectRoot,
		reader:      bufio.NewReader(os.Stdin),
		writer:      os.Stdout,
		handlers:    make(map[string]Handler),
	}

	// Initialize tools handler
	s.tools = NewToolsHandler(cfg.DB, cfg.Provider, cfg.ProjectRoot)

	// Register handlers
	s.registerHandlers()

	return s
}

// registerHandlers registers all MCP method handlers.
func (s *Server) registerHandlers() {
	s.handlers["initialize"] = s.handleInitialize
	s.handlers["initialized"] = s.handleInitialized
	s.handlers["ping"] = s.handlePing
	s.handlers["tools/list"] = s.handleToolsList
	s.handlers["tools/call"] = s.handleToolsCall
	s.handlers["resources/list"] = s.handleResourcesList
	s.handlers["resources/read"] = s.handleResourcesRead
	s.handlers["prompts/list"] = s.handlePromptsList
}

// Run starts the MCP server and processes messages until context is canceled.
func (s *Server) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Read a line from stdin
		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read error: %w", err)
		}

		// Parse the request
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.sendError(nil, ParseError, "Parse error", err.Error())
			continue
		}

		// Handle the request
		s.handleRequest(ctx, req)
	}
}

// handleRequest dispatches a request to the appropriate handler.
func (s *Server) handleRequest(ctx context.Context, req Request) {
	// Validate JSON-RPC version
	if req.JSONRPC != "2.0" {
		s.sendError(req.ID, InvalidRequest, "Invalid Request", "jsonrpc must be 2.0")
		return
	}

	// Check if initialized (except for initialize and initialized methods)
	if req.Method != "initialize" && req.Method != "initialized" && req.Method != "ping" {
		s.initMu.Lock()
		initialized := s.initialized
		s.initMu.Unlock()
		if !initialized {
			s.sendError(req.ID, InvalidRequest, "Server not initialized", nil)
			return
		}
	}

	// Find handler
	handler, ok := s.handlers[req.Method]
	if !ok {
		s.sendError(req.ID, MethodNotFound, "Method not found", req.Method)
		return
	}

	// Execute handler
	result, err := handler(ctx, req.Params)
	if err != nil {
		s.sendError(req.ID, InternalError, err.Error(), nil)
		return
	}

	// Send response (only if ID is present - not for notifications)
	if req.ID != nil {
		s.sendResponse(NewResponse(req.ID, result))
	}
}

// handleInitialize handles the initialize request.
func (s *Server) handleInitialize(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var initParams InitializeParams
	if params != nil {
		if err := json.Unmarshal(params, &initParams); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}

	return InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: ServerCapabilities{
			Tools: &ToolsCapability{
				ListChanged: false,
			},
			Resources: &ResourcesCapability{
				Subscribe:   false,
				ListChanged: false,
			},
			Prompts: &PromptsCapability{
				ListChanged: false,
			},
		},
		ServerInfo: Implementation{
			Name:    "vecgrep",
			Version: version.Version,
		},
		Instructions: "vecgrep provides semantic code search using vector embeddings. " +
			"Use vecgrep_search to find relevant code snippets, " +
			"vecgrep_index to index new files, and " +
			"vecgrep_status to check index statistics.",
	}, nil
}

// handleInitialized handles the initialized notification.
func (s *Server) handleInitialized(ctx context.Context, params json.RawMessage) (interface{}, error) {
	s.initMu.Lock()
	s.initialized = true
	s.initMu.Unlock()

	// This is a notification, no response needed
	return nil, nil
}

// handlePing handles ping requests.
func (s *Server) handlePing(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return map[string]string{}, nil
}

// handleToolsList returns the list of available tools.
func (s *Server) handleToolsList(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return s.tools.ListTools(), nil
}

// handleToolsCall executes a tool.
func (s *Server) handleToolsCall(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var callParams CallToolParams
	if err := json.Unmarshal(params, &callParams); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	return s.tools.CallTool(ctx, callParams.Name, callParams.Arguments)
}

// handleResourcesList returns the list of available resources.
func (s *Server) handleResourcesList(ctx context.Context, params json.RawMessage) (interface{}, error) {
	// For now, we don't expose any resources
	return ResourcesListResult{
		Resources: []Resource{},
	}, nil
}

// handleResourcesRead reads a resource.
func (s *Server) handleResourcesRead(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var readParams ReadResourceParams
	if err := json.Unmarshal(params, &readParams); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	// We don't have any resources currently
	return nil, fmt.Errorf("resource not found: %s", readParams.URI)
}

// handlePromptsList returns the list of available prompts.
func (s *Server) handlePromptsList(ctx context.Context, params json.RawMessage) (interface{}, error) {
	// For now, we don't expose any prompts
	return PromptsListResult{
		Prompts: []Prompt{},
	}, nil
}

// sendResponse sends a response to stdout.
func (s *Server) sendResponse(resp Response) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	data, err := json.Marshal(resp)
	if err != nil {
		// Can't send error response, log to stderr
		fmt.Fprintf(os.Stderr, "Failed to marshal response: %v\n", err)
		return
	}

	s.writer.Write(data)
	s.writer.Write([]byte("\n"))
}

// sendError sends an error response.
func (s *Server) sendError(id interface{}, code int, message string, data interface{}) {
	s.sendResponse(NewErrorResponse(id, code, message, data))
}

// sendNotification sends a notification.
func (s *Server) sendNotification(method string, params interface{}) error {
	notif, err := NewNotification(method, params)
	if err != nil {
		return err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	data, err := json.Marshal(notif)
	if err != nil {
		return err
	}

	s.writer.Write(data)
	s.writer.Write([]byte("\n"))
	return nil
}

// Log sends a logging notification.
func (s *Server) Log(level LoggingLevel, logger string, data interface{}) {
	_ = s.sendNotification("notifications/message", LoggingMessageParams{
		Level:  level,
		Logger: logger,
		Data:   data,
	})
}
