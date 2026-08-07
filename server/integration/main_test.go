//go:build integration

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A leaked child process matters more here than in most projects.
//
// tursogo takes an **exclusive whole-file lock** that shuts other processes out
// of a database entirely — not just for writes, for reads (§5.4). A `catlogd`
// that outlives its test therefore does not merely waste a few megabytes: it
// holds a temp-directory database open forever, its temp directory cannot be
// removed, and if it is still bound to a port the next run can collide with it.
// A stray is a silent, compounding failure, which is exactly the kind that gets
// blamed on something else.
//
// So every child a fixture starts is registered here, and [TestMain] reaps
// whatever is still alive once the package's tests are done — whether they
// passed, failed, or unwound through a panic. The registry is belt to the
// fixtures' braces: `t.Cleanup` still stops each process on the normal path,
// and this catches the paths where it did not run.
//
// Not covered: `go test -timeout` firing. Go panics the whole binary from its
// own watchdog and no cleanup or TestMain code runs at all. The mitigation for
// that is on the other side — no fixture here waits longer than
// [gracefulStop] + [projectorWait], both well inside any sane -timeout.
var children struct {
	mu   sync.Mutex
	live map[*exec.Cmd]string // cmd → what it is, for the report
}

// gracefulStop is how long a child gets to honour SIGINT before it is killed.
// Shutdown drains two HTTP servers, stops the ingest writer and the projector
// and checkpoints two WALs (§5.4), so it is not instant — but it is also not
// thirty seconds, and a fixture that waits thirty seconds for a wedged process
// is a fixture that will eventually be the reason a run times out.
const gracefulStop = 15 * time.Second

func TestMain(m *testing.M) {
	children.live = map[*exec.Cmd]string{}

	// Ctrl-C during a long run would otherwise orphan every child.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "integration: interrupted; reaping child processes")
		reapChildren()
		os.Exit(130)
	}()

	code := m.Run()

	if n := reapChildren(); n > 0 {
		fmt.Fprintf(os.Stderr,
			"integration: %d child process(es) outlived their test and were killed; "+
				"a fixture's cleanup did not run (§5.4: they hold exclusive database locks)\n", n)
		if code == 0 {
			// Do not let a leak pass silently just because the assertions were
			// happy: the leak is itself a defect in the fixtures.
			code = 1
		}
	}
	os.Exit(code)
}

// trackChild registers a started process for reaping.
func trackChild(cmd *exec.Cmd, what string) {
	children.mu.Lock()
	defer children.mu.Unlock()
	children.live[cmd] = what
}

// untrackChild deregisters a process a fixture has already stopped.
func untrackChild(cmd *exec.Cmd) {
	children.mu.Lock()
	defer children.mu.Unlock()
	delete(children.live, cmd)
}

// reapChildren kills every still-registered process and reports how many there
// were. It kills rather than signalling politely: by the time this runs the
// test that owned the process is over, so there is nothing left to shut down
// gracefully for, and the only thing that matters is that the file lock goes.
func reapChildren() int {
	children.mu.Lock()
	defer children.mu.Unlock()

	killed := 0
	for cmd, what := range children.live {
		if cmd.Process == nil || cmd.ProcessState != nil {
			delete(children.live, cmd)
			continue
		}
		fmt.Fprintf(os.Stderr, "integration: killing stray %s (pid %d)\n", what, cmd.Process.Pid)
		_ = cmd.Process.Kill()
		// Reap it, so no zombie is left behind and the lock is definitely
		// released before the next package's tests start.
		_, _ = cmd.Process.Wait()
		delete(children.live, cmd)
		killed++
	}
	return killed
}
