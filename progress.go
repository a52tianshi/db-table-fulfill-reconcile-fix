package reconcile

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Phase is the pass a Run is in.
type Phase uint8

const (
	ScanOld Phase = iota + 1 // forward: scan Old, look up New (missing / different)
	ScanNew                  // reverse: scan New, look up Old (extra)
)

func (p Phase) String() string {
	switch p {
	case ScanOld:
		return "old"
	case ScanNew:
		return "new"
	}
	return "none"
}

// Progress is a snapshot delivered to Config.Progress. All counts in Report
// are committed results: the batch has been looked up, rechecked and its
// writes committed before the callback runs.
type Progress struct {
	Phase     Phase
	Batch     int  // batches completed in this phase
	Pages     int  // pages scanned in this phase
	PhaseDone bool // this phase's scan finished and its last batch is done
	Report    Report
	Elapsed   time.Duration
	Done      bool  // last callback of the Run
	Err       error // non-nil when the Run stopped with an error (Done is true)
}

// NoProgress explicitly opts out of progress callbacks.
func NoProgress(Progress) {}

// LogProgress returns a callback that writes one line at most every
// `every`, plus at each phase end and at the end of the Run. It is safe to
// share between concurrent Runs. Write errors are ignored.
func LogProgress(w io.Writer, every time.Duration) func(Progress) {
	var mu sync.Mutex
	var last time.Time
	return func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if !p.Done && !p.PhaseDone && !last.IsZero() && now.Sub(last) < every {
			return
		}
		last = now
		state := "running"
		switch {
		case p.Err != nil:
			state = "failed: " + p.Err.Error()
		case p.Done:
			state = "done"
		case p.PhaseDone:
			state = "phase done"
		}
		rep := p.Report
		fmt.Fprintf(w, "reconcile progress phase=%s batch=%d pages=%d scanned old=%d new=%d | missing=%d different=%d extra=%d | inserted=%d updated=%d warned=%d | retries=%d | %s | %s\n",
			p.Phase, p.Batch, p.Pages, rep.OldScanned, rep.NewScanned, rep.Missing, rep.Different, rep.Extra,
			rep.Inserted, rep.Updated, rep.Warned, rep.Retries, p.Elapsed.Round(time.Second), state)
	}
}
