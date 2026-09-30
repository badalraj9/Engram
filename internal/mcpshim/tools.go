// Tool table: the six MCP tools over client/go (Q1).
//
// remember/recall/correct/attest/resolve/as_of map onto
// emit/recall/correct/attest/resolve/as_of. consolidate is deliberately
// absent (plan: the tick stays with CLI/client — the agent must not
// schedule maintenance, FR10). as_of carries the documented v1
// narrowing (low-tier history only; caller tier not on the wire) in
// its description, not in fine print elsewhere.
//
// Error split (spec §error-handling): JSON-shape problems and unknown
// tools are PROTOCOL errors (-32602); every core failure (validation,
// unreachable daemon, gate rejections) is a TOOL result with
// isError:true — the call arrived fine, the world said no.
package mcpshim

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	goClient "engram/client/go"
	"engram/internal/rpc"
)

// schema builds a JSON-Schema object node (spec: inputSchema is
// type:object with properties + required).
func schema(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func numProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// toolDef is one row of the table: name, description, schema, handler.
type toolDef struct {
	name        string
	description string
	schema      map[string]any
	run         func(ctx context.Context, c *goClient.Client, args map[string]any) (string, error)
}

// toolTable is the full six-tool surface (list order is wire order).
func toolTable() []toolDef {
	recordProps := func() map[string]any {
		return map[string]any{
			"subject":         strProp("fact subject (no free-text field exists — structuring quality is the caller's job, SRS 1.3)"),
			"predicate":       strProp("fact predicate"),
			"object":          strProp("fact object"),
			"source":          strProp("authoring source id"),
			"session":         strProp("authoring session id (source+session is identity, FR9)"),
			"acl":             strProp("low|user|system (default user; low enters quarantine)"),
			"base_confidence": numProp("starting confidence [0,1] (default 0.5)"),
			"valid_from":      numProp("world-time from, unix-nanos (default 0 = always)"),
			"valid_to":        numProp("world-time to, unix-nanos (default far future)"),
		}
	}
	return []toolDef{
		{
			name:        "remember",
			description: "Store a structured fact (subject/predicate/object + source/session). Returns the gate verdict and id.",
			schema:      schema(recordProps(), "subject", "predicate", "object", "source", "session"),
			run:         runRemember,
		},
		{
			name:        "recall",
			description: "Recall ranked, currently-true facts for terms — or ABSTAIN when nothing clears the bar. Never a forced guess. One hit per subject (per-subject dedup): pair subject+predicate to drill into one entity.",
			schema: schema(map[string]any{
				"terms":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "query terms (joined into the subject lane on v1 wire)"},
				"caller_tier": strProp("low|user|system — records filter before ranking (FR13)"),
				"budget":      numProp("max records (default 10)"),
			}, "terms", "caller_tier"),
			run: runRecall,
		},
		{
			name:        "correct",
			description: "Replace wrong content located via hint. hint names subject [predicate [object...]]; the remaining fields are the correction.",
			schema: schema(func() map[string]any {
				p := recordProps()
				p["hint"] = strProp("locator: subject [predicate [object...]] (floor match, never always-argmax)")
				return p
			}(), "hint", "subject", "predicate", "object", "source", "session"),
			run: runCorrect,
		},
		{
			name:        "attest",
			description: "Verdict-only vote (confirm/disconfirm) on a prior recall id. Cross-session confirms promote quarantined records.",
			schema: schema(map[string]any{
				"id":      strProp("record id from a prior recall"),
				"confirm": boolProp("true=confirm, false=disconfirm"),
				"source":  strProp("attesting source"),
				"session": strProp("attesting session (must differ from write cluster to promote)"),
			}, "id", "confirm", "source", "session"),
			run: runAttest,
		},
		{
			name:        "resolve",
			description: "Settle a BOTH-state contradiction pair with a ruling (keep-a|keep-b|merge).",
			schema: schema(map[string]any{
				"id_a":    strProp("first pair member"),
				"id_b":    strProp("second pair member"),
				"verdict": strProp("keep-a|keep-b|merge"),
			}, "id_a", "id_b", "verdict"),
			run: runResolve,
		},
		{
			name:        "as_of",
			description: "Bi-temporal history for terms at time t (terms[0]=subject, terms[1]=predicate). as_of reads low-tier history only (v1); caller tier is not conveyed on the wire.",
			schema: schema(map[string]any{
				"terms": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"t":     numProp("unix-nanos on the axis"),
				"axis":  strProp("valid|transaction"),
			}, "terms", "t", "axis"),
			run: runAsOf,
		},
	}
}

// callTool executes tools/call: shape errors are protocol errors,
// handler errors are isError tool results.
func (s *Server) callTool(req request) response {
	var p struct {
		Name string         `json:"name"`
		Args map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.Name) == "" {
		return response{JSONRPC: "2.0", ID: req.ID, Err: &rpcErr{Code: codeInvalidArgs, Message: "tools/call requires name + arguments"}}
	}
	if p.Args == nil {
		p.Args = map[string]any{}
	}
	for _, t := range s.tools {
		if t.name != p.Name {
			continue
		}
		text, err := t.run(context.Background(), s.client, p.Args)
		if err != nil {
			return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "error: " + err.Error()}},
				"isError": true,
			}}
		}
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
			"isError": false,
		}}
	}
	return response{JSONRPC: "2.0", ID: req.ID, Err: &rpcErr{Code: codeInvalidArgs, Message: "unknown tool: " + p.Name}}
}

// --- argument helpers (presence + type, fail-closed) ---

func argStr(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("argument %q must be a non-blank string", key)
	}
	return s, nil
}

func optStr(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func optNum(args map[string]any, key string, def float64) float64 {
	if v, ok := args[key].(float64); ok {
		return v
	}
	return def
}

// recordFromArgs builds the domain input shared by remember/correct.
// Defaults: acl user, confidence 0.5, valid [0, 1<<62) ("always true" —
// exact powers of two survive the JSON float64 crossing intact).
func recordFromArgs(args map[string]any) (rpc.InputRecord, error) {
	sub, err := argStr(args, "subject")
	if err != nil {
		return rpc.InputRecord{}, err
	}
	pred, err := argStr(args, "predicate")
	if err != nil {
		return rpc.InputRecord{}, err
	}
	obj, err := argStr(args, "object")
	if err != nil {
		return rpc.InputRecord{}, err
	}
	src, err := argStr(args, "source")
	if err != nil {
		return rpc.InputRecord{}, err
	}
	ses, err := argStr(args, "session")
	if err != nil {
		return rpc.InputRecord{}, err
	}
	return rpc.InputRecord{
		Subject: sub, Predicate: pred, Object: obj,
		ValidTime: rpc.Interval{
			From: int64(optNum(args, "valid_from", 0)),
			To:   int64(optNum(args, "valid_to", 1<<62)),
		},
		Provenance:     rpc.Provenance{Source: src, Session: ses},
		ACL:            rpc.AclTier(optStr(args, "acl", "user")),
		BaseConfidence: optNum(args, "base_confidence", 0.5),
	}, nil
}

// --- handlers (one per tool; core failures return errors → isError) ---

func runRemember(_ context.Context, c *goClient.Client, args map[string]any) (string, error) {
	in, err := recordFromArgs(args)
	if err != nil {
		return "", err
	}
	v, id, flagged, err := c.Emit(context.Background(), in)
	if err != nil {
		return "", err
	}
	if flagged != "" {
		return fmt.Sprintf("verdict=%s id=%s flagged=%s", v, id, flagged), nil
	}
	return fmt.Sprintf("verdict=%s id=%s", v, id), nil
}

func runRecall(_ context.Context, c *goClient.Client, args map[string]any) (string, error) {
	raw, ok := args["terms"].([]any)
	if !ok || len(raw) == 0 {
		return "", fmt.Errorf("argument \"terms\" must be a non-empty array of strings")
	}
	var terms []string
	for _, t := range raw {
		s, ok := t.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("argument \"terms\" must hold non-blank strings")
		}
		terms = append(terms, s)
	}
	tier, err := argStr(args, "caller_tier")
	if err != nil {
		return "", err
	}
	budget := int(optNum(args, "budget", 10))
	if budget <= 0 {
		return "", fmt.Errorf("argument \"budget\" must be positive")
	}
	recs, abstained, err := c.Recall(context.Background(), terms, budget, tier)
	if err != nil {
		return "", err
	}
	if abstained {
		return "ABSTAIN: nothing cleared the threshold", nil
	}
	var b strings.Builder
	for _, r := range recs {
		fmt.Fprintf(&b, "- %s | %s | %s [id=%s]\n", r.Subject, r.Predicate, r.Object, r.ID)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func runCorrect(_ context.Context, c *goClient.Client, args map[string]any) (string, error) {
	hint, err := argStr(args, "hint")
	if err != nil {
		return "", err
	}
	in, err := recordFromArgs(args)
	if err != nil {
		return "", err
	}
	id, v, err := c.Correct(context.Background(), hint, in)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("id=%s verdict=%s", id, v), nil
}

func runAttest(_ context.Context, c *goClient.Client, args map[string]any) (string, error) {
	id, err := argStr(args, "id")
	if err != nil {
		return "", err
	}
	confirm, ok := args["confirm"].(bool)
	if !ok {
		return "", fmt.Errorf("argument \"confirm\" must be a boolean")
	}
	src, err := argStr(args, "source")
	if err != nil {
		return "", err
	}
	ses, err := argStr(args, "session")
	if err != nil {
		return "", err
	}
	conf, promoted, err := c.Attest(context.Background(), id, confirm, src, ses)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("confidence=%.4f promoted=%v", conf, promoted), nil
}

func runResolve(_ context.Context, c *goClient.Client, args map[string]any) (string, error) {
	a, err := argStr(args, "id_a")
	if err != nil {
		return "", err
	}
	b, err := argStr(args, "id_b")
	if err != nil {
		return "", err
	}
	vs, err := argStr(args, "verdict")
	if err != nil {
		return "", err
	}
	var v rpc.ResolveVerdict
	switch rpc.ResolveVerdict(strings.TrimSpace(vs)) {
	case rpc.ResolveKeepA, rpc.ResolveKeepB, rpc.ResolveMerge:
		v = rpc.ResolveVerdict(strings.TrimSpace(vs))
	default:
		return "", fmt.Errorf("argument \"verdict\" must be keep-a|keep-b|merge")
	}
	id, err := c.Resolve(context.Background(), a, b, v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("id=%s", id), nil
}

func runAsOf(_ context.Context, c *goClient.Client, args map[string]any) (string, error) {
	raw, ok := args["terms"].([]any)
	if !ok || len(raw) == 0 {
		return "", fmt.Errorf("argument \"terms\" must be a non-empty array of strings")
	}
	var terms []string
	for _, t := range raw {
		s, ok := t.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("argument \"terms\" must hold non-blank strings")
		}
		terms = append(terms, s)
	}
	ts, ok := args["t"].(float64)
	if !ok {
		return "", fmt.Errorf("argument \"t\" must be a number")
	}
	axis, err := argStr(args, "axis")
	if err != nil {
		return "", err
	}
	var ax rpc.Axis
	switch rpc.Axis(strings.TrimSpace(axis)) {
	case rpc.AxisValid, rpc.AxisTransaction:
		ax = rpc.Axis(strings.TrimSpace(axis))
	default:
		return "", fmt.Errorf("argument \"axis\" must be valid|transaction")
	}
	recs, err := c.AsOf(context.Background(), terms, int64(ts), ax)
	if err != nil {
		return "", err
	}
	if len(recs) == 0 {
		return "no history", nil
	}
	var b strings.Builder
	for _, r := range recs {
		fmt.Fprintf(&b, "- %s | %s | %s [id=%s status=%s]\n", r.Subject, r.Predicate, r.Object, r.ID, r.Status)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
