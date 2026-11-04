package job

import (
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// reachProbePruneEvery spaces the history cleanup: the job runs every minute,
// the seven-day history needs trimming far less often.
const reachProbePruneEvery = time.Hour

// ReachProbeStaleJob raises probe.stale once for every reachability probe that
// has stopped reporting, so a probe that went silent is not mistaken for "all ok",
// and once an hour drops results and orphan targets older than the history.
type ReachProbeStaleJob struct {
	probeService service.ReachProbeService
	mu           sync.Mutex
	lastPrune    time.Time
	now          func() time.Time // seam for tests
}

func NewReachProbeStaleJob() *ReachProbeStaleJob {
	return &ReachProbeStaleJob{now: time.Now}
}

func (j *ReachProbeStaleJob) Run() {
	if err := j.probeService.CheckStale(); err != nil {
		logger.Warning("reach probe stale check failed:", err)
	}
	now := time.Now()
	if j.now != nil {
		now = j.now()
	}
	j.mu.Lock()
	due := j.lastPrune.IsZero() || now.Sub(j.lastPrune) >= reachProbePruneEvery
	if due {
		j.lastPrune = now
	}
	j.mu.Unlock()
	if !due {
		return
	}
	if err := j.probeService.PruneHistory(); err != nil {
		logger.Warning("reach probe history cleanup failed:", err)
	}
}
