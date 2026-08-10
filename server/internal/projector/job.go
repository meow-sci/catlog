package projector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// A rebuild is a routine operation, not an emergency one.
//
// It is how a shadow ban's events leave the boards, how a new board gets the
// history that preceded it, and how a changed fold is applied to everything it
// should have applied to all along — on top of the nightly correctness backstop
// D22 already asked for. That makes it something an operator runs *often*, and
// something the server sometimes has to run on its own initiative.
//
// Three things follow, and this file is all three:
//
//   - **It runs detached.** A rebuild is two passes over the whole log — minutes
//     at production size — and an HTTP request must not be holding a connection,
//     an admin write lock, or a client's patience for the duration.
//   - **It is observable while it runs.** "Started ten minutes ago" and "started
//     ten minutes ago and is 4% through" are different operational facts.
//   - **A request that arrives during one is not lost.** A shadow ban applied
//     while a rebuild is scanning would otherwise be silently missing from the
//     file that gets swapped in, because the scan had already passed those rows.
//     Queueing one more run is the whole fix.

// RebuildPhase names what a running rebuild is doing.
type RebuildPhase string

const (
	// PhaseIdle means no rebuild has run in this process.
	PhaseIdle RebuildPhase = "idle"
	// PhaseState is pass 1: flight state and careers over the whole log.
	PhaseState RebuildPhase = "state"
	// PhaseBoards is pass 2: every board, against the completed flight state.
	PhaseBoards RebuildPhase = "boards"
	// PhaseSwapping is the atomic file swap.
	PhaseSwapping RebuildPhase = "swapping"
	// PhaseDone and PhaseFailed are terminal.
	PhaseDone   RebuildPhase = "done"
	PhaseFailed RebuildPhase = "failed"
)

// Running reports whether this phase is one a rebuild passes through rather
// than ends at.
func (p RebuildPhase) Running() bool {
	return p == PhaseState || p == PhaseBoards || p == PhaseSwapping
}

// ErrRebuildInProgress is returned by [Projector.StartRebuild] when one is
// already running. It is not an error condition so much as an answer:
// [Projector.RequestRebuild] is what a caller wants when "another one after
// this" is acceptable.
var ErrRebuildInProgress = errors.New("projector: a rebuild is already running")

// RebuildStatus is one rebuild's observable state — what `GET
// /admin/projections/rebuild` returns.
type RebuildStatus struct {
	// Phase is where it is, or where it ended.
	Phase RebuildPhase `json:"phase"`
	// Reason is why it started: an operator's request, a shadow ban, a stale
	// build stamp at boot. Recorded because "why is the server rebuilding right
	// now" is the first question anyone asks.
	Reason string `json:"reason"`
	// StartedAt and FinishedAt are unix ms; FinishedAt is zero while running.
	StartedAt  int64 `json:"started_at"`
	FinishedAt int64 `json:"finished_at,omitempty"`
	// Scanned is how many event rows the current pass has read, and Head is how
	// many it will read. Together they are the progress bar; each pass counts
	// from zero.
	Scanned int64 `json:"scanned"`
	Head    int64 `json:"head"`
	// Queued reports that another rebuild will start as soon as this one ends,
	// because something changed the log underneath it.
	Queued bool `json:"queued"`
	// Err is the failure message, empty unless Phase is [PhaseFailed]. A
	// failed rebuild changes nothing: the live file was never swapped.
	Err string `json:"error,omitempty"`
	// Result is the completed rebuild's report, present once Phase is
	// [PhaseDone].
	Result *RebuildResult `json:"result,omitempty"`
	// Suspended reports that the incremental fold loop is *not* running because
	// the live file was built by a different fold set. It is the state a stale
	// deploy sits in until its rebuild lands, and it means boards are stale but
	// never wrong.
	Suspended bool `json:"suspended"`
}

// rebuildState is the projector's half of the above, behind a mutex. It is
// deliberately not a set of atomics: a status is read as a whole and has to be
// self-consistent, and it is read at human frequency.
type rebuildState struct {
	mu      sync.Mutex
	status  RebuildStatus
	running bool
	// queued holds the reason for the follow-up run, if one has been asked for.
	queued string
	// suspended stops the incremental loop. See [Projector.checkBuild].
	suspended bool
}

// RebuildStatus reports the current or last rebuild.
func (p *Projector) RebuildStatus() RebuildStatus {
	p.rb.mu.Lock()
	defer p.rb.mu.Unlock()
	s := p.rb.status
	s.Queued = p.rb.queued != ""
	s.Suspended = p.rb.suspended
	if s.Phase == "" {
		s.Phase = PhaseIdle
	}
	return s
}

// Suspended reports whether the incremental fold loop is parked because the
// live projections file was built by a different fold set.
func (p *Projector) Suspended() bool {
	p.rb.mu.Lock()
	defer p.rb.mu.Unlock()
	return p.rb.suspended
}

// StartRebuild kicks off a rebuild in the background and returns as soon as it
// has started, reporting [ErrRebuildInProgress] if one already is.
//
// The context is deliberately **not** the caller's. A rebuild that a request
// cancellation could kill halfway would leave a half-built scratch file and a
// live database that is still whatever it was, which is safe but wasteful; more
// to the point, the caller has no business deciding how long the server's own
// maintenance may take.
func (p *Projector) StartRebuild(reason string) (RebuildStatus, error) {
	p.rb.mu.Lock()
	if p.rb.running {
		p.rb.mu.Unlock()
		return p.RebuildStatus(), ErrRebuildInProgress
	}
	p.rb.running = true
	p.rb.status = RebuildStatus{
		Phase:     PhaseState,
		Reason:    reason,
		StartedAt: p.nowMillis(),
	}
	p.rb.mu.Unlock()

	go p.runRebuild(reason)
	return p.RebuildStatus(), nil
}

// RequestRebuild starts a rebuild, or — if one is already running — records
// that another is owed the moment it finishes.
//
// This is what every moderation verb calls. A shadow ban applied while a
// rebuild is halfway through its second pass is invisible to that rebuild: the
// scan has already read those rows. Without the follow-up run, the file swapped
// in would still hold the shadowbanned player's records, and the only signal
// would be an operator noticing them on a board.
func (p *Projector) RequestRebuild(reason string) RebuildStatus {
	p.rb.mu.Lock()
	if p.rb.running {
		p.rb.queued = reason
		p.rb.mu.Unlock()
		p.log.Info("rebuild queued behind the running one", "reason", reason)
		return p.RebuildStatus()
	}
	p.rb.mu.Unlock()

	status, err := p.StartRebuild(reason)
	if err != nil && !errors.Is(err, ErrRebuildInProgress) {
		p.log.Error("could not start a rebuild", "reason", reason, "err", err)
	}
	return status
}

// runRebuild is the background body: one rebuild, then any that was queued
// behind it, until nothing more is owed.
func (p *Projector) runRebuild(reason string) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), RebuildTimeout)
		res, err := p.Rebuild(ctx)
		cancel()

		p.rb.mu.Lock()
		p.rb.status.FinishedAt = p.nowMillis()
		if err != nil {
			p.rb.status.Phase, p.rb.status.Err = PhaseFailed, err.Error()
			p.log.Error("rebuild failed", "reason", reason, "err", err)
		} else {
			p.rb.status.Phase, p.rb.status.Result = PhaseDone, &res
			// The new file carries this binary's build stamp, so whatever made
			// the loop suspend has been answered.
			p.rb.suspended = false
		}
		next := p.rb.queued
		p.rb.queued = ""
		if next == "" {
			p.rb.running = false
			p.rb.mu.Unlock()
			return
		}
		// Another one is owed. Stay `running` across the handover so nothing
		// can slip in and start a third concurrently.
		p.rb.status = RebuildStatus{Phase: PhaseState, Reason: next, StartedAt: p.nowMillis()}
		p.rb.mu.Unlock()
		reason = next
		p.log.Info("starting the queued rebuild", "reason", reason)
	}
}

// RebuildTimeout bounds one rebuild. Generous on purpose: it is two passes over
// the entire event log, and the alternative to finishing is a scratch file that
// gets deleted and a live database that is still stale.
const RebuildTimeout = 2 * time.Hour

// setPhase publishes which pass is running and resets the scan counter, so the
// two passes each report progress from zero against the same head.
func (p *Projector) setPhase(phase RebuildPhase, head int64) {
	p.rb.mu.Lock()
	defer p.rb.mu.Unlock()
	p.rb.status.Phase = phase
	p.rb.status.Scanned = 0
	p.rb.status.Head = head
}

// addScanned advances the progress counter by one batch.
func (p *Projector) addScanned(n int) {
	if n <= 0 {
		return
	}
	p.rb.mu.Lock()
	defer p.rb.mu.Unlock()
	p.rb.status.Scanned += int64(n)
}

// --- the build stamp ----------------------------------------------------------

// BuildInfo is what the server reports about the live projections file's
// provenance.
type BuildInfo struct {
	// Stamped is what the file says built it. Zero for a file that predates the
	// stamp or has never been rebuilt.
	Stamped store.ProjectionBuild `json:"stamped"`
	// Expected is what this binary's folds would produce.
	Expected string `json:"expected_build_id"`
	// Stale means the two disagree: the boards in that file were computed by a
	// different definition of the boards.
	Stale bool `json:"stale"`
}

// Build reports the live file's build stamp against this binary's fold set.
func (p *Projector) Build(ctx context.Context) (BuildInfo, error) {
	var info BuildInfo
	err := p.live.With(func(proj *store.Projections) error {
		var err error
		info.Stamped, err = proj.Build(ctx)
		if err != nil {
			return err
		}
		info.Expected = stats.BuildID(proj.Version)
		info.Stale = !stats.SameBuild(info.Stamped.BuildID, proj.Version) || !info.Stamped.Complete
		return nil
	})
	return info, err
}

// checkBuild is the startup decision, and the only place the fold loop is ever
// suspended.
//
// Four cases, and the third is the one that matters:
//
//  1. **The stamp matches.** Nothing to do.
//  2. **It does not match, and the file holds nothing yet** (checkpoint 0).
//     Folding forward from zero *is* a full build, so the file is stamped and
//     the loop runs normally. This is every fresh deployment and most tests.
//  3. **It does not match and the file holds history.** The loop is suspended
//     and a rebuild is started. Suspending is the whole point: if the loop kept
//     folding, a board added by this deploy would fill up with events from now
//     onwards and look like a real board that nobody has set a record on, when
//     in truth it is missing everything before the deploy. Empty is a state a
//     reader understands; short-by-history is not.
//  4. **It does not match, holds history, and cannot be rebuilt** — an
//     in-memory projections database, which has no file to swap. Suspending
//     would park the loop forever, so it logs loudly and carries on.
func (p *Projector) checkBuild(ctx context.Context, autoRebuild bool) error {
	info, err := p.Build(ctx)
	if err != nil {
		return err
	}
	if !info.Stale {
		p.log.Info("projections build stamp matches", "build_id", info.Expected)
		return nil
	}

	var checkpoint int64
	if err := p.live.With(func(proj *store.Projections) error {
		var err error
		checkpoint, err = proj.Checkpoint(ctx, nil, store.AllProjections)
		return err
	}); err != nil {
		return err
	}

	if checkpoint == 0 {
		if err := p.stampLive(ctx); err != nil {
			return err
		}
		p.log.Info("stamped an empty projections database", "build_id", info.Expected)
		return nil
	}

	if p.live.Path() == "" || p.live.Path() == store.MemoryPath {
		p.log.Error("the projections database was built by a different fold set and cannot be rebuilt",
			"stamped", info.Stamped.BuildID, "expected", info.Expected)
		return nil
	}

	p.rb.mu.Lock()
	p.rb.suspended = true
	p.rb.mu.Unlock()
	p.log.Warn("projections were built by a different fold set — folding suspended until a rebuild lands",
		"stamped", info.Stamped.BuildID, "expected", info.Expected,
		"fold_version", stats.BuildVersion, "checkpoint", checkpoint, "auto_rebuild", autoRebuild)

	if autoRebuild {
		p.RequestRebuild("the projections build stamp did not match this binary's folds")
	}
	return nil
}

// stampLive writes this binary's build stamp onto the live file. It is only
// ever called for a database that holds nothing, where "the incremental loop
// built this from seq 0" is true by definition.
func (p *Projector) stampLive(ctx context.Context) error {
	return p.live.With(func(proj *store.Projections) error {
		return proj.SetBuild(ctx, nil, store.ProjectionBuild{
			BuildID:       stats.BuildID(proj.Version),
			FoldVersion:   stats.BuildVersion,
			SchemaVersion: proj.Version,
			BuiltFromSeq:  0,
			BuiltAt:       p.nowMillis(),
			Complete:      true,
		})
	})
}

// nowMillis is the projector's clock, taken from the store options so a
// development build that moves the server's notion of now moves this too.
func (p *Projector) nowMillis() int64 {
	if p.storeOpts.Now != nil {
		return p.storeOpts.Now().UnixMilli()
	}
	return time.Now().UnixMilli()
}

// describeBuild is the boot log line: what this binary folds, and what the file
// on disk was folded by.
func (p *Projector) describeBuild(info BuildInfo) string {
	if !info.Stale {
		return fmt.Sprintf("projections are current (%s)", info.Expected)
	}
	return fmt.Sprintf("projections are stale: file %q, binary %q",
		info.Stamped.BuildID, info.Expected)
}
