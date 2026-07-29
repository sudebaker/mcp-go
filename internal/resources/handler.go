package resources

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog/log"
)

type ResourceHandler struct {
	manager *ResourceManager
	server  *server.MCPServer
}

func NewResourceHandler(manager *ResourceManager, mcpServer *server.MCPServer) *ResourceHandler {
	return &ResourceHandler{
		manager: manager,
		server:  mcpServer,
	}
}

// RegisterHandler registers the read handler on the MCP server without any resources.
// Resources are registered per-user-initialization via RegisterForSession.
func (h *ResourceHandler) RegisterHandler() {
	h.server.SetResources()
	log.Info().Msg("Registered MCP resource read handler")
}

// RegisterForSession lists all resources for a session's user and registers them
// on the MCP server, replacing any previous registrations.
func (h *ResourceHandler) RegisterForSession(ctx context.Context, sessionID string) ([]mcp.Resource, error) {
	resources, err := h.manager.ListForUser(ctx, sessionID, "")
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}

	serverResources := make([]server.ServerResource, 0, len(resources))
	mcpResources := make([]mcp.Resource, 0, len(resources))
	for _, r := range resources {
		mr := mcp.Resource{
			URI:      r.URI,
			Name:     r.Name,
			MIMEType: r.MIMEType,
		}
		mcpResources = append(mcpResources, mr)
		serverResources = append(serverResources, server.ServerResource{
			Resource: mr,
			Handler:  h.ReadHandler,
		})
	}

	h.server.SetResources(serverResources...)
	log.Info().Int("count", len(resources)).Msg("Registered MCP resources for session")
	return mcpResources, nil
}

func (h *ResourceHandler) ReadHandler(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	uri := request.Params.URI

	session := server.ClientSessionFromContext(ctx)
	if session == nil {
		return nil, fmt.Errorf("no session in context")
	}
	sessionID := session.SessionID()

	res, err := h.manager.ReadForUser(ctx, sessionID, uri)
	if err != nil {
		return nil, fmt.Errorf("read resource: %w", err)
	}
	defer res.Close()

	data, err := io.ReadAll(res.Reader)
	if err != nil {
		return nil, fmt.Errorf("read resource content: %w", err)
	}

	if isTextMIME(res.MIMEType) {
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      uri,
				MIMEType: res.MIMEType,
				Text:     string(data),
			},
		}, nil
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	return []mcp.ResourceContents{
		mcp.BlobResourceContents{
			URI:      uri,
			MIMEType: res.MIMEType,
			Blob:     encoded,
		},
	}, nil
}

func isTextMIME(mime string) bool {
	return strings.HasPrefix(mime, "text/") || mime == "application/json" || mime == "application/xml"
}
