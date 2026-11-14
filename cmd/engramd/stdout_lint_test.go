package main

// R0 lint gate (owner spec §3): "One fmt.Println in the daemon
// startup path corrupts the protocol — enforce this with a lint rule,
// not a bug report." Static scan: the MCP paths (this package's
// runShim/runBoth/runServe and the whole mcpshim package) must never
// write stdout; the allowlist names the CLI verbs where printing is
// the product (runTick).
//
// It tracks the enclosing function by scanning for `^func ` lines and
// flags fmt.Print* / os.Stdout occurrences outside the allowlist,
// ignoring comment lines.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedMains names (file, function) pairs where stdout writes are
// the product: CLI verbs, never MCP paths, never serve mode.
var allowedMains = map[string]bool{
	"runTick": true,
}

// sanctionedWiring is the one allowed os.Stdout occurrence in MCP
// paths: handing stdout to the shim AS the protocol channel is the
// design, not a violation ("zero bytes on stdout except JSON-RPC").
const sanctionedWiring = "Serve(os.Stdin, os.Stdout)"

func TestLint_MCPPathsStdoutSilent(t *testing.T) {
	scanDir(t, ".", allowedMains)
	scanDir(t, filepath.Join("..", "..", "internal", "mcpshim"), map[string]bool{})
}

func scanDir(t *testing.T, dir string, allowed map[string]bool) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("scan %s: %v", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fn := ""
		for i, ln := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(ln)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.HasPrefix(ln, "func ") {
				fn = strings.Fields(ln)[1]
				fn = strings.Split(fn, "(")[0]
				continue
			}
			for _, tok := range []string{"fmt.Print", "os.Stdout"} {
				if strings.Contains(ln, tok) && !allowed[fn] {
					if tok == "os.Stdout" && strings.Contains(ln, sanctionedWiring) {
						continue // stdout AS the protocol channel
					}
					t.Errorf("%s:%d: %s in %s violates MCP stdout purity (allowlist: runTick + shim wiring only)",
						path, i+1, tok, fn)
				}
			}
		}
	}
}
