// Daemon wiring (P5 — the process behind the socket).
//
// This package is the only place that imports intake, query,
// maintenance, index, truth, and rpc together: it opens the store,
// rebuilds the serving index from the log at boot (DESIGN §2
// snapshot procedure — every start re-proves rebuildability), adapts
// the core paths onto rpc.Ports, and serves. No logic lives here
// beyond construction + adaptation + lifecycle; verdicts stay in
// processing, execution in intake, reads in query, ticks in
// maintenance.
//
// Single-writer: all port closures run under the rpc.Server's mutex
// (documented on rpc.Ports) — this package adds no goroutine, no
// ticker, no lock of its own. Rebuild swaps the live index + emitter
// under that same mutex, so in-flight RPCs never see a torn pair.
package daemon

import (
	"log"
	"time"

	"engram/internal/config"
	"engram/internal/index"
	"engram/internal/intake"
	"engram/internal/maintenance"
	"engram/internal/query"
	"engram/internal/rpc"
	"engram/internal/telemetry"
	"engram/internal/truth"
)

// Daemon is the process: store + serving index + emitter + server.
type Daemon struct {
	store   *truth.Store
	idx     *index.Index
	emitter *intake.Emitter
	cfg     config.Config
	srv     *rpc.Server
}

// Open loads cfg, opens the store, rebuilds the serving index from
// the log (boot ≡ rebuild proof), and wires the server. The caller
// owns Serve/Stop + Close.
func Open(cfg config.Config) (*Daemon, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	store, err := truth.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	staged, err := store.ListStaged()
	if err != nil {
		store.Close()
		return nil, err
	}
	idx, err := index.Rebuild(store, staged)
	if err != nil {
		store.Close()
		return nil, err
	}
	d := &Daemon{store: store, idx: idx, cfg: cfg}
	d.emitter = intake.NewEmitter(store, idx, cfg.RateLimitPerSec)
	srv, err := rpc.NewServer(cfg, d.ports())
	if err != nil {
		store.Close()
		return nil, err
	}
	d.srv = srv
	return d, nil
}

// ports adapts the core paths onto the transport boundary. Closures
// read d.idx/d.emitter per call (never captured) so Rebuild's swap is
// visible to the next op under the server mutex.
func (d *Daemon) ports() rpc.Ports {
	return rpc.Ports{
		Emit: func(in rpc.InputRecord) (rpc.Verdict, string, string, error) {
			res, err := d.emitter.Emit(in)
			if err != nil {
				return "", "", "", err
			}
			return res.Verdict, res.ID, res.FlaggedID, nil
		},
		Correct: func(sub, pred, obj string, cor rpc.InputRecord) (string, rpc.Verdict, error) {
			rec, verdict, err := d.emitter.Correct(
				intake.CorrectHint{Subject: sub, Predicate: pred, Object: obj}, cor)
			if err != nil {
				return "", "", err
			}
			return rec.ID, verdict, nil
		},
		Attest: func(id string, confirm bool, src, ses string) (float64, bool, error) {
			return d.emitter.Attest(id, confirm, src, ses)
		},
		Resolve: func(aID, bID string, v rpc.ResolveVerdict, prov rpc.Provenance) (string, error) {
			return d.emitter.Resolve(aID, bID, v, prov)
		},
		Recall: func(spec rpc.RecallSpec) ([]rpc.Record, bool, error) {
			res, err := query.Recall(d.idx, d.store, query.Spec{
				Subject: spec.Subject, Caller: spec.Caller, Budget: spec.Budget,
				Threshold: spec.Threshold, Beta: spec.Beta,
				TrustLow: spec.TrustLow, TrustUser: spec.TrustUser, TrustSys: spec.TrustSys,
				// VALID-NOW (DESIGN §1): the daemon stamps valid-now
				// at recall execution — unix-nanos, explicit
				// parameter, no wire field, no hidden clock (FR10).
				Now: time.Now().UnixNano(),
			})
			if err != nil {
				return nil, false, err
			}
			return res.Records, res.Abstained, nil
		},
		// Documented narrowing: v1 wire as_of carries no identity, so
		// the daemon serves public (low) history — most restrictive,
		// never unfiltered state.
		AsOf: func(subject, predicate string, t int64, axis rpc.Axis) ([]rpc.Record, error) {
			return query.AsOf(d.store, subject, predicate, t, axis, rpc.ACLLow)
		},
		Consolidate: func(tick int64) (int, int, float64, float64, error) {
			// Two clocks per contract (DESIGN §6): wall-now for
			// validity, stamped here where the authority lives.
			res, err := maintenance.Consolidate(d.store, d.idx, tick, time.Now().UnixNano())
			if err != nil {
				return 0, 0, 0, 0, err
			}
			// Per-cause split (P2 contract): the wire carries the
			// lumped total; the split surfaces here until Q4 widens
			// the wire. Visible without reading code.
			log.Printf("engram: tick=%d consolidated=%d expired=%d retired=%d",
				tick, res.Consolidated, res.Expired, res.Retired)
			return res.Consolidated, res.Pruned, res.Thresholds.Abstain, res.Thresholds.Evict, nil
		},
		Rebuild: func() error { return d.rebuild() },
	}
}

// rebuild discards the serving index and replays the log (DESIGN §2),
// then re-points the emitter. Under the server mutex by contract.
func (d *Daemon) rebuild() error {
	staged, err := d.store.ListStaged()
	if err != nil {
		return err
	}
	fresh, err := index.Rebuild(d.store, staged)
	if err != nil {
		return err
	}
	d.idx = fresh
	d.emitter = intake.NewEmitter(d.store, d.idx, d.cfg.RateLimitPerSec)
	return nil
}

// Serve blocks serving gRPC on sock (host owns the goroutine).
func (d *Daemon) Serve(sock string) error { return d.srv.Serve(sock) }

// Stop drains and removes the socket file.
func (d *Daemon) Stop() { d.srv.Stop() }

// Metrics exposes the server registry for host-side scraping.
func (d *Daemon) Metrics() *telemetry.Registry { return d.srv.Metrics() }

// Close releases the store. Drain first via Stop.
func (d *Daemon) Close() error { return d.store.Close() }
