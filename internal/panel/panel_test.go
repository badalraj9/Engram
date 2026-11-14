package panel

// R0 panel gates (BUILD-PLAN R0 item 4, run against the {test} page
// itself): no token → 403, token → 200, spoofed Host/Origin → 403 /
// no CORS header ever, XSS-payload label renders as text, port
// fallback reports the actual port, nosniff + strict CSP on every
// response including rejections.

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, url string, headers map[string]string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, string(body)
}

func start(t *testing.T, port int, token, label string) *Server {
	t.Helper()
	s, err := Listen(port, token, label)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestPanel_TokenGate pins AC11: without the token the panel rejects;
// with it, the page serves.
func TestPanel_TokenGate(t *testing.T) {
	s := start(t, 0, "sekrit", "{test}")
	code, _, _ := get(t, "http://127.0.0.1:"+itoa(s.Port())+"/", nil)
	if code != http.StatusForbidden {
		t.Fatalf("no-token request must be 403, got %d", code)
	}
	code, _, body := get(t, s.URL(), nil)
	if code != http.StatusOK {
		t.Fatalf("token request must be 200, got %d", code)
	}
	if !strings.Contains(body, "{test}") {
		t.Fatalf("page must carry the label, got %q", body)
	}
}

// TestPanel_HostSpoofRejected pins the DNS-rebinding gate: a request
// addressed at the right port but carrying an attacker Host header is
// rejected before anything renders. (req.Host is the only way to
// spoof the header — Go's client ignores Host inside Header.Set.)
func TestPanel_HostSpoofRejected(t *testing.T) {
	s := start(t, 0, "sekrit", "{test}")
	req, err := http.NewRequest(http.MethodGet, s.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example.com"
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("spoofed Host must be 403, got %d", res.StatusCode)
	}
	// A localhost host with any port suffix is accepted.
	req, err = http.NewRequest(http.MethodGet, "http://127.0.0.1:"+itoa(s.Port())+"/?t=sekrit", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "localhost:9999"
	res, err = (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("localhost Host must pass, got %d", res.StatusCode)
	}
}

// TestPanel_NoCORSEver pins the no-CORS rule: the header is absent on
// every response, even when the request carries an Origin.
func TestPanel_NoCORSEver(t *testing.T) {
	s := start(t, 0, "sekrit", "{test}")
	_, hdr, _ := get(t, s.URL(), map[string]string{"Origin": "https://evil.example.com"})
	if hdr.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("ACAO must never be set (no CORS, per spec)")
	}
}

// TestPanel_XSSEscaped pins AC12 on the one dynamic slot: a label
// holding an XSS payload renders as text — the template output
// contains the escaped form, never the raw tag.
func TestPanel_XSSEscaped(t *testing.T) {
	payload := `<img src=x onerror="alert(1)">`
	s := start(t, 0, "sekrit", payload)
	code, _, body := get(t, s.URL(), nil)
	if code != http.StatusOK {
		t.Fatalf("page must serve, got %d", code)
	}
	if strings.Contains(body, "<img") {
		t.Fatalf("raw payload must never reach the page: %q", body)
	}
	if !strings.Contains(body, "&lt;img") {
		t.Fatalf("payload must render as text (escaped), got %q", body)
	}
}

// TestPanel_HeadersOnEveryResponse pins nosniff + CSP on 200s AND on
// rejections — the envelope cannot be bypassed by status.
func TestPanel_HeadersOnEveryResponse(t *testing.T) {
	s := start(t, 0, "sekrit", "{test}")
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"with token", s.URL()},
		{"no token", "http://127.0.0.1:" + itoa(s.Port()) + "/"},
	} {
		_, hdr, _ := get(t, tc.url, nil)
		if hdr.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: nosniff missing", tc.name)
		}
		if !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatalf("%s: strict CSP missing", tc.name)
		}
	}
}

// TestPanel_PortFallback pins the auto-fallback: when the configured
// port is occupied, the next free port serves and the actual URL
// reports it.
func TestPanel_PortFallback(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	taken := occupied.Addr().(*net.TCPAddr).Port
	s := start(t, taken, "sekrit", "{test}")
	if s.Port() == taken {
		t.Fatal("fallback must not bind the occupied port")
	}
	code, _, _ := get(t, s.URL(), nil)
	if code != http.StatusOK {
		t.Fatalf("fallback panel must serve, got %d", code)
	}
}

// TestPanel_NoTokenConfigFailsClosed pins the second fail-closed
// layer: Listen itself refuses an empty token regardless of config.
func TestPanel_NoTokenConfigFailsClosed(t *testing.T) {
	if _, err := Listen(0, "  ", "{test}"); err == nil {
		t.Fatal("blank token must be refused at Listen")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
