// Package mcp adapts crowban's REST API to MCP tools, prompts, and resources (docs/ARCHITECTURE.md §6).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/crowban/internal/infrastructure/apiclient"
)

const (
	taskResourceURIScheme    = "crowban://task/{id}"
	projectResourceURIScheme = "crowban://project/{id}"
)

// RegisterResources registers CrowBan read-only MCP resources and resource templates onto s.
// It exposes tasks (crowban://task/{id}) and projects (crowban://project/{id}) so agent
// harnesses can retrieve resume payloads and project summaries directly as resources.
func RegisterResources(s *mcpsrv.MCPServer, c Client) {
	registerTaskResource(s, c)
	registerProjectResource(s, c)
}

func registerTaskResource(s *mcpsrv.MCPServer, c Client) {
	handler := func(ctx context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
		id := ""
		if req.Params.Arguments != nil {
			if v, ok := req.Params.Arguments["id"].(string); ok {
				id = strings.TrimSpace(v)
			}
		}
		if id == "" {
			if strings.HasPrefix(req.Params.URI, "crowban://task/") {
				id = strings.TrimPrefix(req.Params.URI, "crowban://task/")
				id = strings.TrimSpace(id)
			}
		}
		if id == "" || id == "{id}" {
			return nil, fmt.Errorf("task ID required: specify a task ULID in URI (e.g. crowban://task/<id>)")
		}

		detail, err := c.GetTask(ctx, id, 0, 0)
		if err != nil {
			if e, ok := apiclient.AsAPIError(err); ok {
				return nil, fmt.Errorf("task %q: %s", id, string(e.Body))
			}
			return nil, fmt.Errorf("task %q: %w", id, err)
		}

		data, err := json.Marshal(detail)
		if err != nil {
			return nil, fmt.Errorf("marshal task %q resume payload: %w", id, err)
		}

		return []mcpgo.ResourceContents{
			mcpgo.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(data),
			},
		}, nil
	}

	// Register as a resource template listed in resources/templates/list
	tmpl := mcpgo.NewResourceTemplate(
		taskResourceURIScheme,
		"Task Resume Payload",
		mcpgo.WithTemplateMIMEType("application/json"),
		mcpgo.WithTemplateDescription("URI template for retrieving task resume payloads by task ULID."),
	)
	s.AddResourceTemplate(tmpl, handler)
}

func registerProjectResource(s *mcpsrv.MCPServer, c Client) {
	handler := func(ctx context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
		id := ""
		if req.Params.Arguments != nil {
			if v, ok := req.Params.Arguments["id"].(string); ok {
				id = strings.TrimSpace(v)
			}
		}
		if id == "" {
			if strings.HasPrefix(req.Params.URI, "crowban://project/") {
				id = strings.TrimPrefix(req.Params.URI, "crowban://project/")
				id = strings.TrimSpace(id)
			}
		}
		if id == "" || id == "{id}" {
			return nil, fmt.Errorf("project ID required: specify a project ULID in URI (e.g. crowban://project/<id>)")
		}

		projects, err := c.ListProjects(ctx)
		if err != nil {
			if e, ok := apiclient.AsAPIError(err); ok {
				return nil, fmt.Errorf("projects list: %s", string(e.Body))
			}
			return nil, fmt.Errorf("projects list: %w", err)
		}

		for _, p := range projects.Projects {
			if p.ID == id {
				data, err := json.Marshal(p)
				if err != nil {
					return nil, fmt.Errorf("marshal project %q: %w", id, err)
				}
				return []mcpgo.ResourceContents{
					mcpgo.TextResourceContents{
						URI:      req.Params.URI,
						MIMEType: "application/json",
						Text:     string(data),
					},
				}, nil
			}
		}

		return nil, fmt.Errorf("project %q not found", id)
	}

	// Register as a resource template listed in resources/templates/list
	tmpl := mcpgo.NewResourceTemplate(
		projectResourceURIScheme,
		"Project Summary",
		mcpgo.WithTemplateMIMEType("application/json"),
		mcpgo.WithTemplateDescription("URI template for retrieving project summaries by project ULID."),
	)
	s.AddResourceTemplate(tmpl, handler)
}
