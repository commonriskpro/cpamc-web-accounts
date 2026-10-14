package main

/*
#cgo linux LDFLAGS: -ldl
#cgo linux CFLAGS: -D_GNU_SOURCE
#cgo windows LDFLAGS: -lkernel32
#if defined(__unix__) || defined(__APPLE__)
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#endif
#include <stdint.h>
#include <stdlib.h>

#if defined(__unix__) || defined(__APPLE__)
#include <dlfcn.h>
#include <pthread.h>
#elif defined(_WIN32) || defined(__CYGWIN__)
#include <windows.h>
#endif

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct {
  uint32_t abi_version;
  void* host_ctx;
  cliproxy_host_call_fn call;
  cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {
  uint32_t abi_version;
  cliproxy_plugin_call_fn call;
  cliproxy_plugin_free_fn free_buffer;
  cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

static const cliproxy_host_api* stored_host;
static void store_host(const cliproxy_host_api* host) { stored_host = host; }
static int call_host(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
  if (stored_host == NULL || stored_host->call == NULL) return 1;
  return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}
static void free_host(void* ptr, size_t len) {
  if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) stored_host->free_buffer(ptr, len);
}

// Keep one extra reference to this image on Unix. The host may dlclose its
// handle after shutdown; retaining this reference keeps Go callbacks and the
// Go runtime mapped until process exit. The reference is intentionally never
// released and is acquired at most once per process.
// On Windows, GetModuleHandleExW with GET_MODULE_HANDLE_EX_FLAG_PIN pins the DLL
// to prevent FreeLibrary from unmapping the Go runtime (Go issue #11100).
extern int cliproxy_plugin_init(cliproxy_host_api*, cliproxy_plugin_api*);
#if defined(__unix__) || defined(__APPLE__)
static pthread_mutex_t plugin_image_pin_mu = PTHREAD_MUTEX_INITIALIZER;
static void* plugin_image_pin;
static int pin_plugin_image(void) {
  int result = 0;
  pthread_mutex_lock(&plugin_image_pin_mu);
  if (plugin_image_pin == NULL) {
    Dl_info info;
    if (dladdr((void*)(uintptr_t)&cliproxy_plugin_init, &info) == 0 || info.dli_fname == NULL) {
      result = 1;
    } else {
      plugin_image_pin = dlopen(info.dli_fname, RTLD_NOW | RTLD_LOCAL);
      if (plugin_image_pin == NULL) result = 1;
    }
  }
  pthread_mutex_unlock(&plugin_image_pin_mu);
  return result;
}
#elif defined(_WIN32) || defined(__CYGWIN__)
static int pin_plugin_image(void) {
  HMODULE hModule = NULL;
  BOOL ok = GetModuleHandleExW(
    GET_MODULE_HANDLE_EX_FLAG_PIN | GET_MODULE_HANDLE_EX_FLAG_FROM_ADDRESS,
    (LPCWSTR)(uintptr_t)&cliproxy_plugin_init,
    &hModule
  );
  return ok ? 0 : 1;
}
#else
static int pin_plugin_image(void) { return 0; }
#endif

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

const (
	abiVersion    = 1
	schemaVersion = 6
	providerID    = "web-accounts"
	maxBodyBytes  = 16 << 20
	maxFrameBytes = 8 << 20
	maxRPCBytes   = 32 << 20
	upstreamRepo  = "https://github.com/diegosouzapw/OmniRoute/tree/152d95108c9c3d557562311ffed63240a511eb31/open-sse/executors"
)

const (
	methodRegister           = "plugin.register"
	methodReconfigure        = "plugin.reconfigure"
	methodPluginQuiesce      = "plugin.quiesce"
	methodManagementRegister = "management.register"
	methodManagementHandle   = "management.handle"
	methodModelStatic        = "model.static"
	methodModelForAuth       = "model.for_auth"
	methodAuthID             = "auth.identifier"
	methodAuthParse          = "auth.parse"
	methodAuthRefresh        = "auth.refresh"
	methodExecID             = "executor.identifier"
	methodExec               = "executor.execute"
	methodExecStream         = "executor.execute_stream"
	methodHostHTTP           = "host.http.do"
	methodHostStream         = "host.http.do_stream"
	methodHostRead           = "host.http.stream_read"
	methodHostClose          = "host.http.stream_close"
	methodStreamEmit         = "host.stream.emit"
	methodStreamClose        = "host.stream.close"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	RetryAfter string `json:"retry_after,omitempty"`
}

type httpStatusError struct {
	message    string
	statusCode int
	retryAfter string
}

func (e httpStatusError) Error() string      { return e.message }
func (e httpStatusError) StatusCode() int    { return e.statusCode }
func (e httpStatusError) RetryAfter() string { return e.retryAfter }

type registration struct {
	SchemaVersion uint32       `json:"schema_version"`
	Metadata      metadata     `json:"metadata"`
	Capabilities  capabilities `json:"capabilities"`
}
type metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo"`
	ConfigFields     []configField `json:"ConfigFields,omitempty"`
}
type configField struct {
	Name        string   `json:"Name"`
	Type        string   `json:"Type"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"Description"`
}
type capabilities struct {
	ManagementAPI      bool     `json:"management_api"`
	ModelProvider      bool     `json:"model_provider"`
	AuthProvider       bool     `json:"auth_provider"`
	Executor           bool     `json:"executor"`
	ExecutorModelScope string   `json:"executor_model_scope"`
	ExecutorInput      []string `json:"executor_input_formats"`
	ExecutorOutput     []string `json:"executor_output_formats"`
}

type authParseRequest struct {
	Provider string `json:"Provider"`
	Path     string `json:"Path"`
	FileName string `json:"FileName"`
	RawJSON  []byte `json:"RawJSON"`
}
type authData struct {
	Provider    string            `json:"Provider"`
	ID          string            `json:"ID"`
	FileName    string            `json:"FileName"`
	Label       string            `json:"Label"`
	Prefix      string            `json:"Prefix,omitempty"`
	ProxyURL    string            `json:"ProxyURL,omitempty"`
	Disabled    bool              `json:"Disabled,omitempty"`
	StorageJSON []byte            `json:"StorageJSON"`
	Metadata    map[string]any    `json:"Metadata,omitempty"`
	Attributes  map[string]string `json:"Attributes,omitempty"`
}
type authParseResponse struct {
	Handled bool     `json:"Handled"`
	Auth    authData `json:"Auth,omitempty"`
}
type authRefreshRequest struct {
	AuthID       string            `json:"AuthID"`
	AuthProvider string            `json:"AuthProvider"`
	StorageJSON  []byte            `json:"StorageJSON"`
	Metadata     map[string]any    `json:"Metadata"`
	Attributes   map[string]string `json:"Attributes"`
	Prefix       string            `json:"Prefix,omitempty"`
	ProxyURL     string            `json:"ProxyURL,omitempty"`
	Disabled     bool              `json:"Disabled,omitempty"`
}
type authRefreshResponse struct {
	Auth authData `json:"Auth"`
}

type authModelRequest struct {
	AuthID       string            `json:"AuthID"`
	AuthProvider string            `json:"AuthProvider"`
	StorageJSON  []byte            `json:"StorageJSON"`
	Metadata     map[string]any    `json:"Metadata"`
	Attributes   map[string]string `json:"Attributes"`
}
type modelResponse struct {
	Provider string      `json:"Provider"`
	Models   []modelInfo `json:"Models"`
}
type modelInfo struct {
	ID                        string           `json:"ID"`
	Object                    string           `json:"Object"`
	Created                   int64            `json:"Created,omitempty"`
	OwnedBy                   string           `json:"OwnedBy"`
	Type                      string           `json:"Type,omitempty"`
	DisplayName               string           `json:"DisplayName"`
	Name                      string           `json:"Name"`
	Description               string           `json:"Description,omitempty"`
	ContextLength             int64            `json:"ContextLength,omitempty"`
	MaxCompletionTokens       int64            `json:"MaxCompletionTokens,omitempty"`
	SupportedParameters       []string         `json:"SupportedParameters,omitempty"`
	SupportedInputModalities  []string         `json:"SupportedInputModalities,omitempty"`
	SupportedOutputModalities []string         `json:"SupportedOutputModalities,omitempty"`
	Thinking                  *thinkingSupport `json:"Thinking,omitempty"`
}
type thinkingSupport struct {
	Min            int  `json:"Min"`
	Max            int  `json:"Max"`
	ZeroAllowed    bool `json:"ZeroAllowed"`
	DynamicAllowed bool `json:"DynamicAllowed"`
}

type executorRequest struct {
	AuthID         string              `json:"AuthID"`
	AuthProvider   string              `json:"AuthProvider"`
	Model          string              `json:"Model"`
	Format         string              `json:"Format"`
	Stream         bool                `json:"Stream"`
	Headers        map[string][]string `json:"Headers"`
	Payload        []byte              `json:"Payload"`
	StorageJSON    []byte              `json:"StorageJSON"`
	AuthMetadata   map[string]any      `json:"AuthMetadata"`
	AuthAttributes map[string]string   `json:"AuthAttributes"`
	StreamID       string              `json:"stream_id,omitempty"`
	HostCallbackID string              `json:"host_callback_id,omitempty"`
}
type executorResponse struct {
	Payload  []byte              `json:"Payload"`
	Headers  map[string][]string `json:"Headers,omitempty"`
	Metadata map[string]any      `json:"Metadata,omitempty"`
}
type executorStreamResponse struct {
	Headers map[string][]string `json:"Headers,omitempty"`
	Chunks  []streamChunk       `json:"Chunks,omitempty"`
}
type streamChunk struct {
	Payload []byte `json:"Payload,omitempty"`
	Error   string `json:"Error,omitempty"`
}

type httpRequest struct {
	Method         string              `json:"Method"`
	URL            string              `json:"URL"`
	Headers        map[string][]string `json:"Headers,omitempty"`
	Body           []byte              `json:"Body,omitempty"`
	HostCallbackID string              `json:"host_callback_id,omitempty"`
}
type httpResponse struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers,omitempty"`
	Body       []byte              `json:"Body,omitempty"`
}
type httpStreamResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers,omitempty"`
	StreamID   string              `json:"stream_id"`
}
type httpStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}
type httpStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}
type streamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}
type streamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	// Pin before validating the host table: a loader may dlclose the image
	// after any failed init, including an ABI mismatch.
	if C.pin_plugin_image() != 0 {
		return 1
	}
	if host == nil || plugin == nil || uint32(host.abi_version) != abiVersion || host.call == nil || host.free_buffer == nil {
		return 1
	}
	if !beginPluginInit() {
		return 1
	}
	resetPluginLifecycle()
	C.store_host(host)
	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	methodName := C.GoString(method)
	if !beginPluginCall(methodName) {
		writeResponse(response, errorEnvelope("plugin_quiesced", "plugin is quiescing"))
		return 1
	}
	defer endPluginCall()
	if uint64(requestLen) > uint64(maxRPCBytes) {
		writeResponse(response, errorEnvelope("request_too_large", "plugin request is too large"))
		return 1
	}
	var raw []byte
	if request != nil && requestLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	result, err := handleMethod(methodName, raw)
	if err != nil {
		writeResponse(response, errorEnvelopeFor(err))
		return 1
	}
	if !writeResponse(response, result) {
		return 1
	}
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	if !beginPluginShutdown() {
		return
	}
	shutdownPlugin()
	C.store_host(nil)
	finishPluginShutdown()
}

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case methodRegister, methodReconfigure:
		if method == methodReconfigure && !reopenPluginLifecycle() {
			return nil, errors.New("plugin is shutting down")
		}
		return okEnvelope(registration{SchemaVersion: schemaVersion, Metadata: metadata{
			Name: "web-accounts", Version: "0.1.2", Author: "Saturno",
			GitHubRepository: upstreamRepo,
		}, Capabilities: capabilities{ManagementAPI: true, ModelProvider: true, AuthProvider: true, Executor: true, ExecutorModelScope: "oauth", ExecutorInput: []string{"chat-completions"}, ExecutorOutput: []string{"chat-completions"}}})
	case methodPluginQuiesce:
		quiescePlugin()
		return okEnvelope(map[string]bool{"quiesced": true})
	case methodManagementRegister:
		return okEnvelope(resourceRegistration{Resources: []resourceRoute{{Path: accountsResourcePath, Menu: "Cuentas web", Description: "Conecta Claude, Kimi y Qoder con la extensión de Chrome"}}})
	case methodManagementHandle:
		var req resourceRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, invalidRequest()
		}
		return okEnvelope(serveAccountsResource(req))
	case methodModelStatic:
		return okEnvelope(modelResponse{Provider: providerID})
	case methodModelForAuth:
		var req authModelRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, invalidRequest()
		}
		return okEnvelope(modelResponse{Provider: providerID, Models: modelsForAuth(req.StorageJSON, req.Attributes)})
	case methodAuthID:
		return okEnvelope(map[string]string{"Identifier": providerID})
	case methodAuthParse:
		var req authParseRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, invalidRequest()
		}
		resp, err := parseAuth(req)
		if err != nil {
			return nil, err
		}
		return okEnvelope(resp)
	case methodAuthRefresh:
		var req authRefreshRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, invalidRequest()
		}
		if _, _, err := readCredential(req.StorageJSON); err != nil {
			return nil, err
		}
		updated := makeAuthData(req.StorageJSON, req.AuthID, "", req.Attributes)
		updated.Prefix, updated.ProxyURL, updated.Disabled = req.Prefix, req.ProxyURL, req.Disabled
		return okEnvelope(authRefreshResponse{Auth: updated})
	case methodExecID:
		return okEnvelope(map[string]string{"Identifier": providerID})
	case methodExec:
		var req executorRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, invalidRequest()
		}
		ctx := context.Background()
		var cancel context.CancelFunc
		if provider, _, errCredential := readCredential(req.StorageJSON); errCredential == nil && (provider == "qoder" || provider == "gemini") {
			timeout := qoderTimeout
			if provider == "gemini" {
				timeout = geminiTimeout
			}
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		resp, err := execute(ctx, req, false)
		if err != nil {
			return nil, err
		}
		return okEnvelope(resp)
	case methodExecStream:
		var req executorRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, invalidRequest()
		}
		provider, _, errCredential := readCredential(req.StorageJSON)
		if errCredential != nil {
			return nil, errCredential
		}
		if provider == "qoder" || provider == "gemini" {
			timeout := qoderTimeout
			if provider == "gemini" {
				timeout = geminiTimeout
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			resp, err := execute(ctx, req, false)
			if err != nil {
				return nil, err
			}
			publicModel := strings.TrimSpace(req.Model)
			if publicModel == "" {
				var body chatRequest
				if json.Unmarshal(req.Payload, &body) == nil {
					publicModel = body.Model
				}
			}
			var chunks []streamChunk
			if provider == "gemini" {
				chunks, err = geminiStreamChunks(resp.Payload, publicModel)
			} else {
				chunks, err = qoderStreamChunks(resp.Payload, publicModel)
			}
			if err != nil {
				return nil, err
			}
			return okEnvelope(executorStreamResponse{Headers: map[string][]string{"Content-Type": {"text/event-stream"}, "Cache-Control": {"no-cache"}}, Chunks: chunks})
		}
		if req.StreamID == "" {
			return nil, errors.New("stream bridge unavailable")
		}
		worker, ctx, ok := beginStreamWorker()
		if !ok {
			return nil, errors.New("plugin is quiescing")
		}
		go func() {
			defer endStreamWorker(worker)
			executeAsync(ctx, worker, req)
		}()
		return okEnvelope(executorStreamResponse{Headers: map[string][]string{"Content-Type": {"text/event-stream"}, "Cache-Control": {"no-cache"}}})
	default:
		return errorEnvelope("unknown_method", "unknown method"), nil
	}
}

func invalidRequest() error  { return errors.New("invalid plugin request") }
func safeError(err error) string {
	if err == nil {
		return "web account request failed"
	}
	return err.Error()
}
func errorEnvelopeFor(err error) []byte {
	message := safeError(err)
	status := 0
	retryAfter := ""
	if e, ok := err.(interface{ StatusCode() int }); ok {
		status = e.StatusCode()
	}
	if e, ok := err.(interface{ RetryAfter() string }); ok {
		retryAfter = e.RetryAfter()
	}
	return errorEnvelopeWithStatus("plugin_error", message, status, retryAfter)
}
func okEnvelope(value any) ([]byte, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: b})
}
func errorEnvelope(code, message string) []byte {
	return errorEnvelopeWithStatus(code, message, 0, "")
}
func errorEnvelopeWithStatus(code, message string, status int, retryAfter string) []byte {
	if status < 400 || status > 599 {
		status = 0
	}
	if len(retryAfter) > 128 || !noCRLF(retryAfter) {
		retryAfter = ""
	}
	b, _ := json.Marshal(envelope{OK: false, Error: &rpcError{Code: code, Message: message, HTTPStatus: status, RetryAfter: retryAfter}})
	return b
}
func writeResponse(response *C.cliproxy_buffer, raw []byte) bool {
	if response == nil || len(raw) == 0 {
		return false
	}
	if len(raw) > maxRPCBytes {
		return false
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return false
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
	return true
}

func callHost(method string, request any) ([]byte, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxRPCBytes {
		return nil, errors.New("host callback request is too large")
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var cReq *C.uint8_t
	if len(raw) > 0 {
		cReq = (*C.uint8_t)(C.CBytes(raw))
		defer C.free(unsafe.Pointer(cReq))
	}
	var cResp C.cliproxy_buffer
	rc := C.call_host(cMethod, cReq, C.size_t(len(raw)), &cResp)
	if rc != 0 || cResp.ptr == nil || uint64(cResp.len) > uint64(maxRPCBytes) {
		if cResp.ptr != nil {
			C.free_host(cResp.ptr, cResp.len)
		}
		return nil, errors.New("host callback failed")
	}
	resp := C.GoBytes(cResp.ptr, C.int(cResp.len))
	C.free_host(cResp.ptr, cResp.len)
	var env envelope
	if json.Unmarshal(resp, &env) != nil {
		return nil, errors.New("host callback rejected request")
	}
	if !env.OK {
		if env.Error != nil && env.Error.HTTPStatus >= 400 && env.Error.HTTPStatus <= 599 {
			return nil, httpStatusError{message: "host callback rejected request", statusCode: env.Error.HTTPStatus, retryAfter: env.Error.RetryAfter}
		}
		return nil, errors.New("host callback rejected request")
	}
	return env.Result, nil
}

func emitStream(id string, payload []byte) error {
	_, err := callHost(methodStreamEmit, streamEmitRequest{StreamID: id, Payload: payload})
	return err
}
func closeStream(id, message string) {
	_, _ = callHost(methodStreamClose, streamCloseRequest{StreamID: id, Error: message})
}

func doHTTP(req httpRequest) (httpResponse, error) {
	b, err := callHost(methodHostHTTP, req)
	if err != nil {
		return httpResponse{}, err
	}
	var resp httpResponse
	if json.Unmarshal(b, &resp) != nil {
		return httpResponse{}, errors.New("invalid host HTTP response")
	}
	return resp, nil
}
func openHTTPStream(req httpRequest) (httpStreamResponse, error) {
	b, err := callHost(methodHostStream, req)
	if err != nil {
		return httpStreamResponse{}, err
	}
	var resp httpStreamResponse
	if json.Unmarshal(b, &resp) != nil || resp.StreamID == "" {
		return httpStreamResponse{}, errors.New("invalid host HTTP stream response")
	}
	return resp, nil
}
func readHTTPStream(id string) (httpStreamReadResponse, error) {
	b, err := callHost(methodHostRead, httpStreamReadRequest{StreamID: id})
	if err != nil {
		return httpStreamReadResponse{}, err
	}
	var resp httpStreamReadResponse
	if json.Unmarshal(b, &resp) != nil {
		return httpStreamReadResponse{}, errors.New("invalid host HTTP stream chunk")
	}
	return resp, nil
}
func closeHTTPStream(id string) {
	_, _ = callHost(methodHostClose, httpStreamReadRequest{StreamID: id})
}

func randomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func hashID(provider, credential string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + credential))
	return providerID + "-" + provider + "-" + fmt.Sprintf("%x", sum[:8])
}
func noCRLF(value string) bool { return !strings.ContainsAny(value, "\r\n") }
func stringValue(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func rawStringValue(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func trimHorizontal(value string) string { return strings.Trim(value, " \t") }

func credentialValue(input map[string]any) (string, error) {
	keys := []string{"credential", "cookie", "cookies", "sessionKey", "access_token", "accessToken", "token", "kimi-auth", "personal_access_token", "qoder_pat", "apiKey"}
	var found string
	count := 0
	for _, key := range keys {
		value, ok := rawStringValue(input, key)
		if !ok && (key == "cookie" || key == "cookies") {
			if list, listOK := input[key].([]any); listOK {
				encoded, err := json.Marshal(list)
				if err != nil {
					return "", errors.New("web account credential format is invalid")
				}
				value, ok = string(encoded), true
			}
		}
		if !ok || trimHorizontal(value) == "" {
			continue
		}
		count++
		found = value
	}
	if count > 1 {
		return "", errors.New("web account credential fields are ambiguous")
	}
	return found, nil
}

type authOptions struct {
	prefix   string
	proxyURL string
	disabled bool
}

func authOptionsFromInput(input map[string]any) (authOptions, error) {
	var options authOptions
	read := func(keys ...string) (string, error) {
		var found string
		for _, key := range keys {
			raw, ok := rawStringValue(input, key)
			if !ok {
				continue
			}
			if !noCRLF(raw) {
				return "", errors.New("auth routing metadata contains invalid line breaks")
			}
			value := trimHorizontal(raw)
			if found != "" && found != value {
				return "", errors.New("auth routing metadata is ambiguous")
			}
			found = value
		}
		return found, nil
	}
	var err error
	if options.prefix, err = read("prefix", "Prefix"); err != nil {
		return authOptions{}, err
	}
	if options.proxyURL, err = read("proxy_url", "proxyURL", "ProxyURL"); err != nil {
		return authOptions{}, err
	}
	var disabledSet bool
	for _, key := range []string{"disabled", "Disabled"} {
		if raw, ok := input[key]; ok {
			value, valueOK := raw.(bool)
			if !valueOK {
				return authOptions{}, errors.New("auth disabled field is invalid")
			}
			if disabledSet && options.disabled != value {
				return authOptions{}, errors.New("auth disabled field is ambiguous")
			}
			options.disabled, disabledSet = value, true
		}
	}
	return options, nil
}

func parseAuth(req authParseRequest) (authParseResponse, error) {
	var input map[string]any
	if len(req.RawJSON) == 0 || json.Unmarshal(req.RawJSON, &input) != nil {
		return authParseResponse{}, errors.New("invalid web account JSON")
	}
	accountType := strings.ToLower(stringValue(input, "type"))
	if accountType != providerID {
		return authParseResponse{Handled: false}, nil
	}
	webProvider := strings.ToLower(stringValue(input, "web_provider", "webProvider"))
	if webProvider == "qwen" || webProvider == "qwen-web" {
		return authParseResponse{}, errors.New("Qwen Web is unavailable pending provenance review")
	}
	if webProvider != "claude" && webProvider != "kimi" && webProvider != "qoder" && webProvider != "gemini" {
		return authParseResponse{Handled: false}, nil
	}
	credential, err := credentialValue(input)
	if err != nil {
		return authParseResponse{}, err
	}
	if !noCRLF(credential) {
		return authParseResponse{}, errors.New("credential contains invalid line breaks")
	}
	if trimHorizontal(credential) == "" {
		return authParseResponse{}, errors.New("web account credential is required")
	}
	options, err := authOptionsFromInput(input)
	if err != nil {
		return authParseResponse{}, err
	}
	canonical, err := canonicalStorage(webProvider, credential, input)
	if err != nil {
		return authParseResponse{}, err
	}
	attrs := map[string]string{"web_provider": webProvider}
	name := strings.TrimSpace(req.FileName)
	id := hashID(webProvider, credential)
	if name != "" {
		// Auth-file uploads identify the record by its filename. Keeping that ID
		// stable lets credential replacement update the existing source instead
		// of creating a duplicate virtual auth entry.
		id = name
	}
	if name == "" {
		name = id + ".json"
	}
	auth := makeAuthData(canonical, id, name, attrs)
	auth.Prefix, auth.ProxyURL, auth.Disabled = options.prefix, options.proxyURL, options.disabled
	return authParseResponse{Handled: true, Auth: auth}, nil
}

func canonicalStorage(webProvider, credential string, input map[string]any) ([]byte, error) {
	data := map[string]any{"web_provider": webProvider}
	switch webProvider {
	case "claude":
		cookie, err := normalizeClaudeCookie(credential)
		if err != nil {
			return nil, err
		}
		data["cookie"] = cookie
		device, _ := rawStringValue(input, "device_id")
		if device == "" {
			device, _ = rawStringValue(input, "deviceId")
		}
		device = trimHorizontal(device)
		if device != "" {
			if !noCRLF(device) {
				return nil, errors.New("device id contains invalid line breaks")
			}
			data["device_id"] = device
		}
	case "kimi":
		token := extractKimiToken(credential)
		if token == "" || !noCRLF(token) {
			return nil, errors.New("Kimi access_token is required")
		}
		data["access_token"] = token
	case "qoder":
		if !noCRLF(credential) {
			return nil, errors.New("credential contains invalid line breaks")
		}
		data["personal_access_token"] = trimHorizontal(credential)
	case "gemini":
		if !noCRLF(credential) {
			return nil, errors.New("credential contains invalid line breaks")
		}
		data["cookie"] = trimHorizontal(credential)
	}
	return json.Marshal(data)
}

func makeAuthData(storage []byte, id, file string, attrs map[string]string) authData {
	if id == "" {
		id = hashIDFromStorage(storage)
	}
	if file == "" {
		file = id + ".json"
	}
	provider := ""
	var data map[string]any
	_ = json.Unmarshal(storage, &data)
	provider = stringValue(data, "web_provider")
	return authData{Provider: providerID, ID: id, FileName: file, Label: provider + " web account", StorageJSON: storage, Metadata: map[string]any{"web_provider": provider}, Attributes: attrs}
}
func hashIDFromStorage(storage []byte) string {
	sum := sha256.Sum256(storage)
	return providerID + "-" + fmt.Sprintf("%x", sum[:8])
}

func readCredential(storage []byte) (string, string, error) {
	var data map[string]any
	if len(storage) == 0 || json.Unmarshal(storage, &data) != nil {
		return "", "", errors.New("invalid web account storage")
	}
	p := strings.ToLower(stringValue(data, "web_provider"))
	if p != "claude" && p != "kimi" && p != "qoder" && p != "gemini" {
		return "", "", errors.New("unsupported web account provider")
	}
	if p == "qwen" {
		return "", "", errors.New("Qwen Web is unavailable pending provenance review")
	}
	c := stringValue(data, "cookie", "access_token", "personal_access_token")
	if c == "" || !noCRLF(c) {
		return "", "", errors.New("web account credential is invalid")
	}
	return p, c, nil
}

func modelsForAuth(storage []byte, attrs map[string]string) []modelInfo {
	p, _, err := readCredential(storage)
	if err != nil && attrs != nil {
		p = strings.ToLower(attrs["web_provider"])
	}
	switch p {
	case "claude":
		return []modelInfo{
			model("web-claude/claude-fable-5-1", "claude-fable-5-1", "Claude Fable 5.1 (web)", true),
			model("web-claude/claude-fable-5", "claude-fable-5", "Claude Fable 5 (web)", false),
			model("web-claude/claude-opus-5", "claude-opus-5", "Claude Opus 5 (web)", true),
			model("web-claude/claude-opus-4-8", "claude-opus-4-8", "Claude Opus 4.8 (web)", false),
			model("web-claude/claude-opus-4-7", "claude-opus-4-7", "Claude Opus 4.7 (web)", false),
			model("web-claude/claude-opus-4-6", "claude-opus-4-6", "Claude Opus 4.6 (web)", false),
			model("web-claude/claude-sonnet-5", "claude-sonnet-5", "Claude Sonnet 5 (web)", false),
			model("web-claude/claude-sonnet-4-6", "claude-sonnet-4-6", "Claude Sonnet 4.6 (web)", false),
			model("web-claude/claude-haiku-4-5-20251001", "claude-haiku-4-5-20251001", "Claude Haiku 4.5 (web)", false),
		}
	case "kimi":
		return []modelInfo{
			model("web-kimi/k3", "k3", "K3 (Kimi Web)", true),
			model("web-kimi/k2d6", "k2d6", "K2.6 (Kimi Web)", true),
		}
	case "qoder":
		return []modelInfo{model("web-qoder/qwen3.8-max-preview", "qwen3.8-max-preview", "Qwen3.8-Max-Preview (Qoder)", true)}
	case "gemini":
		return []modelInfo{
			model("web-gemini/gemini-4-argon", "gemini-4-argon", "Gemini 4 Argon (Web)", true),
			model("gemini-4-argon", "gemini-4-argon", "Gemini 4 Argon", true),
			model("web-gemini/gemini-2.5-pro", "gemini-2.5-pro", "Gemini 2.5 Pro (Web)", true),
			model("web-gemini/gemini-2.5-flash", "gemini-2.5-flash", "Gemini 2.5 Flash (Web)", true),
		}
	default:
		return nil
	}
}

func model(id, upstreamID, name string, reasoning bool) modelInfo {
	m := modelInfo{ID: id, Object: "model", OwnedBy: providerID, DisplayName: name, Name: upstreamID, ContextLength: 128000, MaxCompletionTokens: 16384, SupportedParameters: []string{"stream"}, SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}}
	if reasoning {
		m.Thinking = &thinkingSupport{Min: 0, Max: 32768, ZeroAllowed: true, DynamicAllowed: false}
	}
	return m
}

func normalizeClaudeCookie(raw string) (string, error) {
	if raw == "" || !noCRLF(raw) {
		return "", errors.New("Claude session cookie is invalid")
	}
	raw = trimHorizontal(strings.TrimPrefix(strings.TrimPrefix(raw, "Cookie:"), "cookie:"))
	if strings.HasPrefix(raw, "[") {
		var items []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if json.Unmarshal([]byte(raw), &items) != nil || len(items) == 0 {
			return "", errors.New("Claude cookie JSON is invalid")
		}
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if item.Name == "" || item.Value == "" || !noCRLF(item.Name) || !noCRLF(item.Value) {
				return "", errors.New("Claude cookie JSON is invalid")
			}
			parts = append(parts, item.Name+"="+item.Value)
		}
		raw = strings.Join(parts, "; ")
	}
	if !strings.Contains(raw, "=") {
		raw = "sessionKey=" + raw
	}
	found := false
	for _, part := range strings.Split(raw, ";") {
		if strings.HasPrefix(strings.TrimSpace(part), "sessionKey=") {
			found = true
		}
	}
	if !found {
		return "", errors.New("Claude sessionKey cookie is required")
	}
	return raw, nil
}
func extractKimiToken(raw string) string {
	if !noCRLF(raw) {
		return ""
	}
	raw = trimHorizontal(raw)
	if strings.HasPrefix(raw, "{") {
		var data map[string]any
		if json.Unmarshal([]byte(raw), &data) == nil {
			return trimHorizontal(stringValue(data, "access_token", "token", "kimi-auth"))
		}
	}
	for _, prefix := range []string{"Bearer ", "bearer ", "access_token=", "kimi-auth="} {
		if strings.HasPrefix(raw, prefix) {
			return trimHorizontal(strings.TrimPrefix(raw, prefix))
		}
	}
	if !strings.ContainsAny(raw, "=; ") {
		return raw
	}
	return ""
}
