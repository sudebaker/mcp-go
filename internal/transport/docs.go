package transport

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *MCPServer) handleDocs(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if path == "/docs" || path == "/docs/" {
		http.Redirect(w, r, "/docs/", http.StatusMovedPermanently)
		return
	}

	if strings.HasPrefix(path, "/docs/") {
		filePath := strings.TrimPrefix(path, "/docs/")

		contentType := getContentType(filePath)
		if contentType == "" {
			http.NotFound(w, r)
			return
		}

		content, err := swaggerAssets.ReadFile("docs/" + filePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", contentType)
		w.Write(content)
		return
	}

	http.NotFound(w, r)
}

func getContentType(path string) string {
	if strings.HasSuffix(path, ".html") {
		return "text/html; charset=utf-8"
	}
	if strings.HasSuffix(path, ".css") {
		return "text/css; charset=utf-8"
	}
	if strings.HasSuffix(path, ".js") {
		return "application/javascript; charset=utf-8"
	}
	if strings.HasSuffix(path, ".json") {
		return "application/json; charset=utf-8"
	}
	if strings.HasSuffix(path, ".png") {
		return "image/png"
	}
	if strings.HasSuffix(path, ".svg") {
		return "image/svg+xml"
	}
	if strings.HasSuffix(path, ".ttf") {
		return "font/ttf"
	}
	if strings.HasSuffix(path, ".woff") {
		return "font/woff"
	}
	if strings.HasSuffix(path, ".woff2") {
		return "font/woff2"
	}
	return ""
}

func (s *MCPServer) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	tools := make([]map[string]interface{}, 0, len(s.tools))
	for _, t := range s.tools {
		tools = append(tools, map[string]interface{}{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}

	spec := map[string]interface{}{
		"openapi": "3.0.0",
		"info": map[string]interface{}{
			"title":       s.serverName,
			"version":     s.version,
			"description": "MCP (Model Context Protocol) Server with Streamable HTTP transport",
		},
		"servers": []map[string]string{
			{"url": "/", "description": "MCP Server"},
		},
		"paths": map[string]interface{}{
			"/health": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Health check",
					"description": "Returns server health status",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Server is healthy",
						},
					},
				},
			},
			"/docs": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Swagger UI",
					"description": "Interactive API documentation",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Swagger UI dashboard",
						},
					},
				},
			},
			"/mcp": map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "MCP Streamable HTTP",
					"description": "Send MCP JSON-RPC messages. Session ID returned via Mcp-Session-Id header.",
					"requestBody": map[string]interface{}{
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"jsonrpc": map[string]string{"type": "string", "example": "2.0"},
										"id":      map[string]string{"type": "integer"},
										"method":  map[string]string{"type": "string"},
										"params":  map[string]string{"type": "object"},
									},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "JSON-RPC response or SSE stream",
							"headers": map[string]interface{}{
								"Mcp-Session-Id": map[string]interface{}{
									"description": "Session ID for subsequent requests",
									"schema":      map[string]string{"type": "string"},
								},
							},
						},
					},
				},
				"get": map[string]interface{}{
					"summary":     "MCP SSE Stream",
					"description": "Establish SSE stream for server-initiated messages (requires Mcp-Session-Id header)",
				},
			},
		},
		"components": map[string]interface{}{
			"schemas": map[string]interface{}{
				"Tool": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name":        map[string]string{"type": "string"},
						"description": map[string]string{"type": "string"},
						"inputSchema": map[string]interface{}{
							"type": "object",
						},
					},
				},
			},
		},
		"x-mcp-info": map[string]interface{}{
			"protocol":        "MCP (Model Context Protocol)",
			"transport":       "Streamable HTTP (spec 2025-03-26)",
			"specification":   "https://modelcontextprotocol.io/specification/2025-03-26/basic/transports",
			"available_tools": tools,
			"usage": map[string]interface{}{
				"step1": "POST /mcp with initialize request, receive Mcp-Session-Id header",
				"step2": "Include Mcp-Session-Id header in subsequent requests",
				"step3": "POST /mcp with tools/list to discover available tools",
				"step4": "POST /mcp with tools/call to execute tools",
			},
			"example_initialize": map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "initialize",
				"params": map[string]interface{}{
					"protocolVersion": "2025-03-26",
					"capabilities":    map[string]interface{}{},
					"clientInfo": map[string]string{
						"name":    "my-client",
						"version": "1.0",
					},
				},
			},
			"example_tools_call": map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      2,
				"method":  "tools/call",
				"params": map[string]interface{}{
					"name": "echo",
					"arguments": map[string]string{
						"text": "Hello!",
					},
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(spec)
}

func (s *MCPServer) setupDocsEndpoints(mux *http.ServeMux) {
	mux.HandleFunc("/docs/", s.handleDocs)
	mux.HandleFunc("/openapi.json", s.handleOpenAPISpec)
}

type embeddedAsset struct {
	data     []byte
	isDir    bool
	Children []embeddedAsset
}

var swaggerAssets = &embeddedAsset{
	isDir: true,
	Children: []embeddedAsset{
		{
			isDir: true,
			Children: []embeddedAsset{
				{
					data:     []byte(indexHTML),
					isDir:    false,
					Children: nil,
				},
			},
		},
	},
}

func (e *embeddedAsset) ReadFile(name string) ([]byte, error) {
	parts := strings.Split(strings.TrimPrefix(name, "docs/"), "/")
	return e.readFile(parts)
}

func (e *embeddedAsset) readFile(path []string) ([]byte, error) {
	if len(path) == 0 || path[0] == "" {
		return nil, nil
	}

	for _, child := range e.Children {
		if child.isDir {
			if child.Children[0].isDir == false && child.Children[0].data != nil {
				if path[0] == "" || path[0] == "index.html" {
					return child.Children[0].data, nil
				}
			}
			if result, err := child.readFile(path[1:]); err == nil && result != nil {
				return result, nil
			}
		} else {
			if child.data != nil && (path[0] == "" || path[0] == "index.html") {
				return child.data, nil
			}
		}
	}
	return nil, nil
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>MCP Orchestrator - API Documentation</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui.css" />
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui-bundle.js"></script>
  <script>
    window.onload = function() {
      SwaggerUIBundle({
        url: '/openapi.json',
        dom_id: '#swagger-ui',
        deepLinking: true,
        presets: [
          SwaggerUIBundle.presets.apis,
          SwaggerUIBundle.SwaggerUIStandalonePreset
        ],
        layout: "StandaloneLayout"
      });
    };
  </script>
</body>
</html>`
