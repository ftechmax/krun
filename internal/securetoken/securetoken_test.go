package securetoken

import (
	"errors"
	"strings"
	"testing"
)

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestHexFrom(t *testing.T) {
	token, err := hexFrom(strings.NewReader("0123456789abcdef"), 8)
	if err != nil {
		t.Fatalf("hexFrom: %v", err)
	}
	if token != "3031323334353637" {
		t.Fatalf("token = %q", token)
	}
}

func TestHexFromPropagatesEntropyFailure(t *testing.T) {
	wantErr := errors.New("entropy unavailable")
	if _, err := hexFrom(failingReader{err: wantErr}, 8); !errors.Is(err, wantErr) {
		t.Fatalf("hexFrom error = %v, want %v", err, wantErr)
	}
}
