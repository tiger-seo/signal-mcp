package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// RegisterTools wires up all MCP tools exposed by this server.
func RegisterTools(srv *MCPServer, client *SignalClient, logger *slog.Logger) {
	srv.AddTool(ToolDef{
		Name: "send_message_to_user",
		Description: "Send a direct Signal message to a single user, identified by phone " +
			"number (E.164 format, e.g. +15551234567) or Signal username.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"recipient": map[string]interface{}{
					"type":        "string",
					"description": "The recipient's phone number in E.164 format (e.g. +15551234567) or their Signal username.",
				},
				"message": map[string]interface{}{
					"type":        "string",
					"description": "The text of the message to send.",
				},
				"text_mode": map[string]interface{}{
					"type": "string",
					"enum": []string{"normal", "styled"},
					"description": "Use 'styled' to enable Signal text formatting " +
						"(*italic*, **bold**, ~strikethrough~, ||spoiler||, `monospace`). Defaults to 'normal'.",
				},
			},
			"required": []string{"recipient", "message"},
		},
	}, makeSendHandler(client, logger, false))

	srv.AddTool(ToolDef{
		Name:        "send_message_to_group",
		Description: "Send a Signal message to a group, identified by its Signal group ID.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"group_id": map[string]interface{}{
					"type":        "string",
					"description": "The Signal group ID (base64-encoded, as returned by the group listing API).",
				},
				"message": map[string]interface{}{
					"type":        "string",
					"description": "The text of the message to send.",
				},
				"text_mode": map[string]interface{}{
					"type": "string",
					"enum": []string{"normal", "styled"},
					"description": "Use 'styled' to enable Signal text formatting " +
						"(*italic*, **bold**, ~strikethrough~, ||spoiler||, `monospace`). Defaults to 'normal'.",
				},
			},
			"required": []string{"group_id", "message"},
		},
	}, makeSendHandler(client, logger, true))

	srv.AddTool(ToolDef{
		Name: "receive_message",
		Description: "Wait for and retrieve incoming Signal messages (direct and group) " +
			"sent to the configured account, up to a timeout.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"timeout_seconds": map[string]interface{}{
					"type":        "integer",
					"minimum":     0,
					"maximum":     300,
					"description": "How many seconds to wait for new messages before returning. Defaults to 5.",
				},
				"max_messages": map[string]interface{}{
					"type":        "integer",
					"minimum":     1,
					"description": "Maximum number of messages to return. Omit for no limit within the timeout window.",
				},
				"ignore_attachments": map[string]interface{}{
					"type":        "boolean",
					"description": "If true (default), attachments are not downloaded, only reported by count.",
				},
			},
		},
	}, makeReceiveHandler(client, logger))
}

func makeSendHandler(client *SignalClient, logger *slog.Logger, isGroup bool) ToolHandler {
	return func(ctx context.Context, rawArgs json.RawMessage) (*ToolCallResult, error) {
		var args struct {
			Recipient string `json:"recipient"`
			GroupID   string `json:"group_id"`
			Message   string `json:"message"`
			TextMode  string `json:"text_mode"`
		}
		if len(rawArgs) > 0 {
			if err := json.Unmarshal(rawArgs, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
		}

		target := args.Recipient
		targetKind := "recipient"
		if isGroup {
			target = args.GroupID
			targetKind = "group_id"
		}
		target = strings.TrimSpace(target)
		message := strings.TrimSpace(args.Message)

		if target == "" {
			return nil, fmt.Errorf("%s is required", targetKind)
		}
		if message == "" {
			return nil, fmt.Errorf("message is required")
		}
		if args.TextMode != "" && args.TextMode != "normal" && args.TextMode != "styled" {
			return nil, fmt.Errorf("text_mode must be 'normal' or 'styled', got %q", args.TextMode)
		}

		logger.Info("sending signal message", "kind", targetKind, "target", target, "message_length", len(message))

		timestamp, err := client.SendMessage(ctx, []string{target}, message, args.TextMode, nil)
		if err != nil {
			logger.Error("failed to send signal message", "kind", targetKind, "target", target, "error", err)
			return nil, err
		}

		logger.Info("signal message sent", "kind", targetKind, "target", target, "timestamp", timestamp)

		return &ToolCallResult{
			Content: []ContentBlock{{
				Type: "text",
				Text: fmt.Sprintf("Message sent successfully to %s %q. Signal timestamp: %s", targetKind, target, timestamp),
			}},
		}, nil
	}
}

func makeReceiveHandler(client *SignalClient, logger *slog.Logger) ToolHandler {
	return func(ctx context.Context, rawArgs json.RawMessage) (*ToolCallResult, error) {
		args := struct {
			TimeoutSeconds    *int  `json:"timeout_seconds"`
			MaxMessages       *int  `json:"max_messages"`
			IgnoreAttachments *bool `json:"ignore_attachments"`
		}{}
		if len(rawArgs) > 0 {
			if err := json.Unmarshal(rawArgs, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
		}

		timeoutSeconds := 5
		if args.TimeoutSeconds != nil {
			timeoutSeconds = *args.TimeoutSeconds
		}
		if timeoutSeconds < 0 || timeoutSeconds > 300 {
			return nil, fmt.Errorf("timeout_seconds must be between 0 and 300, got %d", timeoutSeconds)
		}

		maxMessages := 0
		if args.MaxMessages != nil {
			maxMessages = *args.MaxMessages
		}

		ignoreAttachments := true
		if args.IgnoreAttachments != nil {
			ignoreAttachments = *args.IgnoreAttachments
		}

		logger.Info("waiting for signal messages",
			"timeout_seconds", timeoutSeconds, "max_messages", maxMessages, "ignore_attachments", ignoreAttachments)

		messages, err := client.ReceiveMessages(ctx, timeoutSeconds, maxMessages, ignoreAttachments)
		if err != nil {
			logger.Error("failed to receive signal messages", "error", err)
			return nil, err
		}

		if len(messages) == 0 {
			return &ToolCallResult{
				Content: []ContentBlock{{
					Type: "text",
					Text: fmt.Sprintf("No new messages received within %d second(s).", timeoutSeconds),
				}},
			}, nil
		}

		b, err := json.MarshalIndent(messages, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("formatting messages: %w", err)
		}

		return &ToolCallResult{
			Content: []ContentBlock{{
				Type: "text",
				Text: fmt.Sprintf("Received %d message(s):\n%s", len(messages), string(b)),
			}},
		}, nil
	}
}
