package main

import (
	"encoding/json"
	"testing"
)

func TestClaudeSSEDecoderAcceptsFragmentedCompleteStream(t *testing.T) {
	body := []byte("data: {\"type\":\"message_start\"}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hola\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	for split := 1; split < len(body); split++ {
		decoder := newSSEDecoder()
		var events []semanticEvent
		for _, chunk := range [][]byte{body[:split], body[split:]} {
			got, err := decoder.feed(chunk)
			if err != nil {
				t.Fatalf("split %d: feed: %v", split, err)
			}
			events = append(events, got...)
		}
		tail, err := decoder.finish()
		if err != nil {
			t.Fatalf("split %d: finish: %v", split, err)
		}
		events = append(events, tail...)
		if len(events) != 3 || events[1].text != "hola" || events[2].kind != "finish" {
			t.Fatalf("split %d: events = %#v", split, events)
		}
	}
}

func TestClaudeSSEDecoderRejectsMalformedAndTruncatedStreams(t *testing.T) {
	for _, body := range [][]byte{
		[]byte("data: {not-json}\n\n"),
		[]byte("data: {\"type\":\"message_start\"}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n"),
		[]byte("data: {\"type\":\"message_start\"}\n\ndata: [DONE]\n\n"),
	} {
		if _, err := parseClaudeSSE(body); err == nil {
			t.Fatalf("expected Claude SSE rejection for %q", body)
		}
	}
}

func TestClaudeSSEDecoderIgnoresThinkingSignaturesAndNormalizesStop(t *testing.T) {
	body := []byte("data: {\"type\":\"message_start\"}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"initial\",\"signature\":\"opaque\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"opaque\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"more\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	acc, err := parseClaudeSSE(body)
	if err != nil || acc.Reasoning != "initialmore" || acc.Stop != "length" {
		t.Fatalf("Claude signature/stop handling = %#v, %v", acc, err)
	}
}

func TestKimiConnectDecoderAcceptsSplitFramesAndEndMarker(t *testing.T) {
	first, _ := json.Marshal(map[string]any{"op": "set", "mask": "block.text", "block": map[string]any{"text": map[string]string{"content": "hola"}}})
	end, _ := json.Marshal(map[string]any{})
	body := append(frameConnectMessage(first), frameConnectMessage(end)...)
	body[len(first)+5] |= 2 // EndStream flag on the second frame.
	decoder := &connectDecoder{}
	var frames []connectFrame
	for _, chunk := range [][]byte{body[:3], body[3 : len(first)+7], body[len(first)+7:]} {
		got, err := decoder.feed(chunk)
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, got...)
	}
	if len(frames) != 2 || frames[0].text != "hola" || !frames[1].end || frames[1].err != "" {
		t.Fatalf("frames = %#v", frames)
	}
	acc, err := parseKimiStream(body)
	if err != nil || acc.Text != "hola" {
		t.Fatalf("parseKimiStream = %#v, %v", acc, err)
	}
}

func TestKimiConnectDecoderRejectsTruncatedAndOversizedFrames(t *testing.T) {
	decoder := &connectDecoder{}
	if _, err := decoder.feed([]byte{0, 0, 0, 0, 4, '{'}); err != nil {
		t.Fatalf("feed should wait for payload: %v", err)
	}
	if _, err := decoder.finish(); err == nil {
		t.Fatal("expected truncated frame rejection")
	}
	over := []byte{0, 0x01, 0x00, 0x00, 0x01}
	// 16 MiB is over the protocol ceiling, independent of payload availability.
	over[1] = 0x01
	over[2], over[3], over[4] = 0x00, 0x00, 0x00
	// 0x01000000 == 16 MiB.
	over = []byte{0, 0x01, 0x00, 0x00, 0x00}
	if _, err := decoder.feed(over); err == nil {
		t.Fatal("expected oversized frame rejection")
	}
}

func TestKimiConnectDecoderRejectsEndStreamErrorWithoutCode(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"error": map[string]any{"message": "opaque"}})
	body := frameConnectMessage(payload)
	body[0] = 2
	if _, err := parseKimiStream(body); err == nil {
		t.Fatal("Kimi EndStream error without code must be rejected")
	}
}
