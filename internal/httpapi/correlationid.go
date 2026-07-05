package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"

	breengine "github.com/helmedeiros/bre-go/engine"
)

// CorrelationIDHeader is echoed on both request (caller-supplied) and
// response so cross-service tracing sees the same ID that the JSON
// event body carries.
const CorrelationIDHeader = "X-Correlation-ID"

var randRead = rand.Read

// WithCorrelationID mirrors the markup-svc middleware: honour a supplied
// header, otherwise mint a UUID v4 from crypto/rand, always echo on the
// response, and inject the ID onto the request context so downstream
// handlers can carry it into emitted funnel events.
func WithCorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationIDHeader)
		if id == "" {
			generated, err := generateUUID()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
			id = generated
		}
		w.Header().Set(CorrelationIDHeader, id)
		ctx := breengine.WithCorrelationID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func generateUUID() (string, error) {
	var b [16]byte
	if _, err := randRead(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16])), nil
}
