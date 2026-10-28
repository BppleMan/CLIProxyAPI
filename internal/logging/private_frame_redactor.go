package logging

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// PrivateFrameRedactor assembles streaming lines before credential redaction.
// Oversized or malformed structured frames are omitted rather than written raw.
type PrivateFrameRedactor struct {
	pending    []byte
	discarding bool
}

const privateFrameLimit = 32 * 1024 * 1024

func privateFramePayload(frame []byte) ([]byte, int) {
	start := 0
	for start < len(frame) && (frame[start] == ' ' || frame[start] == '\t') {
		start++
	}
	if bytes.HasPrefix(frame[start:], []byte("data:")) {
		start += len("data:")
		for start < len(frame) && (frame[start] == ' ' || frame[start] == '\t') {
			start++
		}
	}
	return bytes.TrimSpace(frame[start:]), start
}

func privateStructuredFrame(payload []byte) bool {
	return len(payload) > 0 && (payload[0] == '{' || payload[0] == '[') && !bytes.Equal(payload, []byte("[DONE]"))
}

func RedactPrivateFrame(frame []byte) []byte {
	payload, start := privateFramePayload(frame)
	if !privateStructuredFrame(payload) {
		return bytes.Clone(frame)
	}
	if !gjson.ValidBytes(payload) {
		ReportPrivateCaptureFailure()
		return []byte("[structured log frame omitted: invalid JSON]\n")
	}
	redacted := RedactCredentialFields(payload)
	result := append(bytes.Clone(frame[:start]), redacted...)
	end := len(frame)
	for end > start && (frame[end-1] == ' ' || frame[end-1] == '\t' || frame[end-1] == '\r' || frame[end-1] == '\n') {
		end--
	}
	result = append(result, frame[end:]...)
	return result
}

func (r *PrivateFrameRedactor) Feed(chunk []byte, emit func([]byte)) {
	// Some executors pass one SSE line per call without its newline. Preserve
	// that framing when the next call starts a new event or data field.
	trimmed := bytes.TrimLeft(chunk, " \t")
	if len(r.pending) > 0 && (bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:"))) {
		emit(RedactPrivateFrame(r.pending))
		r.pending = nil
	}
	for len(chunk) > 0 {
		index := bytes.IndexByte(chunk, '\n')
		length := len(chunk)
		if index >= 0 {
			length = index + 1
		}
		part := chunk[:length]
		chunk = chunk[length:]
		if !r.discarding {
			if len(r.pending)+len(part) > privateFrameLimit {
				r.pending = nil
				r.discarding = true
				ReportPrivateCaptureFailure()
			} else {
				r.pending = append(r.pending, part...)
			}
		}
		if index >= 0 {
			if r.discarding {
				emit([]byte("[log frame omitted: size limit exceeded]\n"))
			} else {
				emit(RedactPrivateFrame(r.pending))
			}
			r.pending = nil
			r.discarding = false
		}
	}
	if !r.discarding && len(r.pending) > 0 {
		payload, _ := privateFramePayload(r.pending)
		if privateStructuredFrame(payload) && gjson.ValidBytes(payload) {
			emit(RedactPrivateFrame(r.pending))
			r.pending = nil
		}
	}
}

func (r *PrivateFrameRedactor) Finish(emit func([]byte)) {
	if r.discarding {
		emit([]byte("[log frame omitted: size limit exceeded]\n"))
	} else if len(r.pending) > 0 {
		emit(RedactPrivateFrame(r.pending))
	}
	r.pending = nil
	r.discarding = false
}
