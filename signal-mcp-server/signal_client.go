package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SignalClient talks to a signal-cli-rest-api instance
// (https://github.com/bbernhard/signal-cli-rest-api).
type SignalClient struct {
	baseURL string
	number  string
	token   string
	http    *http.Client
	logger  *slog.Logger
}

func NewSignalClient(baseURL, number, token string, timeout time.Duration, logger *slog.Logger) *SignalClient {
	return &SignalClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		number:  number,
		token:   token,
		http:    &http.Client{Timeout: timeout},
		logger:  logger,
	}
}

// --- POST /v2/send ---

type sendMessageV2Request struct {
	Message           string   `json:"message"`
	Number            string   `json:"number"`
	Recipients        []string `json:"recipients"`
	TextMode          string   `json:"text_mode,omitempty"`
	Base64Attachments []string `json:"base64_attachments,omitempty"`
}

type sendMessageV2Response struct {
	Timestamp string `json:"timestamp"`
}

// SendMessage sends a message to one or more recipients via POST /v2/send.
// A recipient may be a phone number (E.164), a Signal username, or (for
// group messages) a Signal group ID - signal-cli-rest-api figures out which
// from the value itself, no separate flag is required.
func (c *SignalClient) SendMessage(ctx context.Context, recipients []string, message, textMode string, attachments []string) (string, error) {
	reqBody := sendMessageV2Request{
		Message:           message,
		Number:            c.number,
		Recipients:        recipients,
		TextMode:          textMode,
		Base64Attachments: attachments,
	}

	var respBody sendMessageV2Response
	if err := c.doJSON(ctx, http.MethodPost, "/v2/send", nil, reqBody, &respBody); err != nil {
		return "", err
	}
	return respBody.Timestamp, nil
}

// --- GET /v1/receive/{number} ---

type receiveDataMessage struct {
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
	GroupInfo *struct {
		GroupID   string `json:"groupId"`
		GroupName string `json:"groupName"`
	} `json:"groupInfo,omitempty"`
	Attachments []struct {
		ContentType string `json:"contentType"`
		Filename    string `json:"filename"`
		ID          string `json:"id"`
		Size        int64  `json:"size"`
	} `json:"attachments,omitempty"`
}

type receiveEnvelope struct {
	Source         string              `json:"source"`
	SourceNumber   string              `json:"sourceNumber"`
	SourceName     string              `json:"sourceName"`
	SourceUUID     string              `json:"sourceUuid"`
	Timestamp      int64               `json:"timestamp"`
	DataMessage    *receiveDataMessage `json:"dataMessage,omitempty"`
	SyncMessage    json.RawMessage     `json:"syncMessage,omitempty"`
	TypingMessage  json.RawMessage     `json:"typingMessage,omitempty"`
	ReceiptMessage json.RawMessage     `json:"receiptMessage,omitempty"`
	CallMessage    json.RawMessage     `json:"callMessage,omitempty"`
}

type receiveEntry struct {
	Account  string          `json:"account"`
	Envelope receiveEnvelope `json:"envelope"`
}

// ReceivedMessage is the simplified shape returned to MCP clients.
type ReceivedMessage struct {
	EventType       string `json:"event_type"` // message | typing | receipt | call | sync | other
	Source          string `json:"source"`
	SourceName      string `json:"source_name,omitempty"`
	Timestamp       int64  `json:"timestamp"`
	Message         string `json:"message,omitempty"`
	GroupID         string `json:"group_id,omitempty"`
	GroupName       string `json:"group_name,omitempty"`
	AttachmentCount int    `json:"attachment_count,omitempty"`
}

// ReceiveMessages long-polls GET /v1/receive/{number} for incoming messages,
// waiting up to timeoutSeconds for at least one to arrive.
func (c *SignalClient) ReceiveMessages(ctx context.Context, timeoutSeconds, maxMessages int, ignoreAttachments bool) ([]ReceivedMessage, error) {
	q := url.Values{}
	q.Set("timeout", strconv.Itoa(timeoutSeconds))
	if maxMessages > 0 {
		q.Set("max_messages", strconv.Itoa(maxMessages))
	}
	q.Set("ignore_attachments", strconv.FormatBool(ignoreAttachments))

	// Our own HTTP client timeout must exceed the server-side receive timeout,
	// otherwise we'd cancel the request before signal-cli-rest-api responds.
	reqCtx := ctx
	if timeoutSeconds > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds+10)*time.Second)
		defer cancel()
	}

	path := fmt.Sprintf("/v1/receive/%s", url.PathEscape(c.number))

	var raw json.RawMessage
	if err := c.doJSON(reqCtx, http.MethodGet, path, q, nil, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}

	var entries []receiveEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		// Be defensive: some deployments have been observed to return a
		// single object rather than an array when there's exactly one message.
		var single receiveEntry
		if err2 := json.Unmarshal(raw, &single); err2 != nil {
			return nil, fmt.Errorf("parsing receive response: %w", err)
		}
		entries = []receiveEntry{single}
	}

	out := make([]ReceivedMessage, 0, len(entries))
	for _, e := range entries {
		env := e.Envelope
		rm := ReceivedMessage{
			Source:     firstNonEmpty(env.SourceNumber, env.Source, env.SourceUUID),
			SourceName: env.SourceName,
			Timestamp:  env.Timestamp,
		}
		switch {
		case env.DataMessage != nil:
			rm.EventType = "message"
			rm.Message = env.DataMessage.Message
			rm.AttachmentCount = len(env.DataMessage.Attachments)
			if env.DataMessage.GroupInfo != nil {
				rm.GroupID = env.DataMessage.GroupInfo.GroupID
				rm.GroupName = env.DataMessage.GroupInfo.GroupName
			}
		case len(env.TypingMessage) > 0:
			rm.EventType = "typing"
		case len(env.ReceiptMessage) > 0:
			rm.EventType = "receipt"
		case len(env.CallMessage) > 0:
			rm.EventType = "call"
		case len(env.SyncMessage) > 0:
			rm.EventType = "sync"
		default:
			rm.EventType = "other"
		}
		out = append(out, rm)
	}

	c.logger.Info("received messages from signal", "count", len(out))
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// --- shared HTTP plumbing ---

func (c *SignalClient) doJSON(ctx context.Context, method, path string, query url.Values, body interface{}, out interface{}) error {
	fullURL := c.baseURL + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}

	var bodyReader io.Reader
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		bodyBytes = b
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	c.logger.Debug("sending request to signal-cli-rest-api",
		"method", method, "url", fullURL, "body", string(bodyBytes))

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.logger.Error("request to signal-cli-rest-api failed",
			"method", method, "url", fullURL, "error", err,
			"duration_ms", time.Since(start).Milliseconds())
		return fmt.Errorf("calling signal-cli-rest-api at %s: %w", fullURL, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	c.logger.Debug("received response from signal-cli-rest-api",
		"method", method, "url", fullURL, "status", resp.StatusCode,
		"duration_ms", time.Since(start).Milliseconds(), "body", string(respBytes))

	if resp.StatusCode >= 400 {
		c.logger.Warn("signal-cli-rest-api returned an error status",
			"method", method, "url", fullURL, "status", resp.StatusCode, "body", string(respBytes))
		return fmt.Errorf("signal-cli-rest-api returned %d: %s", resp.StatusCode, extractErrorMessage(respBytes))
	}

	if out == nil || len(respBytes) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBytes, out); err != nil {
		return fmt.Errorf("decoding response JSON: %w", err)
	}
	return nil
}

func extractErrorMessage(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error != "" {
		return e.Error
	}
	if len(body) == 0 {
		return "(empty response body)"
	}
	return string(body)
}
