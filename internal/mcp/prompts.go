// Package mcp adapts crowban's REST API to MCP tools and prompts (docs/ARCHITECTURE.md §6).
package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/crowban/internal/infrastructure/apiclient"
)

// RegisterPrompts registers CrowBan workflow prompts onto s.
// defaultActor is the container-default identity (from CROWBAN_AGENT_NAME)
// used for agent onboarding guidance when an explicit actor_name is omitted.
func RegisterPrompts(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	registerTriageOpenTasks(s, c)
	registerNextTask(s, c, defaultActor)
}

func registerTriageOpenTasks(s *mcpsrv.MCPServer, c Client) {
	prompt := mcpgo.NewPrompt(
		"triage_open_tasks",
		mcpgo.WithPromptDescription("Lists open tasks in a project and formats a triage / next-actions summary."),
		mcpgo.WithArgument("project_id",
			mcpgo.ArgumentDescription("Project ULID to triage open tasks for (optional; triages across projects if omitted)."),
		),
		mcpgo.WithArgument("limit",
			mcpgo.ArgumentDescription("Maximum number of tasks to inspect per status (optional; default: 50)."),
		),
	)

	s.AddPrompt(prompt, func(ctx context.Context, req mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
		var projectID string
		limit := 50
		if req.Params.Arguments != nil {
			projectID = strings.TrimSpace(req.Params.Arguments["project_id"])
			if lStr := strings.TrimSpace(req.Params.Arguments["limit"]); lStr != "" {
				if parsed, err := strconv.Atoi(lStr); err == nil && parsed > 0 {
					limit = parsed
					if limit > 100 {
						limit = 100
					}
				}
			}
		}

		// 1. Fetch in-progress tasks
		doingList, err := c.ListTasks(ctx, apiclient.TaskFilter{
			ProjectID: projectID,
			Status:    "doing",
			Limit:     limit,
		})
		if err != nil {
			msg := fmt.Sprintf("# Triage Error\n\nUnable to retrieve in-progress tasks: %v", err)
			return mcpgo.NewGetPromptResult("Open tasks triage summary", []mcpgo.PromptMessage{
				mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(msg)),
			}), nil
		}

		// 2. Fetch unblocked todo tasks
		unblockedList, err := c.ListTasks(ctx, apiclient.TaskFilter{
			ProjectID: projectID,
			Status:    "todo",
			Unblocked: true,
			Limit:     limit,
		})
		if err != nil {
			msg := fmt.Sprintf("# Triage Error\n\nUnable to retrieve unblocked tasks: %v", err)
			return mcpgo.NewGetPromptResult("Open tasks triage summary", []mcpgo.PromptMessage{
				mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(msg)),
			}), nil
		}

		// 3. Fetch all todo tasks (to identify blocked ones)
		todoList, err := c.ListTasks(ctx, apiclient.TaskFilter{
			ProjectID: projectID,
			Status:    "todo",
			Limit:     limit,
		})
		if err != nil {
			msg := fmt.Sprintf("# Triage Error\n\nUnable to retrieve todo tasks: %v", err)
			return mcpgo.NewGetPromptResult("Open tasks triage summary", []mcpgo.PromptMessage{
				mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(msg)),
			}), nil
		}

		// Optional project name resolution
		var projectName string
		if projectID != "" {
			if projs, err := c.ListProjects(ctx); err == nil && projs != nil {
				for _, p := range projs.Projects {
					if p.ID == projectID {
						projectName = p.Name
						break
					}
				}
			}
		}

		// Identify blocked tasks
		unblockedSet := make(map[string]bool, len(unblockedList.Tasks))
		for _, t := range unblockedList.Tasks {
			unblockedSet[t.ID] = true
		}
		var blockedTasks []apiclient.TaskSummary
		for _, t := range todoList.Tasks {
			if !unblockedSet[t.ID] {
				blockedTasks = append(blockedTasks, t)
			}
		}

		// Graceful empty data handling
		if len(doingList.Tasks) == 0 && len(todoList.Tasks) == 0 {
			scope := "the repository"
			if projectName != "" {
				scope = fmt.Sprintf("project %q (%s)", projectName, projectID)
			} else if projectID != "" {
				scope = fmt.Sprintf("project %s", projectID)
			}
			msg := fmt.Sprintf("# Triage Summary: No Open Tasks\n\nNo open tasks were found for %s.\nAll tasks are either completed or archived, or no tasks have been created yet.\n\n### Recommended Next Actions\n- Use `create_task` to create a new task.\n- Use `list_projects` to explore available projects.\n", scope)
			return mcpgo.NewGetPromptResult("Open tasks triage summary", []mcpgo.PromptMessage{
				mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(msg)),
			}), nil
		}

		var sb strings.Builder
		sb.WriteString("# Open Tasks Triage Summary\n\n")
		if projectName != "" {
			sb.WriteString(fmt.Sprintf("**Project**: %s (`%s`)\n\n", projectName, projectID))
		} else if projectID != "" {
			sb.WriteString(fmt.Sprintf("**Project ID**: `%s`\n\n", projectID))
		} else {
			sb.WriteString("**Scope**: All Projects\n\n")
		}

		totalOpen := len(doingList.Tasks) + len(todoList.Tasks)
		sb.WriteString(fmt.Sprintf("- **Total Open Tasks**: %d\n", totalOpen))
		sb.WriteString(fmt.Sprintf("- **In-Progress (`doing`)**: %d\n", len(doingList.Tasks)))
		sb.WriteString(fmt.Sprintf("- **Ready for Pickup (`todo`, unblocked)**: %d\n", len(unblockedList.Tasks)))
		sb.WriteString(fmt.Sprintf("- **Blocked (`todo`, has unresolved blockers)**: %d\n\n", len(blockedTasks)))

		// 1. In-Progress section
		sb.WriteString("## 1. In-Progress Tasks (`doing`)\n")
		if len(doingList.Tasks) == 0 {
			sb.WriteString("*No tasks currently in progress.*\n\n")
		} else {
			for _, t := range doingList.Tasks {
				assignee := "unassigned"
				if t.Assignee != nil {
					assignee = fmt.Sprintf("%s (%s)", t.Assignee.Name, t.Assignee.Type)
				}
				claimed := "no lease"
				if t.ClaimedBy != nil {
					expiry := ""
					if t.ClaimExpiresAt != nil {
						expiry = fmt.Sprintf(", expires: %s", *t.ClaimExpiresAt)
					}
					claimed = fmt.Sprintf("claimed by %s (%s)%s", t.ClaimedBy.Name, t.ClaimedBy.Type, expiry)
				}
				sb.WriteString(fmt.Sprintf("- **[%s] %s** (Priority: %d, %s, Assignee: %s)\n",
					t.ID, t.Title, t.Priority, claimed, assignee))
			}
			sb.WriteString("\n")
		}

		// 2. Ready section
		sb.WriteString("## 2. Ready for Pickup (`todo`, unblocked)\n")
		if len(unblockedList.Tasks) == 0 {
			sb.WriteString("*No unblocked tasks waiting in todo.*\n\n")
		} else {
			for _, t := range unblockedList.Tasks {
				due := "none"
				if t.DueAt != nil {
					due = *t.DueAt
				}
				assignee := "unassigned"
				if t.Assignee != nil {
					assignee = fmt.Sprintf("%s (%s)", t.Assignee.Name, t.Assignee.Type)
				}
				sb.WriteString(fmt.Sprintf("- **[%s] %s** (Priority: %d, Due: %s, Assignee: %s)\n",
					t.ID, t.Title, t.Priority, due, assignee))
			}
			sb.WriteString("\n")
		}

		// 3. Blocked section
		sb.WriteString("## 3. Blocked Tasks\n")
		if len(blockedTasks) == 0 {
			sb.WriteString("*No blocked tasks.*\n\n")
		} else {
			for _, t := range blockedTasks {
				sb.WriteString(fmt.Sprintf("- **[%s] %s** (Priority: %d, Status: %s)\n",
					t.ID, t.Title, t.Priority, t.Status))
			}
			sb.WriteString("\n")
		}

		// 4. Next actions
		sb.WriteString("## Recommended Next Actions\n")
		actionNum := 1
		if len(unblockedList.Tasks) > 0 {
			top := unblockedList.Tasks[0]
			sb.WriteString(fmt.Sprintf("%d. Pick up the highest priority ready task **[%s]** (%q, Priority %d) using `claim_task`.\n",
				actionNum, top.ID, top.Title, top.Priority))
			actionNum++
		}
		if len(doingList.Tasks) > 0 {
			sb.WriteString(fmt.Sprintf("%d. Review in-progress tasks to verify active leases and progress.\n", actionNum))
			actionNum++
		}
		if len(blockedTasks) > 0 {
			sb.WriteString(fmt.Sprintf("%d. Review dependencies for the %d blocked task(s) to unblock work.\n",
				actionNum, len(blockedTasks)))
			actionNum++
		}

		return mcpgo.NewGetPromptResult("Open tasks triage summary", []mcpgo.PromptMessage{
			mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(sb.String())),
		}), nil
	})
}

func registerNextTask(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	prompt := mcpgo.NewPrompt(
		"next_task",
		mcpgo.WithPromptDescription("Finds the highest priority unblocked task for an assignee or project and generates an onboarding prompt for an agent."),
		mcpgo.WithArgument("project_id",
			mcpgo.ArgumentDescription("Project ULID to find the next unblocked task in (optional)."),
		),
		mcpgo.WithArgument("assignee",
			mcpgo.ArgumentDescription("Durable assignee name to filter tasks by (optional)."),
		),
		mcpgo.WithArgument("actor_name",
			mcpgo.ArgumentDescription("Agent actor name for onboarding instructions (optional; defaults to container default or 'agent')."),
		),
	)

	s.AddPrompt(prompt, func(ctx context.Context, req mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
		var projectID, assignee string
		actorName := defaultActor.Name
		if actorName == "" {
			actorName = "agent"
		}

		if req.Params.Arguments != nil {
			if p := strings.TrimSpace(req.Params.Arguments["project_id"]); p != "" {
				projectID = p
			}
			if a := strings.TrimSpace(req.Params.Arguments["assignee"]); a != "" {
				assignee = a
			}
			if act := strings.TrimSpace(req.Params.Arguments["actor_name"]); act != "" {
				actorName = act
			}
		}

		// 1. Query unblocked todo tasks (ordered by priority DESC, due_at ASC nulls-last, created_at ASC)
		unblockedList, err := c.ListTasks(ctx, apiclient.TaskFilter{
			ProjectID:    projectID,
			AssigneeName: assignee,
			Status:       "todo",
			Unblocked:    true,
			Limit:        1,
		})
		if err != nil {
			msg := fmt.Sprintf("# Error Retrieving Tasks\n\nUnable to list unblocked tasks: %v", err)
			return mcpgo.NewGetPromptResult("Next task guidance", []mcpgo.PromptMessage{
				mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(msg)),
			}), nil
		}

		var chosen *apiclient.TaskSummary
		if len(unblockedList.Tasks) > 0 {
			chosen = &unblockedList.Tasks[0]
		} else {
			// Check if there is an in-progress task for this assignee or actor
			doingList, err := c.ListTasks(ctx, apiclient.TaskFilter{
				ProjectID:    projectID,
				AssigneeName: assignee,
				Status:       "doing",
				Limit:        5,
			})
			if err == nil && len(doingList.Tasks) > 0 {
				for _, t := range doingList.Tasks {
					if (t.ClaimedBy != nil && t.ClaimedBy.Name == actorName) ||
						(t.Assignee != nil && t.Assignee.Name == actorName) {
						chosen = &t
						break
					}
				}
				if chosen == nil && assignee != "" {
					chosen = &doingList.Tasks[0]
				}
			}
		}

		// Graceful empty data fallback
		if chosen == nil {
			var scopeParts []string
			if projectID != "" {
				scopeParts = append(scopeParts, fmt.Sprintf("project `%s`", projectID))
			}
			if assignee != "" {
				scopeParts = append(scopeParts, fmt.Sprintf("assignee `%s`", assignee))
			}
			scope := "all projects"
			if len(scopeParts) > 0 {
				scope = strings.Join(scopeParts, " and ")
			}

			msg := fmt.Sprintf("# No Available Tasks\n\nNo unblocked tasks are currently available for pickup for %s.\n\nAll tasks in this scope may already be completed, currently claimed by other agents, or blocked by unfinished dependencies.\n\n### Recommended Next Actions\n1. Run `triage_open_tasks` to view the status of all open tasks.\n2. Check for blocked tasks or dependency resolution.\n3. Create a new task using `create_task`.\n", scope)
			return mcpgo.NewGetPromptResult("Next task guidance", []mcpgo.PromptMessage{
				mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(msg)),
			}), nil
		}

		// 2. Fetch full detail for the chosen task
		detail, err := c.GetTask(ctx, chosen.ID, 5, 5)
		if err != nil {
			// Fall back to summary if GetTask fails
			detail = &apiclient.TaskDetail{TaskSummary: *chosen}
		}

		// 3. Format Agent Onboarding Prompt
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("# Task Onboarding: [%s] %s\n\n", detail.ID, detail.Title))
		sb.WriteString(fmt.Sprintf("You have been assigned to work on task **%s** (%q).\n\n", detail.ID, detail.Title))

		sb.WriteString("## Task Details\n")
		sb.WriteString(fmt.Sprintf("- **ID**: `%s`\n", detail.ID))
		if detail.ProjectID != "" {
			sb.WriteString(fmt.Sprintf("- **Project ID**: `%s`\n", detail.ProjectID))
		}
		sb.WriteString(fmt.Sprintf("- **Priority**: %d (0=lowest, 5=highest)\n", detail.Priority))
		sb.WriteString(fmt.Sprintf("- **Status**: `%s`\n", detail.Status))
		sb.WriteString(fmt.Sprintf("- **Version**: %d\n", detail.Version))
		if detail.DueAt != nil {
			sb.WriteString(fmt.Sprintf("- **Due Date**: %s\n", *detail.DueAt))
		} else {
			sb.WriteString("- **Due Date**: None\n")
		}
		if detail.Assignee != nil {
			sb.WriteString(fmt.Sprintf("- **Durable Assignee**: %s (%s)\n", detail.Assignee.Name, detail.Assignee.Type))
		} else {
			sb.WriteString("- **Durable Assignee**: Unassigned\n")
		}
		if detail.ClaimedBy != nil {
			expiry := ""
			if detail.ClaimExpiresAt != nil {
				expiry = fmt.Sprintf(" until %s", *detail.ClaimExpiresAt)
			}
			sb.WriteString(fmt.Sprintf("- **Claim Lease**: %s (%s)%s\n", detail.ClaimedBy.Name, detail.ClaimedBy.Type, expiry))
		}
		sb.WriteString("\n")

		// Description
		sb.WriteString("### Description\n")
		if strings.TrimSpace(detail.Description) != "" {
			sb.WriteString(detail.Description + "\n\n")
		} else {
			sb.WriteString("*(No description provided)*\n\n")
		}

		// Acceptance Criteria
		sb.WriteString("### Acceptance Criteria\n")
		if len(detail.AcceptanceCriteria) == 0 {
			sb.WriteString("*(No acceptance criteria specified)*\n\n")
		} else {
			for _, ac := range detail.AcceptanceCriteria {
				box := "[ ]"
				extra := ""
				if ac.Done {
					box = "[x]"
					if ac.Note != "" {
						extra = fmt.Sprintf(" - *%s*", ac.Note)
					}
				}
				sb.WriteString(fmt.Sprintf("- %s **Seq %d**: %s%s\n", box, ac.Seq, ac.Text, extra))
			}
			sb.WriteString("\n")
		}

		// Resume Notes / Context
		sb.WriteString("### Resume Notes & Durable Context\n")
		if strings.TrimSpace(detail.Notes) != "" {
			sb.WriteString(detail.Notes + "\n\n")
		} else {
			sb.WriteString("*(No previous notes recorded)*\n\n")
		}

		// Recent Comments
		if len(detail.Comments) > 0 {
			sb.WriteString("### Recent Discussion\n")
			for _, cm := range detail.Comments {
				sb.WriteString(fmt.Sprintf("- **%s** (%s, %s): %s\n", cm.Actor.Name, cm.Kind, cm.CreatedAt, cm.Body))
			}
			sb.WriteString("\n")
		}

		// Step-by-step agent workflow playbook
		sb.WriteString("---\n\n")
		sb.WriteString("## CrowBan Agent Workflow Playbook\n")
		sb.WriteString(fmt.Sprintf("Execute this task following the standard CrowBan workflow as agent **%q**:\n\n", actorName))

		sb.WriteString(fmt.Sprintf("1. **Claim the Task** (acquire exclusive lease):\n   `claim_task(id=%q, actor={\"type\": \"agent\", \"name\": %q})`\n\n", detail.ID, actorName))

		sb.WriteString(fmt.Sprintf("2. **Inspect & Update Notes** (record initial plan & findings):\n   `update_notes(id=%q, actor={\"type\": \"agent\", \"name\": %q}, notes=\"## Plan\\n1. ...\")`\n\n", detail.ID, actorName))

		sb.WriteString(fmt.Sprintf("3. **Heartbeat During Long Work** (keep claim active before 15m TTL expires):\n   `heartbeat_task(id=%q, actor={\"type\": \"agent\", \"name\": %q})`\n\n", detail.ID, actorName))

		if len(detail.AcceptanceCriteria) > 0 {
			firstAC := detail.AcceptanceCriteria[0]
			sb.WriteString(fmt.Sprintf("4. **Complete Acceptance Criteria** (mark each AC with evidence):\n   `complete_ac(id=%q, seq=%d, actor={\"type\": \"agent\", \"name\": %q}, done=true, note=\"Tested and verified\")`\n\n", detail.ID, firstAC.Seq, actorName))
		} else {
			sb.WriteString(fmt.Sprintf("4. **Complete Acceptance Criteria**:\n   `complete_ac(id=%q, seq=1, actor={\"type\": \"agent\", \"name\": %q}, done=true, note=\"...\")`\n\n", detail.ID, actorName))
		}

		sb.WriteString(fmt.Sprintf("5. **Attach Testing Evidence** (encouraged, not required — humans verify via artifacts):\n   If you ran tests or verification commands, upload the output so a human can review it:\n   `attach_artifact(id=%q, kind=\"command-output\", ref=\"<path-or-URL of the test/verification output>\", summary=\"e.g. go test ./... — all pass\")`\n\n", detail.ID))

		sb.WriteString(fmt.Sprintf("6. **Finish and Release**:\n   - Move to done: `update_task(id=%q, version=%d, status=\"done\", actor={\"type\": \"agent\", \"name\": %q})`\n   - Release lease: `release_task(id=%q, actor={\"type\": \"agent\", \"name\": %q})`\n",
			detail.ID, detail.Version, actorName, detail.ID, actorName))

		return mcpgo.NewGetPromptResult("Next task guidance", []mcpgo.PromptMessage{
			mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(sb.String())),
		}), nil
	})
}
