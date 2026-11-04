package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// ReachProbeStaleJob raises probe.stale once for every reachability probe that
// has stopped reporting, so a probe that went silent is not mistaken for "all ok".
type ReachProbeStaleJob struct {
	probeService service.ReachProbeService
}

func NewReachProbeStaleJob() *ReachProbeStaleJob {
	return new(ReachProbeStaleJob)
}

func (j *ReachProbeStaleJob) Run() {
	if err := j.probeService.CheckStale(); err != nil {
		logger.Warning("reach probe stale check failed:", err)
	}
}
