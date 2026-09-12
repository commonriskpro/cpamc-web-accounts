package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type semanticEvent struct {
	kind string
	text string
	stop string
}

type sseDecoder struct {
	buffer []byte
	data   []string
	state  string
	blocks map[int]string
	stop   string
	done   bool
}

func newSSEDecoder() *sseDecoder {
	return &sseDecoder{state: "awaiting", blocks: map[int]string{}, stop: "end_turn"}
}
func (d *sseDecoder) feed(chunk []byte) ([]semanticEvent, error) {
	if len(d.buffer)+len(chunk) > maxBodyBytes {
		return nil, errors.New("SSE stream is too large")
	}
	d.buffer = append(d.buffer, chunk...)
	var out []semanticEvent
	for {
		i := bytes.IndexByte(d.buffer, '\n')
		if i < 0 {
			if len(d.buffer) > 1<<20 {
				return nil, errors.New("SSE line is too large")
			}
			break
		}
		line := bytes.TrimSuffix(d.buffer[:i], []byte{'\r'})
		d.buffer = d.buffer[i+1:]
		if len(line) == 0 {
			if len(d.data) > 0 {
				event := strings.Join(d.data, "\n")
				d.data = nil
				ev, err := d.event(event)
				if err != nil {
					return nil, err
				}
				out = append(out, ev...)
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			value := bytes.TrimPrefix(line, []byte("data:"))
			value = bytes.TrimPrefix(value, []byte{' '})
			d.data = append(d.data, string(value))
			if len(strings.Join(d.data, "\n")) > 1<<20 {
				return nil, errors.New("SSE event is too large")
			}
		}
	}
	return out, nil
}
func (d *sseDecoder) finish() ([]semanticEvent, error) {
	if len(d.buffer) > 0 {
		d.buffer = append(d.buffer, '\n')
	}
	out, err := d.feed(nil)
	if err != nil {
		return nil, err
	}
	if len(d.data) > 0 {
		ev, e := d.event(strings.Join(d.data, "\n"))
		if e != nil {
			return nil, e
		}
		out = append(out, ev...)
	}
	if d.state != "stopped" {
		return nil, errors.New("SSE stream ended before message_stop")
	}
	return out, nil
}
func (d *sseDecoder) event(data string) ([]semanticEvent, error) {
	if data == "[DONE]" {
		return nil, errors.New("SSE stream ended before message_stop")
	}
	var obj map[string]any
	if json.Unmarshal([]byte(data), &obj) != nil {
		return nil, errors.New("SSE event contains malformed JSON")
	}
	typ, _ := obj["type"].(string)
	if typ == "" {
		return nil, errors.New("SSE event type is missing")
	}
	switch typ {
	case "ping", "completion", "message_limit", "content_block_retract", "model_fallback", "model_update", "compaction_status", "conversation_ready", "cache_performance", "tool_approval":
		return nil, nil
	case "message_start":
		if d.state != "awaiting" {
			return nil, errors.New("message_start is out of order")
		}
		d.state = "message"
		return []semanticEvent{{kind: "start"}}, nil
	case "content_block_start":
		if d.state != "message" {
			return nil, errors.New("content block is out of order")
		}
		index, ok := intValue(obj["index"])
		if !ok || d.blocks[index] != "" {
			return nil, errors.New("content block index is invalid")
		}
		block, ok := obj["content_block"].(map[string]any)
		if !ok {
			return nil, errors.New("content block is invalid")
		}
		kind, _ := block["type"].(string)
		if kind != "text" && kind != "thinking" {
			return nil, errors.New("unsupported Claude Web content block")
		}
		d.blocks[index] = kind
		if kind == "text" {
			if text, ok := block["text"].(string); ok && text != "" {
				return []semanticEvent{{kind: "text", text: text}}, nil
			}
		}
		if kind == "thinking" {
			if thinking, ok := block["thinking"].(string); ok && thinking != "" {
				return []semanticEvent{{kind: "reasoning", text: thinking}}, nil
			}
		}
		return nil, nil
	case "content_block_delta":
		if d.state != "message" {
			return nil, errors.New("content delta is out of order")
		}
		index, ok := intValue(obj["index"])
		if !ok {
			return nil, errors.New("content delta index is invalid")
		}
		kind := d.blocks[index]
		delta, ok := obj["delta"].(map[string]any)
		if !ok {
			return nil, errors.New("content delta is invalid")
		}
		typ, _ := delta["type"].(string)
		text, _ := delta["text"].(string)
		if typ == "signature_delta" {
			if kind != "thinking" {
				return nil, errors.New("Claude signature delta block mismatch")
			}
			// Signatures are upstream validation material, not user-visible text.
			return nil, nil
		}
		if typ == "thinking_delta" {
			text, _ = delta["thinking"].(string)
			if text == "" {
				text, _ = delta["text"].(string)
			}
			if kind != "thinking" {
				return nil, errors.New("Claude thinking delta block mismatch")
			}
			return []semanticEvent{{kind: "reasoning", text: text}}, nil
		}
		if typ != "text_delta" || kind != "text" {
			return nil, errors.New("unsupported Claude Web content delta")
		}
		return []semanticEvent{{kind: "text", text: text}}, nil
	case "content_block_stop":
		index, ok := intValue(obj["index"])
		if !ok || d.blocks[index] == "" {
			return nil, errors.New("content block stop has no open block")
		}
		delete(d.blocks, index)
		return nil, nil
	case "message_delta":
		if d.state != "message" || len(d.blocks) != 0 {
			return nil, errors.New("message delta is out of order")
		}
		delta, ok := obj["delta"].(map[string]any)
		if !ok {
			return nil, errors.New("message delta is invalid")
		}
		if stop, ok := delta["stop_reason"].(string); ok && stop != "" {
			d.stop = normalizeStopReason(stop)
		}
		return nil, nil
	case "message_stop":
		if d.state != "message" || len(d.blocks) != 0 {
			return nil, errors.New("message_stop is out of order")
		}
		d.state = "stopped"
		return []semanticEvent{{kind: "finish", stop: normalizeStopReason(d.stop)}}, nil
	case "error":
		return nil, errors.New("Claude Web upstream reported an error")
	default:
		return nil, fmt.Errorf("unknown Claude Web stream event %s", typ)
	}
}
func intValue(v any) (int, bool) {
	f, ok := v.(float64)
	return int(f), ok && f >= 0 && float64(int(f)) == f
}
func parseClaudeSSE(body []byte) (responseAccumulator, error) {
	d := newSSEDecoder()
	events, err := d.feed(body)
	if err != nil {
		return responseAccumulator{}, err
	}
	tail, err := d.finish()
	if err != nil {
		return responseAccumulator{}, err
	}
	events = append(events, tail...)
	var out responseAccumulator
	out.Stop = "stop"
	for _, ev := range events {
		if ev.kind == "text" {
			out.Text += ev.text
		}
		if ev.kind == "reasoning" {
			out.Reasoning += ev.text
		}
		if ev.kind == "finish" {
			out.Stop = ev.stop
		}
	}
	return out, nil
}

func normalizeStopReason(stop string) string {
	switch stop {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	default:
		if stop == "" {
			return "stop"
		}
		return stop
	}
}

type connectFrame struct {
	kind string
	text string
	end  bool
	err  string
}
type connectDecoder struct{ buffer []byte }

func frameConnectMessage(jsonBody []byte) []byte {
	frame := make([]byte, 5+len(jsonBody))
	frame[0] = 0
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(jsonBody)))
	copy(frame[5:], jsonBody)
	return frame
}
func (d *connectDecoder) feed(chunk []byte) ([]connectFrame, error) {
	if len(d.buffer)+len(chunk) > maxBodyBytes {
		return nil, errors.New("Kimi stream is too large")
	}
	d.buffer = append(d.buffer, chunk...)
	var out []connectFrame
	for {
		if len(d.buffer) < 5 {
			break
		}
		flags := d.buffer[0]
		size := binary.BigEndian.Uint32(d.buffer[1:5])
		if size > maxFrameBytes {
			return nil, errors.New("Kimi Connect frame is too large")
		}
		if flags&^byte(0x03) != 0 || flags&1 != 0 {
			return nil, errors.New("unsupported Kimi Connect frame")
		}
		if uint64(len(d.buffer)) < uint64(5)+uint64(size) {
			break
		}
		payload := append([]byte(nil), d.buffer[5:5+size]...)
		d.buffer = d.buffer[5+size:]
		if flags&2 != 0 {
			var msg map[string]any
			if len(payload) > 0 && json.Unmarshal(payload, &msg) != nil {
				return nil, errors.New("Kimi EndStream frame is invalid")
			}
			streamErr, err := endStreamError(msg)
			if err != nil {
				return nil, err
			}
			out = append(out, connectFrame{end: true, err: streamErr})
			continue
		}
		if len(payload) == 0 {
			continue
		}
		var msg map[string]any
		if json.Unmarshal(payload, &msg) != nil {
			return nil, errors.New("Kimi Connect frame contains invalid JSON")
		}
		kind, text := kimiDelta(msg)
		if kind != "" && text != "" {
			out = append(out, connectFrame{kind: kind, text: text})
		}
	}
	return out, nil
}
func (d *connectDecoder) finish() ([]connectFrame, error) {
	if len(d.buffer) != 0 {
		return nil, errors.New("Kimi stream ended with truncated frame")
	}
	return nil, errors.New("Kimi stream ended without EndStream frame")
}
func endStreamError(msg map[string]any) (string, error) {
	e, hasError := msg["error"]
	if !hasError || e == nil {
		return "", nil
	}
	errorObject, ok := e.(map[string]any)
	if !ok {
		return "", errors.New("Kimi EndStream error is invalid")
	}
	code, _ := errorObject["code"].(string)
	if strings.TrimSpace(code) == "" {
		return "", errors.New("Kimi EndStream error code is missing")
	}
	return "upstream_error", nil
}
func kimiDelta(msg map[string]any) (string, string) {
	op, _ := msg["op"].(string)
	mask, _ := msg["mask"].(string)
	block, _ := msg["block"].(map[string]any)
	var key string
	switch {
	case op == "append" && mask == "block.text.content":
		key = "text"
	case op == "append" && mask == "block.think.content":
		key = "think"
	case op == "set" && mask == "block.text":
		key = "text"
	case op == "set" && mask == "block.think":
		key = "think"
	default:
		return "", ""
	}
	nested, _ := block[key].(map[string]any)
	text, _ := nested["content"].(string)
	return key, text
}
func parseKimiStream(body []byte) (responseAccumulator, error) {
	d := &connectDecoder{}
	frames, err := d.feed(body)
	if err != nil {
		return responseAccumulator{}, err
	}
	var out responseAccumulator
	for _, frame := range frames {
		if frame.end {
			if frame.err != "" {
				return responseAccumulator{}, errors.New("Kimi Web upstream reported an error")
			}
			out.Stop = "stop"
			return out, nil
		}
		if frame.kind == "text" {
			out.Text += frame.text
		}
		if frame.kind == "think" {
			out.Reasoning += frame.text
		}
	}
	return responseAccumulator{}, errors.New("Kimi stream ended without EndStream frame")
}
