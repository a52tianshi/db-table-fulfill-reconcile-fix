package reconcile

import (
	"fmt"
	"time"
)

// Report summarizes one Run. Confirmed counts come from the transactional
// reread, except InsertBatchSnapshot inserts, which are counted as scanned.
type Report struct {
	OldScanned int64 // rows read from Old
	NewScanned int64 // rows read from New (reverse pass)
	Suspected  int64 // candidates from the first, lock-free comparison
	Stale      int64 // candidates found consistent on reread (no action)

	Missing   int64 // confirmed in old only
	Different int64 // confirmed different
	Extra     int64 // confirmed in new only (never deleted)

	Inserted int64 // rows written into New by Insert
	Updated  int64 // rows overwritten in New by Update
	Warned   int64 // findings notified without writing

	Duration time.Duration
}

// Consistent reports whether this Run confirmed no inconsistency at all.
func (r Report) Consistent() bool { return r.Missing+r.Different+r.Extra == 0 }

func (r Report) String() string {
	return fmt.Sprintf("scanned old=%d new=%d suspected=%d stale=%d | missing=%d different=%d extra=%d | inserted=%d updated=%d warned=%d | %s",
		r.OldScanned, r.NewScanned, r.Suspected, r.Stale, r.Missing, r.Different, r.Extra,
		r.Inserted, r.Updated, r.Warned, r.Duration.Round(time.Millisecond))
}
