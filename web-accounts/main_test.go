package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginLifecycleReconfigureRollbackAndShutdown(t *testing.T) {
	pluginState.mu.Lock()
	pluginState.active = false
	pluginState.shuttingDown = false
	pluginState.mu.Unlock()
	lifecycle = pluginLifecycle{active: make(map[*streamWorker]struct{})}
	t.Cleanup(func() {
		pluginState.mu.Lock()
		pluginState.active = false
		pluginState.shuttingDown = false
		pluginState.mu.Unlock()
		lifecycle = pluginLifecycle{active: make(map[*streamWorker]struct{})}
	})

	if !beginPluginInit() {
		t.Fatal("first init should be accepted")
	}
	if beginPluginInit() {
		t.Fatal("duplicate init should be rejected")
	}
	resetPluginLifecycle()

	if !beginPluginCall(methodPluginQuiesce) {
		t.Fatal("quiesce call should be accepted")
	}
	quiescePlugin()
	endPluginCall()
	if beginPluginCall(methodExecID) {
		t.Fatal("quiesced plugin accepted a new call")
	}
	if !beginPluginCall(methodReconfigure) {
		t.Fatal("reconfigure should be accepted while quiesced")
	}
	if !reopenPluginLifecycle() {
		t.Fatal("reconfigure did not reopen the lifecycle")
	}
	endPluginCall()
	if !beginPluginCall(methodExecID) {
		t.Fatal("reconfigured plugin rejected a call")
	}
	endPluginCall()

	if !beginPluginShutdown() {
		t.Fatal("shutdown should start for an active plugin")
	}
	shutdownPlugin()
	finishPluginShutdown()
	if beginPluginCall(methodReconfigure) {
		t.Fatal("shutdown reopened the plugin")
	}
	if !beginPluginInit() {
		t.Fatal("reinit after completed shutdown should be accepted")
	}
	abortPluginInit()
}

func TestReconfigureReturnsCompleteRegistration(t *testing.T) {
	pluginState.mu.Lock()
	pluginState.active = false
	pluginState.shuttingDown = false
	pluginState.mu.Unlock()
	lifecycle = pluginLifecycle{active: make(map[*streamWorker]struct{})}
	t.Cleanup(func() {
		pluginState.mu.Lock()
		pluginState.active = false
		pluginState.shuttingDown = false
		pluginState.mu.Unlock()
		lifecycle = pluginLifecycle{active: make(map[*streamWorker]struct{})}
	})
	if !beginPluginInit() {
		t.Fatal("init should be accepted")
	}
	quiescePlugin()
	raw, err := handleMethod(methodReconfigure, nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		t.Fatalf("reconfigure envelope = %s, %v", raw, err)
	}
	var got registration
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != schemaVersion || got.Metadata.Name != providerID || got.Metadata.Version == "" ||
		got.Metadata.Author == "" || got.Metadata.GitHubRepository == "" ||
		!got.Capabilities.ModelProvider || !got.Capabilities.AuthProvider || !got.Capabilities.Executor ||
		got.Capabilities.ExecutorModelScope != "oauth" || len(got.Capabilities.ExecutorInput) != 1 ||
		got.Capabilities.ExecutorInput[0] != "chat-completions" || len(got.Capabilities.ExecutorOutput) != 1 ||
		got.Capabilities.ExecutorOutput[0] != "chat-completions" {
		t.Fatalf("incomplete reconfigure registration: %#v", got)
	}
}

func TestParseAuthNormalizesCredentialsAndFiltersModels(t *testing.T) {
	claude, _ := json.Marshal(map[string]any{"type": providerID, "web_provider": "claude", "cookie": "sessionKey=synthetic"})
	resp, err := parseAuth(authParseRequest{RawJSON: claude, FileName: "claude.json"})
	if err != nil || !resp.Handled {
		t.Fatalf("Claude parse = %#v, %v", resp, err)
	}
	if got := modelsForAuth(resp.Auth.StorageJSON, resp.Auth.Attributes); len(got) != 9 || got[0].ID == "k3" {
		t.Fatalf("Claude models = %#v", got)
	}
	if !strings.Contains(string(resp.Auth.StorageJSON), "sessionKey=synthetic") {
		t.Fatal("canonical cookie missing")
	}

	kimi, _ := json.Marshal(map[string]any{"type": providerID, "web_provider": "kimi", "access_token": "synthetic-token"})
	resp, err = parseAuth(authParseRequest{RawJSON: kimi})
	if err != nil || !resp.Handled {
		t.Fatalf("Kimi parse = %#v, %v", resp, err)
	}
	got := modelsForAuth(resp.Auth.StorageJSON, resp.Auth.Attributes)
	if len(got) != 2 || got[0].ID == "claude-opus-5" {
		t.Fatalf("Kimi models = %#v", got)
	}
	if strings.Contains(resp.Auth.Label, "synthetic") || strings.Contains(resp.Auth.Attributes["web_provider"], "synthetic") {
		t.Fatal("secret escaped into auth metadata")
	}
}

func TestParseAuthUsesFilenameAsStableID(t *testing.T) {
	first := mustJSON(map[string]any{"type": providerID, "web_provider": "kimi", "access_token": "first-token"})
	second := mustJSON(map[string]any{"type": providerID, "web_provider": "kimi", "access_token": "rotated-token"})
	firstAuth, err := parseAuth(authParseRequest{RawJSON: first, FileName: "web-accounts-kimi-fixed.json"})
	if err != nil {
		t.Fatal(err)
	}
	secondAuth, err := parseAuth(authParseRequest{RawJSON: second, FileName: "web-accounts-kimi-fixed.json"})
	if err != nil {
		t.Fatal(err)
	}
	otherAuth, err := parseAuth(authParseRequest{RawJSON: second, FileName: "web-accounts-kimi-other.json"})
	if err != nil {
		t.Fatal(err)
	}
	if firstAuth.Auth.ID != "web-accounts-kimi-fixed.json" || secondAuth.Auth.ID != firstAuth.Auth.ID {
		t.Fatalf("filename ID not stable: first=%q second=%q", firstAuth.Auth.ID, secondAuth.Auth.ID)
	}
	if otherAuth.Auth.ID == firstAuth.Auth.ID {
		t.Fatal("different filenames must produce different auth IDs")
	}
	withoutName, err := parseAuth(authParseRequest{RawJSON: first})
	if err != nil || withoutName.Auth.ID == "" || withoutName.Auth.FileName == "" {
		t.Fatalf("filename-less fallback invalid: %#v, %v", withoutName, err)
	}
}

func TestParseAuthRejectsWrongTypeQwenAndCRLF(t *testing.T) {
	wrongType, _ := json.Marshal(map[string]any{"type": "other", "web_provider": "claude", "cookie": "sessionKey=x"})
	resp, err := parseAuth(authParseRequest{RawJSON: wrongType})
	if err != nil || resp.Handled {
		t.Fatalf("wrong type = %#v, %v", resp, err)
	}
	qwen, _ := json.Marshal(map[string]any{"type": providerID, "web_provider": "qwen", "cookie": "x"})
	if _, err := parseAuth(authParseRequest{RawJSON: qwen}); err == nil {
		t.Fatal("Qwen Web should remain unavailable")
	}
	crlf, _ := json.Marshal(map[string]any{"type": providerID, "web_provider": "kimi", "access_token": "bad\r\nvalue"})
	if _, err := parseAuth(authParseRequest{RawJSON: crlf}); err == nil {
		t.Fatal("CRLF credential should be rejected")
	}
}

func TestParseAuthPreservesHostRoutingFields(t *testing.T) {
	raw := mustJSON(map[string]any{
		"type": providerID, "web_provider": "kimi", "access_token": "synthetic-token",
		"prefix": "team", "proxy_url": "http://proxy.invalid", "disabled": true,
	})
	resp, err := parseAuth(authParseRequest{RawJSON: raw})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Auth.Prefix != "team" || resp.Auth.ProxyURL != "http://proxy.invalid" || !resp.Auth.Disabled {
		t.Fatalf("routing fields lost: %#v", resp.Auth)
	}
	crlf := mustJSON(map[string]any{"type": providerID, "web_provider": "kimi", "access_token": "\r\nsynthetic"})
	if _, err := parseAuth(authParseRequest{RawJSON: crlf}); err == nil {
		t.Fatal("CR/LF at credential boundary must not be trimmed away")
	}
}

func TestUnsupportedChatParametersAreRejected(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"model": "web-kimi/k3", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "temperature": 0.2})
	_, err := execute(context.Background(), executorRequest{AuthProvider: providerID, Model: "web-kimi/k3", Payload: payload, StorageJSON: []byte(`{"web_provider":"kimi","access_token":"synthetic"}`)}, false)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported parameter error = %v", err)
	}
}

func TestQoderCliUsesIsolatedEnvAndNoTools(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "qodercli")
	script := "#!/bin/sh\nif [ \"$6\" != \"--tools\" ] || [ -n \"$7\" ]; then exit 7; fi\nprintf '{\\\"result\\\":\\\"%s|%s\\\"}' \"$QODER_PERSONAL_ACCESS_TOKEN\" \"$UNRELATED_ENV\"\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("UNRELATED_ENV", "must-not-inherit")
	body := chatRequest{Messages: []chatMessage{{Role: "user", Content: "hello"}}}
	text, err := runQoder(context.Background(), "synthetic-pat", body, "qwen3.8-max-preview")
	if err != nil {
		t.Fatal(err)
	}
	if text != "synthetic-pat|" {
		t.Fatalf("Qoder result = %q", text)
	}
}

func TestQoderCliFallbackName(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "qoder")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got, err := findQoderCommand()
	if err != nil || got != command {
		t.Fatalf("Qoder fallback = %q, %v", got, err)
	}
}

func TestQoderStreamUsesOpenAIChunksAndPublicModel(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "qodercli")
	script := "#!/bin/sh\nprintf '{\"result\":\"hello\"}'\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	payload, _ := json.Marshal(map[string]any{"model": "web-qoder/qwen3.8-max-preview", "stream": true, "messages": []any{map[string]string{"role": "user", "content": "hello"}}})
	raw, err := handleMethod(methodExecStream, mustJSON(executorRequest{
		AuthProvider: providerID,
		Model:        "web-qoder/qwen3.8-max-preview",
		Format:       "chat-completions",
		Payload:      payload,
		StorageJSON:  []byte(`{"web_provider":"qoder","personal_access_token":"synthetic"}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var envelope envelope
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		t.Fatalf("stream envelope = %s, %v", raw, err)
	}
	var response executorStreamResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Chunks) != 4 {
		t.Fatalf("Qoder stream chunks = %d, want 4", len(response.Chunks))
	}
	var id string
	for i, chunk := range response.Chunks[:3] {
		var item struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Model   string `json:"model"`
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(bytes.TrimPrefix(chunk.Payload, []byte("data:"))), &item); err != nil {
			t.Fatal(err)
		}
		if item.Object != "chat.completion.chunk" || item.Model != "web-qoder/qwen3.8-max-preview" || len(item.Choices) != 1 {
			t.Fatalf("chunk %d = %s", i, chunk.Payload)
		}
		if id == "" {
			id = item.ID
		} else if item.ID != id {
			t.Fatal("Qoder stream completion ID changed")
		}
	}
	if string(response.Chunks[3].Payload) != "data: [DONE]\n\n" {
		t.Fatalf("Qoder terminal chunk = %q", response.Chunks[3].Payload)
	}
}

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
