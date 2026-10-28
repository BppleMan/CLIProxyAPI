package logging

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func awaitPrivateTest(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not complete")
	}
}

func TestPrivateSourceAndStreamDoNotWaitForBlockedRecovery(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	gate := make(chan struct{})
	privateRecoveryMu.Lock()
	previous := privateRecoveryDone
	privateRecoveryDone = gate
	privateRecoveryMu.Unlock()
	defer func() {
		privateRecoveryMu.Lock()
		privateRecoveryDone = previous
		privateRecoveryMu.Unlock()
	}()
	dir := t.TempDir()
	logger := NewFileRequestLogger(true, dir, "", 0)
	completed := make(chan struct{})
	var source *FileBodySource
	var stream StreamingLogWriter
	go func() {
		var err error
		source, err = logger.NewFileBodySource("response")
		if err != nil {
			t.Error(err)
		}
		if err = source.AppendBytes([]byte("queued")); err != nil {
			t.Error(err)
		}
		stream, err = logger.LogStreamingRequest("/v1/chat", "POST", nil, []byte(`{"message":"ok"}`), "test")
		if err != nil {
			t.Error(err)
		}
		stream.WriteChunkAsync([]byte("data: hello\n\n"))
		close(completed)
	}()
	awaitPrivateTest(t, completed)
	close(gate)
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if _, err := source.WriteTo(&got); err != nil || got.String() != "queued" {
		t.Fatalf("buffered source lost data: %q %v", got.String(), err)
	}
	if err := source.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

type blockingLogSink struct {
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
}

func (b *blockingLogSink) Write(p []byte) (int, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return len(p), nil
}
func (b *blockingLogSink) Close() error { close(b.closed); return nil }

func TestPrivateApplicationLogDoesNotBlockAndReportsQueueDrops(t *testing.T) {
	sink := &blockingLogSink{entered: make(chan struct{}, 1), release: make(chan struct{}), closed: make(chan struct{})}
	writer := newPrivateAsyncLogWriter(sink, func() error { return nil })
	if writer == nil {
		t.Fatal("failed to start writer")
	}
	_, _ = writer.Write([]byte("first"))
	awaitPrivateTest(t, sink.entered)
	_, before, _ := PrivateCaptureStatus()
	for i := 0; i < 300; i++ {
		_, _ = io.WriteString(writer, "item")
	}
	_, after, _ := PrivateCaptureStatus()
	if after <= before {
		t.Fatal("queue saturation was not reported")
	}
	writer.stop()
	close(sink.release)
	awaitPrivateTest(t, sink.closed)
}

func TestPrivateFrameRedactionAcrossChunks(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	var redactor PrivateFrameRedactor
	var result bytes.Buffer
	emit := func(p []byte) { _, _ = result.Write(p) }
	redactor.Feed([]byte(`data: {"text":"hello","access_`), emit)
	redactor.Feed([]byte("token\":\"STREAM_SECRET\"}\n\n"), emit)
	redactor.Feed([]byte("event: update"), emit)
	redactor.Feed([]byte(`data: {"access_token":"EVENT_SECRET","text":"event-ok"}`), emit)
	redactor.Feed([]byte(`data: {"api_key":"BROKEN_SECRET"`), emit)
	redactor.Finish(emit)
	value := result.String()
	if strings.Contains(value, "STREAM_SECRET") || strings.Contains(value, "BROKEN_SECRET") || strings.Contains(value, "EVENT_SECRET") || !strings.Contains(value, "event-ok") || !strings.Contains(value, "hello") || !strings.Contains(value, "omitted") {
		t.Fatalf("unsafe streamed frame: %s", value)
	}
}

func TestPrivateFrameBudgetDropsSafely(t *testing.T) {
	if !reservePrivateFrameBytes(privateFrameTotalLimit) {
		t.Fatal("unexpected active frame reservation")
	}
	defer privateFrameBytes.Add(-privateFrameTotalLimit)
	var redactor PrivateFrameRedactor
	var result bytes.Buffer
	emit := func(p []byte) { _, _ = result.Write(p) }
	redactor.Feed([]byte(`data: {"api_key":"SECRET"}`), emit)
	redactor.Finish(emit)
	if strings.Contains(result.String(), "SECRET") || !strings.Contains(result.String(), "omitted") {
		t.Fatalf("budget exhaustion did not omit the frame safely: %s", result.String())
	}
}
