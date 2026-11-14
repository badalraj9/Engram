// Package panel — the local operator surface v0 (R0, armored-first).
//
// The envelope is the deliverable of this phase: the {test} page
// exists only to prove the serve path end-to-end, and it is born WITH
// the full envelope in the same commit — the door is bolted before
// any screen exists (BUILD-PLAN R0 item 4). Envelope, per owner spec:
//
//   - binds 127.0.0.1 ONLY, never 0.0.0.0; port from config with
//     auto-fallback to the next free port (actual URL reported)
//   - per-install token; the URL carries ?t=<token>; requests without
//     it are rejected (constant-time compare, fail-closed)
//   - NO CORS header at all (never set, never "*")
//   - Host validated against DNS rebinding (only localhost hosts)
//   - X-Content-Type-Options: nosniff and a strict CSP on every
//     response
//   - everything rendered passes html/template (auto-escaped): a
//     fact whose object is <img src=x onerror=…> renders as text
//
// v0 is read-only and static; the reader seam (CLI parity) lands with
// the screens phase.
package panel

import (
	"context"
	"crypto/subtle"
	"embed"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed assets/panel.html assets/style.css
var assets embed.FS

// maxPortTries bounds the fallback walk: configured port, then the
// next nine — never an unbounded port scan.
const maxPortTries = 10

// Server is the panel listener. Construct with Listen; the zero
// value serves nothing.
type Server struct {
	token string
	label string
	tmpl  *template.Template
	http  *http.Server
	ln    net.Listener
	port  int
}

// pageData is the single template slot — flows through
// html/template so any store-originated string renders as text.
type pageData struct {
	Label string
}

// Listen starts the panel on 127.0.0.1. port is the preferred port;
// the next free port wins when it is taken (actual port reported via
// URL/Port). token must be non-empty (fail-closed, re-checked here —
// config validation is the first gate, this is the second).
func Listen(port int, token, label string) (*Server, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("panel: refusing to serve without a token (fail-closed)")
	}
	tmpl, err := template.ParseFS(assets, "assets/panel.html")
	if err != nil {
		return nil, fmt.Errorf("panel: template: %w", err)
	}
	var ln net.Listener
	lastErr := error(nil)
	for try := 0; try < maxPortTries; try++ {
		p := port + try
		if p > 65535 {
			break
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			ln = l
			break
		}
		lastErr = err
	}
	if ln == nil {
		return nil, fmt.Errorf("panel: no free port in [%d,%d]: %w", port, port+maxPortTries-1, lastErr)
	}
	s := &Server{
		token: token,
		label: label,
		tmpl:  tmpl,
		ln:    ln,
		port:  ln.Addr().(*net.TCPAddr).Port,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePage)
	mux.HandleFunc("/style.css", s.handleCSS)
	s.http = &http.Server{
		Handler:           s.wrap(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = s.http.Serve(ln) }()
	return s, nil
}

// URL is the actual panel address including the token fragment.
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?t=%s", s.port, s.token)
}

// Port is the bound port (may differ from the configured one).
func (s *Server) Port() int { return s.port }

// Close shuts the listener down (bounded drain).
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}

// wrap is the envelope: every response passes Host validation, token
// authentication, and the security headers — in that order, and the
// headers land even on rejections.
func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Security headers on EVERY response, rejections included.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'")
		// No CORS: the header is never set — deliberately absent, not
		// permissive. (Checked by test: an Origin request must not
		// gain Access-Control-Allow-Origin.)
		if !localHost(r.Host) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if !s.authed(r) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authed compares the ?t= token in constant time. Empty supplied
// token never matches (zero-length compare with a non-empty secret
// returns 0 in subtle.ConstantTimeCompare — distinct lengths exit
// early, still non-matching).
func (s *Server) authed(r *http.Request) bool {
	got := r.URL.Query().Get("t")
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// localHost rejects DNS-rebinding Host headers: only the loopback
// names are accepted, any port suffix allowed.
func localHost(host string) bool {
	name := host
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[:i]
	}
	return name == "127.0.0.1" || name == "localhost" || name == "::1"
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.Execute(w, pageData{Label: s.label}); err != nil {
		// Headers may already be written; nothing salvageable — the
		// failure is visible in the response body, never silent.
		return
	}
}

func (s *Server) handleCSS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	css, err := assets.ReadFile("assets/style.css")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(css)
}
