package api

import (
	"net/http"
	"os"
	"path/filepath"
)

// NewRouter wires all /api/ai/* routes and the SPA fallback.
func NewRouter(h *AIHandlers) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/ai/chat", withMethod(http.MethodPost, h.chat))
	mux.HandleFunc("/api/ai/chat/stream", withMethod(http.MethodPost, h.chatStream))
	mux.HandleFunc("/api/ai/memory", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.memoryList(w, r)
		case http.MethodPut:
			h.memoryImport(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/ai/memory/{key}", withMethod(http.MethodDelete, h.memoryDelete))
	mux.HandleFunc("/api/ai/logs", withMethod(http.MethodGet, h.logs))
	mux.Handle("/", spaHandler("./dist"))

	return corsMiddleware(mux)
}

func withMethod(method string, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		fn(w, r)
	}
}

// corsWriter forwards http.Flusher so SSE works through the CORS middleware wrapper.
type corsWriter struct {
	http.ResponseWriter
}

func (cw corsWriter) Flush() {
	if f, ok := cw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := corsWriter{w}
		cw.Header().Set("Access-Control-Allow-Origin", "*")
		cw.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		cw.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			cw.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(cw, r)
	})
}

func spaHandler(dir string) http.Handler {
	fs := http.Dir(dir)
	fileServer := http.FileServer(fs)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
