package network

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ftechmax/krun/internal/contracts"
)

type sseWriter struct {
	ctx     context.Context
	writer  http.ResponseWriter
	flusher http.Flusher
}

func (s *sseWriter) Write(payload []byte) (int, error) {
	chunks := strings.SplitAfter(string(payload), "\n")
	if len(chunks) > 0 && chunks[len(chunks)-1] == "" {
		chunks = chunks[:len(chunks)-1]
	}

	for _, chunk := range chunks {
		if err := s.emit("log", contracts.StreamLogEvent{
			Stream: "stdout",
			Text:   chunk,
		}); err != nil {
			return 0, err
		}
	}

	return len(payload), nil
}

func (s *sseWriter) emit(event string, payload any) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal sse payload: %w", err)
	}

	if _, err := fmt.Fprintf(s.writer, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func StreamSSE(w http.ResponseWriter, r *http.Request, run func(ctx context.Context, out io.Writer) error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteJSON(w, http.StatusInternalServerError, contracts.HelperResponse{
			Success: false,
			Message: "streaming not supported",
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	writer := &sseWriter{
		ctx:     r.Context(),
		writer:  w,
		flusher: flusher,
	}

	runErr := run(r.Context(), writer)
	done := contracts.StreamDoneEvent{Ok: runErr == nil}
	if runErr != nil {
		done.Error = runErr.Error()
	}
	if emitErr := writer.emit("done", done); emitErr != nil {
		fmt.Printf("failed to emit done event: %v\n", emitErr)
	}
}

func WriteJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		fmt.Printf("warning: failed to write helper response: %v\n", err)
	}
}
