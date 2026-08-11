package core

// AWS event-stream (vnd.amazon.eventstream) decoding, for Bedrock's
// InvokeModelWithResponseStream endpoint.
//
// The Messages-API endpoint streams SSE, which the existing reader handles.
// This one frames the same events in AWS's binary encoding, so the choice was
// either decode it or ship without token streaming. Decoding it is about a
// hundred lines and reuses everything downstream: each frame's payload is
// base64 inside a tiny JSON envelope, and the decoded bytes are the ordinary
// Anthropic stream event that anthStreamState already knows how to apply.
//
// Frame layout, all integers big-endian:
//
//	 0..3   total length, including these 4 bytes and the trailing CRC
//	 4..7   headers length
//	 8..11  prelude CRC32 (over bytes 0..7)
//	12..    headers, then payload
//	last 4  message CRC32 (over everything before it)
//
// Header: 1-byte name length, name, 1-byte value type, then a value whose
// width depends on the type. Only the string type (7) carries anything this
// decoder cares about (":message-type", ":event-type", ":exception-type"), but
// every type has to be walked correctly or the parse desynchronizes.
//
// CRCs are parsed but not verified. TCP and TLS already cover corruption, and
// a checksum mismatch here would be indistinguishable from a decoder bug while
// giving the operator no way to act on it.

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	// eventStreamPreludeLen is the fixed prelude: total length, headers
	// length, prelude CRC.
	eventStreamPreludeLen = 12
	// eventStreamOverhead is the prelude plus the trailing message CRC — the
	// bytes that are never headers or payload.
	eventStreamOverhead = eventStreamPreludeLen + 4
	// eventStreamMaxFrame bounds a single frame. AWS caps messages well below
	// this; the limit exists so a corrupt length field cannot make us allocate
	// arbitrarily.
	eventStreamMaxFrame = 24 * 1024 * 1024
)

// eventStreamFrame is one decoded message.
type eventStreamFrame struct {
	Headers map[string]string
	Payload []byte
}

// messageType returns the ":message-type" header ("event", "exception"), or ""
// when absent.
func (f eventStreamFrame) messageType() string { return f.Headers[":message-type"] }

// eventStreamReader decodes frames from a response body.
type eventStreamReader struct {
	r io.Reader
}

func newEventStreamReader(r io.Reader) *eventStreamReader { return &eventStreamReader{r: r} }

// next reads one frame, returning io.EOF at a clean end of stream.
func (e *eventStreamReader) next() (*eventStreamFrame, error) {
	prelude := make([]byte, eventStreamPreludeLen)
	if _, err := io.ReadFull(e.r, prelude); err != nil {
		// A clean EOF on the prelude boundary is the normal end. An EOF partway
		// through is a truncated stream and must not read as success.
		if err == io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("event stream truncated in frame prelude")
		}
		return nil, err
	}
	total := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])

	if total < eventStreamOverhead || total > eventStreamMaxFrame {
		return nil, fmt.Errorf("event stream: implausible frame length %d", total)
	}
	if uint64(headersLen)+eventStreamOverhead > uint64(total) {
		return nil, fmt.Errorf("event stream: headers length %d exceeds frame %d", headersLen, total)
	}

	rest := make([]byte, total-eventStreamPreludeLen)
	if _, err := io.ReadFull(e.r, rest); err != nil {
		return nil, fmt.Errorf("event stream: short frame body: %w", err)
	}

	headers, err := parseEventStreamHeaders(rest[:headersLen])
	if err != nil {
		return nil, err
	}
	// Trailing 4 bytes are the message CRC, deliberately not verified.
	payload := rest[headersLen : len(rest)-4]

	return &eventStreamFrame{Headers: headers, Payload: payload}, nil
}

// parseEventStreamHeaders walks the header block. Every value type must be
// handled: skipping an unknown one by guessing its width desynchronizes the
// rest of the block and turns a readable stream into gibberish.
func parseEventStreamHeaders(b []byte) (map[string]string, error) {
	out := make(map[string]string)
	for i := 0; i < len(b); {
		nameLen := int(b[i])
		i++
		if i+nameLen > len(b) {
			return nil, fmt.Errorf("event stream: header name overruns block")
		}
		name := string(b[i : i+nameLen])
		i += nameLen
		if i >= len(b) {
			return nil, fmt.Errorf("event stream: header %q has no value type", name)
		}
		valueType := b[i]
		i++

		switch valueType {
		case 0, 1: // bool true / false, no value bytes
		case 2: // byte
			i++
		case 3: // int16
			i += 2
		case 4: // int32
			i += 4
		case 5: // int64
			i += 8
		case 6, 7: // byte array, string — both 2-byte length prefixed
			if i+2 > len(b) {
				return nil, fmt.Errorf("event stream: header %q truncated length", name)
			}
			n := int(binary.BigEndian.Uint16(b[i : i+2]))
			i += 2
			if i+n > len(b) {
				return nil, fmt.Errorf("event stream: header %q value overruns block", name)
			}
			if valueType == 7 {
				out[name] = string(b[i : i+n])
			}
			i += n
		case 8: // timestamp (int64 millis)
			i += 8
		case 9: // uuid
			i += 16
		default:
			return nil, fmt.Errorf("event stream: unknown header value type %d for %q", valueType, name)
		}
		if i > len(b) {
			return nil, fmt.Errorf("event stream: header %q overruns block", name)
		}
	}
	return out, nil
}

// bedrockChunkPayload is the envelope Bedrock wraps each event in: the actual
// Anthropic stream event, base64-encoded, under "bytes".
type bedrockChunkPayload struct {
	Bytes []byte `json:"bytes"` // encoding/json base64-decodes []byte for us
}

// decodeBedrockEvent extracts the Anthropic stream event JSON from a frame.
// Returns nil for frames that carry no event (keep-alives and anything else
// AWS adds later), so callers can skip rather than fail.
func decodeBedrockEvent(f *eventStreamFrame) ([]byte, error) {
	switch f.messageType() {
	case "exception", "error":
		// The exception type is the actionable part — the payload message is
		// often generic — so lead with it.
		kind := f.Headers[":exception-type"]
		if kind == "" {
			kind = f.Headers[":error-code"]
		}
		var body struct {
			Message string `json:"message"`
		}
		json.Unmarshal(f.Payload, &body)
		msg := body.Message
		if msg == "" {
			msg = string(f.Payload)
		}
		if kind != "" {
			return nil, fmt.Errorf("bedrock stream error (%s): %s", kind, msg)
		}
		return nil, fmt.Errorf("bedrock stream error: %s", msg)
	}
	if f.Headers[":event-type"] != "chunk" {
		return nil, nil
	}
	var chunk bedrockChunkPayload
	if err := json.Unmarshal(f.Payload, &chunk); err != nil {
		return nil, fmt.Errorf("bedrock stream: undecodable chunk payload: %w", err)
	}
	if len(chunk.Bytes) == 0 {
		return nil, nil
	}
	return chunk.Bytes, nil
}

// encodeEventStreamFrame builds a frame. Only tests construct frames — AWS
// never receives one — but having the encoder next to the decoder is what lets
// the tests exercise the real byte layout instead of a mock.
func encodeEventStreamFrame(headers map[string]string, payload []byte) []byte {
	var hb []byte
	for k, v := range headers {
		hb = append(hb, byte(len(k)))
		hb = append(hb, k...)
		hb = append(hb, 7) // string
		hb = binary.BigEndian.AppendUint16(hb, uint16(len(v)))
		hb = append(hb, v...)
	}
	total := uint32(eventStreamOverhead + len(hb) + len(payload))

	out := make([]byte, 0, total)
	out = binary.BigEndian.AppendUint32(out, total)
	out = binary.BigEndian.AppendUint32(out, uint32(len(hb)))
	out = binary.BigEndian.AppendUint32(out, 0) // prelude CRC, unverified
	out = append(out, hb...)
	out = append(out, payload...)
	out = binary.BigEndian.AppendUint32(out, 0) // message CRC, unverified
	return out
}

// encodeBedrockChunk wraps an Anthropic stream event the way Bedrock does.
func encodeBedrockChunk(eventJSON string) []byte {
	body, _ := json.Marshal(map[string]string{
		"bytes": base64.StdEncoding.EncodeToString([]byte(eventJSON)),
	})
	return encodeEventStreamFrame(map[string]string{
		":message-type": "event",
		":event-type":   "chunk",
		":content-type": "application/json",
	}, body)
}
