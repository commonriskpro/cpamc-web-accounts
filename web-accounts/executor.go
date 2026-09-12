package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const qoderTimeout = 45 * time.Second

var allowedRequestFields = map[string]bool{
	"model": true, "messages": true, "stream": true, "reasoning_effort": true, "reasoning": true,
	"thinking": true, "tools": true, "functions": true,
}

type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []chatMessage   `json:"messages"`
	Tools           json.RawMessage `json:"tools"`
	Functions       json.RawMessage `json:"functions"`
	ReasoningEffort string          `json:"reasoning_effort"`
	Reasoning       json.RawMessage `json:"reasoning"`
	Thinking        json.RawMessage `json:"thinking"`
}
type chatMessage struct {
	Role      string          `json:"role"`
	Content   any             `json:"content"`
	ToolCalls json.RawMessage `json:"tool_calls"`
}

type responseAccumulator struct {
	Text      string
	Reasoning string
	Stop      string
}

func execute(ctx context.Context, req executorRequest, streaming bool) (executorResponse, error) {
	provider, credential, err := readCredential(req.StorageJSON)
	if err != nil {
		return executorResponse{}, err
	}
	if req.AuthProvider != "" && req.AuthProvider != providerID {
		return executorResponse{}, errors.New("web account auth provider mismatch")
	}
	if err := validateFormat(req.Format); err != nil {
		return executorResponse{}, err
	}
	body, err := requestBody(req.Payload)
	if err != nil {
		return executorResponse{}, err
	}
	requestedModel := strings.TrimSpace(req.Model)
	if requestedModel == "" {
		requestedModel = body.Model
	}
	upstreamModel := resolveUpstreamModel(provider, requestedModel)
	if upstreamModel == "" {
		return executorResponse{}, errors.New("requested model is not available for this web account")
	}
	if err := validateRequest(body, provider, upstreamModel); err != nil {
		return executorResponse{}, err
	}
	if provider == "qoder" {
		text, err := runQoder(ctx, credential, body, upstreamModel)
		if err != nil {
			return executorResponse{}, err
		}
		return executorResponse{Payload: completionJSON(requestedModel, text, "stop"), Headers: map[string][]string{"Content-Type": {"application/json"}}}, nil
	}
	if provider == "claude" {
		return executeClaude(ctx, credential, body, upstreamModel, requestedModel, streaming, req.HostCallbackID)
	}
	return executeKimi(ctx, credential, body, upstreamModel, requestedModel, streaming, req.HostCallbackID)
}

func validateFormat(format string) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "chat-completions", "chat_completions", "openai", "openai-chat-completions", "openai_chat_completions":
		return nil
	default:
		return errors.New("unsupported executor input format")
	}
}

func connectorError(message string, err error) error {
	if err != nil {
		var statusErr interface{ StatusCode() int }
		if errors.As(err, &statusErr) {
			out := httpStatusError{message: message, statusCode: statusErr.StatusCode()}
			if retry, ok := err.(interface{ RetryAfter() string }); ok {
				out.retryAfter = retry.RetryAfter()
			}
			return out
		}
	}
	return errors.New(message)
}

func upstreamResponseError(message string, response httpResponse) error {
	if response.StatusCode >= 400 && response.StatusCode <= 599 {
		retryAfter := ""
		for key, values := range response.Headers {
			if strings.EqualFold(key, "Retry-After") && len(values) > 0 {
				retryAfter = values[0]
				break
			}
		}
		return httpStatusError{message: message, statusCode: response.StatusCode, retryAfter: retryAfter}
	}
	return errors.New(message)
}

func requestBody(payload []byte) (chatRequest, error) {
	if len(payload) == 0 || len(payload) > maxBodyBytes {
		return chatRequest{}, errors.New("chat request body is missing or too large")
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(payload, &raw) != nil {
		return chatRequest{}, errors.New("chat request JSON is invalid")
	}
	for key := range raw {
		if !allowedRequestFields[key] {
			return chatRequest{}, errors.New("chat request contains unsupported parameters")
		}
	}
	messageValues, ok := raw["messages"]
	if !ok || !validateMessageShape(messageValues) {
		return chatRequest{}, errors.New("chat request messages contain unsupported fields")
	}
	var body chatRequest
	if json.Unmarshal(payload, &body) != nil || len(body.Messages) == 0 {
		return chatRequest{}, errors.New("chat request messages are required")
	}
	return body, nil
}

func validateMessageShape(raw json.RawMessage) bool {
	var messages []map[string]json.RawMessage
	if json.Unmarshal(raw, &messages) != nil || len(messages) == 0 {
		return false
	}
	for _, message := range messages {
		if _, ok := message["role"]; !ok {
			return false
		}
		if _, ok := message["content"]; !ok {
			return false
		}
		for key := range message {
			if key != "role" && key != "content" && key != "tool_calls" {
				return false
			}
		}
		if toolCalls, ok := message["tool_calls"]; ok && !isNullOrEmptyArray(toolCalls) {
			return false
		}
	}
	return true
}

func isNullOrEmptyArray(raw json.RawMessage) bool {
	if string(raw) == "null" {
		return true
	}
	var values []json.RawMessage
	return json.Unmarshal(raw, &values) == nil && len(values) == 0
}

func validateRequest(body chatRequest, provider, model string) error {
	if body.Tools != nil && !isNullOrEmptyArray(body.Tools) {
		return errors.New("web accounts support text chat only; tools are unsupported")
	}
	if body.Functions != nil && !isNullOrEmptyArray(body.Functions) {
		return errors.New("web accounts support text chat only; functions are unsupported")
	}
	if provider == "claude" || provider == "qoder" {
		if body.ReasoningEffort != "" || (body.Reasoning != nil && string(body.Reasoning) != "null") || (body.Thinking != nil && string(body.Thinking) != "null") {
			return errors.New("this web connector does not support reasoning or thinking parameters")
		}
	}
	if provider == "kimi" {
		if (body.Reasoning != nil && string(body.Reasoning) != "null") || (body.Thinking != nil && string(body.Thinking) != "null") {
			return errors.New("Kimi Web accepts reasoning_effort only")
		}
		if _, err := normalizeKimiReasoning(body.ReasoningEffort); err != nil {
			return err
		}
	}
	if provider == "kimi" && model != "k3" && model != "k2d6" {
		return errors.New("unsupported Kimi Web model")
	}
	if provider == "qoder" && model != "qwen3.8-max-preview" {
		return errors.New("unsupported Qoder model")
	}
	return nil
}

func normalizeKimiReasoning(value string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "REASONING_EFFORT_NONE", nil
	}
	normalized = strings.ToUpper(normalized)
	if !strings.HasPrefix(normalized, "REASONING_EFFORT_") {
		normalized = "REASONING_EFFORT_" + normalized
	}
	if normalized != "REASONING_EFFORT_NONE" && normalized != "REASONING_EFFORT_LOW" {
		return "", errors.New("unsupported Kimi Web reasoning effort")
	}
	return normalized, nil
}
func resolveUpstreamModel(provider, requested string) string {
	for _, m := range modelsForProvider(provider) {
		if m.ID == requested {
			return m.Name
		}
	}
	return ""
}
func supportedModel(provider, model string) bool { return resolveUpstreamModel(provider, model) != "" }
func modelsForProvider(provider string) []modelInfo {
	return modelsForAuth(mustStorage(provider), map[string]string{"web_provider": provider})
}
func mustStorage(provider string) []byte {
	b, _ := json.Marshal(map[string]string{"web_provider": provider, "access_token": "placeholder"})
	if provider == "claude" {
		b, _ = json.Marshal(map[string]string{"web_provider": provider, "cookie": "sessionKey=placeholder"})
	}
	if provider == "qoder" {
		b, _ = json.Marshal(map[string]string{"web_provider": provider, "personal_access_token": "placeholder"})
	}
	return b
}

func messagesText(messages []chatMessage, kimi bool) (string, string, error) {
	var system []string
	var transcript []string
	var nonEmptyUser bool
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "" {
			return "", "", errors.New("message role is required")
		}
		text, err := contentText(message.Content)
		if err != nil {
			return "", "", err
		}
		if message.ToolCalls != nil && string(message.ToolCalls) != "null" && string(message.ToolCalls) != "[]" {
			return "", "", errors.New("tool calls are unsupported")
		}
		switch role {
		case "system", "developer":
			system = append(system, text)
		case "user":
			transcript = append(transcript, "User: "+text)
			if strings.TrimSpace(text) != "" {
				nonEmptyUser = true
			}
		case "assistant":
			transcript = append(transcript, "Assistant: "+text)
		default:
			return "", "", errors.New("unsupported message role")
		}
	}
	prompt := strings.TrimSpace(strings.Join(transcript, "\n\n"))
	if prompt == "" || !nonEmptyUser {
		return "", "", errors.New("a non-empty user message is required")
	}
	if kimi {
		return prompt, strings.TrimSpace(strings.Join(system, "\n\n")), nil
	}
	return prompt, strings.TrimSpace(strings.Join(system, "\n\n")), nil
}
func contentText(content any) (string, error) {
	if s, ok := content.(string); ok {
		return s, nil
	}
	parts, ok := content.([]any)
	if !ok {
		return "", errors.New("web accounts support text message content only")
	}
	var out []string
	for _, part := range parts {
		m, ok := part.(map[string]any)
		if !ok {
			return "", errors.New("web accounts support text message content only")
		}
		for key := range m {
			if key != "type" && key != "text" {
				return "", errors.New("web accounts reject unsupported message content fields")
			}
		}
		typ, _ := m["type"].(string)
		if typ != "text" && typ != "input_text" {
			return "", errors.New("web accounts do not support image, audio, file, or tool content")
		}
		text, ok := m["text"].(string)
		if !ok {
			return "", errors.New("web accounts require text content")
		}
		out = append(out, text)
	}
	return strings.Join(out, ""), nil
}

func executeClaude(ctx context.Context, credential string, body chatRequest, model, publicModel string, streaming bool, callbackID string) (executorResponse, error) {
	cookie, err := normalizeClaudeCookie(credential)
	if err != nil {
		return executorResponse{}, err
	}
	orgResp, err := doHTTP(httpRequest{Method: "GET", URL: "https://claude.ai/api/organizations", Headers: map[string][]string{"Accept": {"application/json"}, "Cookie": {cookie}, "User-Agent": {"Mozilla/5.0"}}, HostCallbackID: callbackID})
	if err != nil {
		return executorResponse{}, connectorError("Claude Web organization request failed", err)
	}
	if orgResp.StatusCode == 401 || orgResp.StatusCode == 403 {
		return executorResponse{}, upstreamResponseError("Claude Web session expired or rejected", orgResp)
	}
	if orgResp.StatusCode != 200 {
		return executorResponse{}, upstreamResponseError("Claude Web organization request failed", orgResp)
	}
	org, err := organizationID(orgResp.Body)
	if err != nil {
		return executorResponse{}, err
	}
	prompt, system, err := messagesText(body.Messages, false)
	if err != nil {
		return executorResponse{}, err
	}
	if system != "" {
		prompt = "System: " + system + "\n\n" + prompt
	}
	conversationID := randomUUID()
	payload := map[string]any{"prompt": prompt, "model": model, "timezone": "UTC", "personalized_styles": []any{map[string]any{"type": "default", "key": "Default", "name": "Normal", "nameKey": "normal_style_name", "prompt": "Normal\n", "summary": "Default responses from Claude", "summaryKey": "normal_style_summary", "isDefault": true}}, "locale": "en-US", "tools": []any{}, "turn_message_uuids": map[string]string{"human_message_uuid": randomUUID(), "assistant_message_uuid": randomUUID()}, "attachments": []any{}, "effort": "low", "files": []any{}, "sync_sources": []any{}, "rendering_mode": "messages", "thinking_mode": "off", "create_conversation_params": map[string]any{"name": "", "model": model, "include_conversation_preferences": true, "paprika_mode": nil, "compass_mode": nil, "is_temporary": false, "enabled_imagine": true, "tool_search_mode": "auto"}}
	requestBody, _ := json.Marshal(payload)
	url := "https://claude.ai/api/organizations/" + org + "/chat_conversations/" + conversationID + "/completion"
	if streaming {
		return streamClaude(ctx, httpRequest{Method: "POST", URL: url, Headers: map[string][]string{"Accept": {"text/event-stream"}, "Content-Type": {"application/json"}, "Cookie": {cookie}, "User-Agent": {"Mozilla/5.0"}, "Origin": {"https://claude.ai"}, "Referer": {"https://claude.ai/new"}}, Body: requestBody, HostCallbackID: callbackID}, publicModel)
	}
	resp, err := doHTTP(httpRequest{Method: "POST", URL: url, Headers: map[string][]string{"Accept": {"text/event-stream"}, "Content-Type": {"application/json"}, "Cookie": {cookie}, "User-Agent": {"Mozilla/5.0"}, "Origin": {"https://claude.ai"}, "Referer": {"https://claude.ai/new"}}, Body: requestBody, HostCallbackID: callbackID})
	if err != nil {
		return executorResponse{}, connectorError("Claude Web completion request failed", err)
	}
	if resp.StatusCode != 200 {
		return executorResponse{}, upstreamResponseError("Claude Web completion request failed", resp)
	}
	acc, err := parseClaudeSSE(resp.Body)
	if err != nil {
		return executorResponse{}, err
	}
	return executorResponse{Payload: completionJSON(publicModel, acc.Text, acc.Stop), Headers: map[string][]string{"Content-Type": {"application/json"}}}, nil
}
func organizationID(body []byte) (string, error) {
	var list []map[string]any
	if json.Unmarshal(body, &list) != nil || len(list) == 0 {
		return "", errors.New("Claude Web organization response is invalid")
	}
	for _, item := range list {
		for _, key := range []string{"uuid", "id"} {
			if s, ok := item[key].(string); ok && s != "" && noCRLF(s) {
				return s, nil
			}
		}
	}
	return "", errors.New("Claude Web organization response is invalid")
}

func executeKimi(ctx context.Context, credential string, body chatRequest, model, publicModel string, streaming bool, callbackID string) (executorResponse, error) {
	token := extractKimiToken(credential)
	if token == "" || !noCRLF(token) {
		return executorResponse{}, errors.New("Kimi access_token is required")
	}
	prompt, system, err := messagesText(body.Messages, true)
	if err != nil {
		return executorResponse{}, err
	}
	config := map[string]any{"scenario": "SCENARIO_K2D5", "tools": []any{}, "message": map[string]any{"id": "", "parent_id": "", "children_message_ids": []string{}, "role": "user", "blocks": []any{map[string]any{"id": "", "message_id": "", "text": map[string]string{"content": prompt}}}, "scenario": "SCENARIO_K2D5", "labels": []any{}, "references": []any{}, "is_goal": false}, "options": map[string]any{"thinking": true, "enable_plugin": false}, "project_id": "", "chat_id": ""}
	if system != "" {
		config["options"].(map[string]any)["system_prompt"] = system
	}
	normalizedReasoning, err := normalizeKimiReasoning(body.ReasoningEffort)
	if err != nil {
		return executorResponse{}, err
	}
	config["options"].(map[string]any)["reasoning_effort"] = normalizedReasoning
	if model == "k3" {
		config["scenario"] = "SCENARIO_K2D5"
		config["message"].(map[string]any)["scenario"] = "SCENARIO_K2D5"
	}
	jsonBody, _ := json.Marshal(config)
	framed := frameConnectMessage(jsonBody)
	req := httpRequest{Method: "POST", URL: "https://www.kimi.ai/apiv2/kimi.gateway.chat.v1.ChatService/Chat", Headers: map[string][]string{"Content-Type": {"application/connect+json"}, "Accept": {"*/*"}, "Authorization": {"Bearer " + token}, "User-Agent": {"Mozilla/5.0"}, "Origin": {"https://www.kimi.ai"}, "Referer": {"https://www.kimi.ai/"}, "connect-protocol-version": {"1"}}, Body: framed, HostCallbackID: callbackID}
	if streaming {
		return streamKimi(ctx, req, publicModel)
	}
	resp, err := doHTTP(req)
	if err != nil {
		return executorResponse{}, connectorError("Kimi Web request failed", err)
	}
	if resp.StatusCode != 200 {
		return executorResponse{}, upstreamResponseError("Kimi Web request failed", resp)
	}
	acc, err := parseKimiStream(resp.Body)
	if err != nil {
		return executorResponse{}, err
	}
	return executorResponse{Payload: completionJSONWithReasoning(publicModel, acc.Text, acc.Reasoning, "stop"), Headers: map[string][]string{"Content-Type": {"application/json"}}}, nil
}

func completionJSON(model, text, finish string) []byte {
	return completionJSONWithReasoning(model, text, "", finish)
}
func completionJSONWithReasoning(model, text, reasoning, finish string) []byte {
	id := "chatcmpl-" + randomUUID()
	msg := map[string]any{"role": "assistant", "content": text}
	if reasoning != "" {
		msg["reasoning_content"] = reasoning
	}
	out := map[string]any{"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": model, "choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish, "logprobs": nil}}}
	b, _ := json.Marshal(out)
	return b
}
func completionChunk(model string, delta map[string]any, finish *string) []byte {
	return completionChunkWithID("chatcmpl-"+randomUUID(), model, delta, finish)
}
func completionChunkWithID(id, model string, delta map[string]any, finish *string) []byte {
	out := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish, "logprobs": nil}}}
	b, _ := json.Marshal(out)
	return append(append([]byte("data: "), b...), []byte("\n\n")...)
}

func qoderStreamChunks(payload []byte, model string) ([]streamChunk, error) {
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(payload, &completion) != nil || len(completion.Choices) != 1 {
		return nil, errors.New("Qoder CLI response cannot be streamed")
	}
	stop := completion.Choices[0].FinishReason
	if stop == "" {
		stop = "stop"
	}
	id := "chatcmpl-" + randomUUID()
	return []streamChunk{
		{Payload: completionChunkWithID(id, model, map[string]any{"role": "assistant", "content": ""}, nil)},
		{Payload: completionChunkWithID(id, model, map[string]any{"content": completion.Choices[0].Message.Content}, nil)},
		{Payload: completionChunkWithID(id, model, map[string]any{}, &stop)},
		{Payload: []byte("data: [DONE]\n\n")},
	}, nil
}

func streamClaude(ctx context.Context, req httpRequest, model string) (executorResponse, error) {
	return streamViaHost(ctx, req, model, "claude")
}
func streamKimi(ctx context.Context, req httpRequest, model string) (executorResponse, error) {
	return streamViaHost(ctx, req, model, "kimi")
}
func streamViaHost(_ context.Context, req httpRequest, _ string, _ string) (executorResponse, error) {
	return executorResponse{}, errors.New("internal stream dispatch error")
}

func executeAsync(ctx context.Context, worker *streamWorker, req executorRequest) {
	provider, _, err := readCredential(req.StorageJSON)
	if err != nil {
		closeStream(req.StreamID, "web account request failed")
		return
	}
	body, err := requestBody(req.Payload)
	if err != nil {
		closeStream(req.StreamID, "web account request failed")
		return
	}
	requestedModel := req.Model
	if requestedModel == "" {
		requestedModel = body.Model
	}
	publicModel := requestedModel
	model := resolveUpstreamModel(provider, publicModel)
	if model == "" || validateRequest(body, provider, model) != nil {
		closeStream(req.StreamID, "web account request rejected")
		return
	}
	if provider == "qoder" {
		response, err := execute(ctx, req, false)
		if err != nil {
			closeStream(req.StreamID, "Qoder CLI request failed")
			return
		}
		_ = emitStream(req.StreamID, append(append([]byte("data: "), response.Payload...), []byte("\n\n")...))
		_ = emitStream(req.StreamID, []byte("data: [DONE]\n\n"))
		closeStream(req.StreamID, "")
		return
	}
	var h httpRequest
	if provider == "claude" {
		h, err = claudeHTTPRequest(req.StorageJSON, body, model, req.HostCallbackID)
	} else {
		h, err = kimiHTTPRequest(req.StorageJSON, body, model, req.HostCallbackID)
	}
	if err != nil {
		closeStream(req.StreamID, "web account request failed")
		return
	}
	upstream, err := openHTTPStream(h)
	if err != nil {
		closeStream(req.StreamID, "upstream stream unavailable")
		return
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		closeHTTPStream(upstream.StreamID)
		closeStream(req.StreamID, "upstream request failed")
		return
	}
	if !worker.setUpstream(upstream.StreamID) {
		closeHTTPStream(upstream.StreamID)
		closeStream(req.StreamID, "plugin is quiescing")
		return
	}
	if provider == "claude" {
		streamClaudeChunks(req.StreamID, upstream.StreamID, publicModel, ctx)
	} else {
		streamKimiChunks(req.StreamID, upstream.StreamID, publicModel, ctx)
	}
}
func claudeHTTPRequest(storage []byte, body chatRequest, model string, callbackID string) (httpRequest, error) {
	p, c, err := readCredential(storage)
	if err != nil || p != "claude" {
		return httpRequest{}, errors.New("invalid Claude credential")
	}
	cookie, err := normalizeClaudeCookie(c)
	if err != nil {
		return httpRequest{}, err
	}
	prompt, system, err := messagesText(body.Messages, false)
	if err != nil {
		return httpRequest{}, err
	}
	if system != "" {
		prompt = "System: " + system + "\n\n" + prompt
	}
	orgResp, err := doHTTP(httpRequest{Method: "GET", URL: "https://claude.ai/api/organizations", Headers: map[string][]string{"Accept": {"application/json"}, "Cookie": {cookie}, "User-Agent": {"Mozilla/5.0"}}, HostCallbackID: callbackID})
	if err != nil || orgResp.StatusCode != 200 {
		return httpRequest{}, errors.New("Claude Web organization request failed")
	}
	org, err := organizationID(orgResp.Body)
	if err != nil {
		return httpRequest{}, err
	}
	payload := map[string]any{"prompt": prompt, "model": model, "timezone": "UTC", "personalized_styles": []any{map[string]any{"type": "default", "key": "Default", "name": "Normal", "nameKey": "normal_style_name", "prompt": "Normal\n", "summary": "Default responses from Claude", "summaryKey": "normal_style_summary", "isDefault": true}}, "locale": "en-US", "tools": []any{}, "turn_message_uuids": map[string]string{"human_message_uuid": randomUUID(), "assistant_message_uuid": randomUUID()}, "attachments": []any{}, "effort": "low", "files": []any{}, "sync_sources": []any{}, "rendering_mode": "messages", "thinking_mode": "off", "create_conversation_params": map[string]any{"name": "", "model": model, "include_conversation_preferences": true, "paprika_mode": nil, "compass_mode": nil, "is_temporary": false, "enabled_imagine": true, "tool_search_mode": "auto"}}
	b, _ := json.Marshal(payload)
	return httpRequest{Method: "POST", URL: "https://claude.ai/api/organizations/" + org + "/chat_conversations/" + randomUUID() + "/completion", Headers: map[string][]string{"Accept": {"text/event-stream"}, "Content-Type": {"application/json"}, "Cookie": {cookie}, "User-Agent": {"Mozilla/5.0"}, "Origin": {"https://claude.ai"}, "Referer": {"https://claude.ai/new"}}, Body: b, HostCallbackID: callbackID}, nil
}
func kimiHTTPRequest(storage []byte, body chatRequest, model string, callbackID string) (httpRequest, error) {
	p, c, err := readCredential(storage)
	if err != nil || p != "kimi" {
		return httpRequest{}, errors.New("invalid Kimi credential")
	}
	token := extractKimiToken(c)
	if token == "" || !noCRLF(token) {
		return httpRequest{}, errors.New("invalid Kimi credential")
	}
	prompt, system, err := messagesText(body.Messages, true)
	if err != nil {
		return httpRequest{}, err
	}
	config := map[string]any{"chat_id": "", "kimiplus_id": "", "scenario": "SCENARIO_K2D5", "tools": []any{}, "message": map[string]any{"id": "", "parent_id": "", "children_message_ids": []string{}, "role": "user", "blocks": []any{map[string]any{"id": "", "message_id": "", "text": map[string]string{"content": prompt}}}, "scenario": "SCENARIO_K2D5", "labels": []any{}, "references": []any{}, "is_goal": false}, "options": map[string]any{"thinking": true, "enable_plugin": false}, "project_id": ""}
	if system != "" {
		config["options"].(map[string]any)["system_prompt"] = system
	}
	reasoningEffort, err := normalizeKimiReasoning(body.ReasoningEffort)
	if err != nil {
		return httpRequest{}, err
	}
	config["options"].(map[string]any)["reasoning_effort"] = reasoningEffort
	b, _ := json.Marshal(config)
	return httpRequest{Method: "POST", URL: "https://www.kimi.ai/apiv2/kimi.gateway.chat.v1.ChatService/Chat", Headers: map[string][]string{"Content-Type": {"application/connect+json"}, "Accept": {"*/*"}, "Authorization": {"Bearer " + token}, "User-Agent": {"Mozilla/5.0"}, "Origin": {"https://www.kimi.ai"}, "Referer": {"https://www.kimi.ai/"}, "connect-protocol-version": {"1"}}, Body: frameConnectMessage(b), HostCallbackID: callbackID}, nil
}

func finishStream(targetID, upstreamID, message string) {
	// Close the upstream before closing the output bridge. Closing the output
	// bridge releases the host callback context; doing this in a deferred call
	// after closeStream races plugin unload and can invoke a stale C callback.
	closeHTTPStream(upstreamID)
	closeStream(targetID, message)
}

func streamClaudeChunks(targetID, upstreamID, model string, ctx context.Context) {
	decoder := newSSEDecoder()
	streamChunkID := "chatcmpl-" + randomUUID()
	var started bool
	for {
		select {
		case <-ctx.Done():
			finishStream(targetID, upstreamID, "Claude Web stream canceled")
			return
		default:
		}
		chunk, err := readHTTPStream(upstreamID)
		if err != nil || chunk.Error != "" {
			finishStream(targetID, upstreamID, "Claude Web stream failed")
			return
		}
		events, err := decoder.feed(chunk.Payload)
		if err != nil {
			finishStream(targetID, upstreamID, "Claude Web stream protocol error")
			return
		}
		if chunk.Done {
			tail, finishErr := decoder.finish()
			if finishErr != nil {
				finishStream(targetID, upstreamID, "Claude Web stream protocol error")
				return
			}
			events = append(events, tail...)
		}
		for _, ev := range events {
			if ev.kind == "start" && !started {
				started = true
				_ = emitStream(targetID, completionChunkWithID(streamChunkID, model, map[string]any{"role": "assistant", "content": ""}, nil))
			}
			if ev.kind == "text" {
				if !started {
					started = true
					_ = emitStream(targetID, completionChunkWithID(streamChunkID, model, map[string]any{"role": "assistant", "content": ""}, nil))
				}
				_ = emitStream(targetID, completionChunkWithID(streamChunkID, model, map[string]any{"content": ev.text}, nil))
			}
			if ev.kind == "reasoning" {
				_ = emitStream(targetID, completionChunkWithID(streamChunkID, model, map[string]any{"reasoning_content": ev.text}, nil))
			}
			if ev.kind == "finish" {
				f := ev.stop
				_ = emitStream(targetID, completionChunkWithID(streamChunkID, model, map[string]any{}, &f))
				_ = emitStream(targetID, []byte("data: [DONE]\n\n"))
				finishStream(targetID, upstreamID, "")
				return
			}
		}
		if chunk.Done {
			finishStream(targetID, upstreamID, "Claude Web stream ended before message_stop")
			return
		}
	}
}
func streamKimiChunks(targetID, upstreamID, model string, ctx context.Context) {
	decoder := &connectDecoder{}
	streamChunkID := "chatcmpl-" + randomUUID()
	var started bool
	for {
		select {
		case <-ctx.Done():
			finishStream(targetID, upstreamID, "Kimi Web stream canceled")
			return
		default:
		}
		chunk, err := readHTTPStream(upstreamID)
		if err != nil || chunk.Error != "" {
			finishStream(targetID, upstreamID, "Kimi Web stream failed")
			return
		}
		frames, err := decoder.feed(chunk.Payload)
		if err != nil {
			finishStream(targetID, upstreamID, "Kimi Web stream protocol error")
			return
		}
		for _, frame := range frames {
			if frame.end {
				if frame.err != "" {
					finishStream(targetID, upstreamID, "Kimi Web stream failed")
					return
				}
				if !started {
					started = true
					_ = emitStream(targetID, completionChunkWithID(streamChunkID, model, map[string]any{"role": "assistant", "content": ""}, nil))
				}
				stop := "stop"
				_ = emitStream(targetID, completionChunkWithID(streamChunkID, model, map[string]any{}, &stop))
				_ = emitStream(targetID, []byte("data: [DONE]\n\n"))
				finishStream(targetID, upstreamID, "")
				return
			}
			if frame.kind != "" {
				if !started {
					started = true
					_ = emitStream(targetID, completionChunkWithID(streamChunkID, model, map[string]any{"role": "assistant", "content": ""}, nil))
				}
				d := map[string]any{}
				if frame.kind == "text" {
					d["content"] = frame.text
				} else {
					d["reasoning_content"] = frame.text
				}
				_ = emitStream(targetID, completionChunkWithID(streamChunkID, model, d, nil))
			}
		}
		if chunk.Done {
			finishStream(targetID, upstreamID, "Kimi Web stream ended without EndStream")
			return
		}
	}
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return 0, errors.New("output limit exceeded")
	}
	if len(p) > remaining {
		_, _ = b.buffer.Write(p[:remaining])
		b.exceeded = true
		return remaining, errors.New("output limit exceeded")
	}
	return b.buffer.Write(p)
}

func runQoder(ctx context.Context, token string, body chatRequest, model string) (string, error) {
	command, err := findQoderCommand()
	if err != nil {
		return "", err
	}
	prompt, system, err := messagesText(body.Messages, false)
	if err != nil {
		return "", err
	}
	if system != "" {
		prompt = "System: " + system + "\n\n" + prompt
	}
	dir, err := os.MkdirTemp("", "cpamc-qoder-")
	if err != nil {
		return "", errors.New("Qoder isolated config could not be created")
	}
	defer os.RemoveAll(dir)
	args := []string{"--print", "--output-format", "json", "--model", "qmodel_preview", "--tools", "", "--config-dir", dir}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	pathEnv := strings.Join([]string{filepath.Dir(command), "/usr/bin", "/bin"}, string(os.PathListSeparator))
	cmd.Env = []string{"PATH=" + pathEnv, "HOME=" + dir, "QODER_PERSONAL_ACCESS_TOKEN=" + token}
	cmd.Stdin = strings.NewReader(prompt)
	var stdout limitedBuffer
	stdout.limit = maxBodyBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &limitedBuffer{limit: 64 << 10}
	if err := cmd.Run(); err != nil || stdout.exceeded {
		if stdout.exceeded {
			return "", errors.New("Qoder CLI response is too large")
		}
		return "", errors.New("Qoder CLI request failed")
	}
	var envelope map[string]any
	if json.Unmarshal(stdout.buffer.Bytes(), &envelope) != nil {
		return "", errors.New("Qoder CLI returned invalid JSON")
	}
	if isErr, _ := envelope["is_error"].(bool); isErr {
		return "", errors.New("Qoder CLI returned an error")
	}
	for _, key := range []string{"result", "content", "text", "response"} {
		if text, ok := envelope[key].(string); ok {
			return text, nil
		}
	}
	return "", errors.New("Qoder CLI returned no text")
}

func findQoderCommand() (string, error) {
	for _, name := range []string{"qodercli", "qoder"} {
		if command, err := exec.LookPath(name); err == nil {
			return command, nil
		}
	}
	return "", errors.New("official Qoder CLI is unavailable; expose qodercli or qoder on PATH")
}
