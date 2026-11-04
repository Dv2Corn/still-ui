package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/mhsanaei/3x-ui/v3/internal/certwatch"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/health"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

var (
	healthTracker = health.NewTracker()
	// xrayCrashes keeps a day of crash times: the longest restart window the settings allow.
	xrayCrashes = health.NewEventWindow(24 * time.Hour)
	// procRoot and cgroupRoot are seams for tests.
	procRoot   = "/proc"
	cgroupRoot = "/sys/fs/cgroup"
)

// RecordXrayCrash counts one unexpected stop of the core (a crash of the child, or a restart
// systemd made by itself) for the "restarted N times in M minutes" alert.
func RecordXrayCrash(at time.Time) { xrayCrashes.Add(at) }

// HealthAlertService watches the server and publishes health alerts on the event bus; the
// notifiers decide, by their enabled events, who hears about them.
type HealthAlertService struct {
	settingService SettingService
	xrayService    XrayService
}

// Run takes one round of checks and publishes what changed.
func (s *HealthAlertService) Run() {
	th := s.settingService.GetHealthAlertThresholds()
	now := time.Now()
	notices := healthTracker.Observe(now, time.Duration(th.RemindHours)*time.Hour, s.collect(now, th))
	for _, n := range notices {
		logger.Infof("health alert %s %s (%s)", n.Event, n.Phase, n.Key)
		if eventBus == nil {
			continue
		}
		eventBus.Publish(eventbus.Event{
			Type:   eventbus.EventType(n.Event),
			Source: n.Key,
			Data:   &eventbus.HealthAlertData{Phase: string(n.Phase), Since: n.Since, Params: n.Params},
		})
	}
}

func (s *HealthAlertService) collect(now time.Time, th HealthAlertThresholds) []health.Check {
	var checks []health.Check
	checks = append(checks, s.xrayChecks(now, th)...)
	checks = append(checks, memoryChecks(th.MemoryPercent)...)
	checks = append(checks, diskChecks(th.DiskPercent)...)
	checks = append(checks, s.certChecks(now, th.CertDays)...)
	if c, ok := backupCheck(); ok {
		checks = append(checks, c)
	}
	return checks
}

func (s *HealthAlertService) xrayChecks(now time.Time, th HealthAlertThresholds) []health.Check {
	down := health.Check{Key: "xray.down", Event: string(eventbus.EventXrayDown), Confirm: 2}
	if s.xrayService.DidXrayCrash() {
		down.Problem = true
		errText := "not running"
		if err := s.xrayService.GetXrayErr(); err != nil {
			errText = err.Error()
		}
		down.Params = map[string]string{"Error": oneLineCut(errText, 300)}
	}
	window := time.Duration(max(th.XrayRestartWindow, 1)) * time.Minute
	count := xrayCrashes.Count(now, window)
	flapping := health.Check{
		Key: "xray.flapping", Event: string(eventbus.EventXrayFlapping),
		Problem: count >= max(th.XrayRestarts, 1),
		Params:  map[string]string{"Count": strconv.Itoa(count), "Window": strconv.Itoa(max(th.XrayRestartWindow, 1))},
	}
	return []health.Check{down, flapping}
}

// memUsage is one memory figure checked against its limit.
type memUsage struct {
	name      string
	used      uint64
	limit     uint64
	limitKind string // "MemoryMax" or "RAM"
}

// memoryChecks compares the panel and the core with their systemd MemoryMax (cgroup v2), or with
// the machine's RAM when there is none. A core that is the panel's child shares the panel's cgroup
// and is checked once with it.
func memoryChecks(threshold int) []health.Check {
	if runtime.GOOS != "linux" || threshold <= 0 {
		return nil
	}
	var total uint64
	if vm, err := mem.VirtualMemory(); err == nil && vm != nil {
		total = vm.Total
	}
	pids := []memProc{{"still-ui", os.Getpid()}}
	if p := XrayProcess(); p != nil {
		if pid := p.PID(); pid > 0 {
			pids = append(pids, memProc{"Xray", pid})
		}
	}
	usages := collectMemory(pids, total)
	var checks []health.Check
	for _, u := range usages {
		pct := health.Percent(u.used, u.limit)
		checks = append(checks, health.Check{
			Key: "mem:" + u.name, Event: string(eventbus.EventProcessMemory), Confirm: 3,
			Problem: u.limit > 0 && pct >= float64(threshold),
			Params: map[string]string{
				"Process": u.name, "Percent": strconv.FormatFloat(pct, 'f', 0, 64),
				"Used": mib(u.used), "Limit": mib(u.limit), "LimitKind": u.limitKind,
				"Threshold": strconv.Itoa(threshold),
			},
		})
	}
	return checks
}

// memProc is a process whose memory is watched.
type memProc struct {
	name string
	pid  int
}

func collectMemory(pids []memProc, total uint64) []memUsage {
	var out []memUsage
	byGroup := map[string]int{}
	for _, p := range pids {
		raw, _ := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(p.pid), "cgroup"))
		if cg, ok := health.CgroupPath(string(raw)); ok {
			dir := filepath.Join(cgroupRoot, cg)
			maxRaw, _ := os.ReadFile(filepath.Join(dir, "memory.max"))
			if limit := health.ParseMemoryMax(string(maxRaw)); limit > 0 {
				if i, dup := byGroup[cg]; dup {
					out[i].name += " + " + p.name
					continue
				}
				curRaw, _ := os.ReadFile(filepath.Join(dir, "memory.current"))
				cur, _ := strconv.ParseUint(strings.TrimSpace(string(curRaw)), 10, 64)
				stat, _ := os.ReadFile(filepath.Join(dir, "memory.stat"))
				byGroup[cg] = len(out)
				out = append(out, memUsage{name: p.name, used: health.WorkingSet(cur, string(stat)), limit: limit, limitKind: "MemoryMax"})
				continue
			}
		}
		if rss := processRSS(p.pid); rss > 0 {
			out = append(out, memUsage{name: p.name, used: rss, limit: total, limitKind: "RAM"})
		}
	}
	return out
}

// processRSS reads VmRSS from /proc/<pid>/status.
func processRSS(pid int) uint64 {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				kb, _ := strconv.ParseUint(fields[0], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}

// diskChecks looks at the file systems holding the root, the database and the backups.
func diskChecks(threshold int) []health.Check {
	if threshold <= 0 {
		return nil
	}
	paths := []string{config.GetDBFolderPath(), config.GetBackupFolderPath()}
	if runtime.GOOS != "windows" {
		paths = append([]string{"/"}, paths...)
	}
	seen := map[string]bool{}
	var checks []health.Check
	for _, p := range paths {
		// A backup folder that does not exist yet lives on its parent's file system.
		for p != "" {
			if _, err := os.Stat(p); err == nil {
				break
			}
			parent := filepath.Dir(p)
			if parent == p {
				break
			}
			p = parent
		}
		u, err := disk.Usage(p)
		if err != nil || u == nil || u.Total == 0 {
			continue
		}
		id := fmt.Sprintf("%s/%d/%d", u.Fstype, u.Total, u.InodesTotal)
		if seen[id] {
			continue
		}
		seen[id] = true
		checks = append(checks, health.Check{
			Key: "disk:" + p, Event: string(eventbus.EventDiskHigh),
			Problem: u.UsedPercent >= float64(threshold),
			Params: map[string]string{
				"Path": p, "Percent": strconv.FormatFloat(u.UsedPercent, 'f', 0, 64),
				"Free": mib(u.Free), "Threshold": strconv.Itoa(threshold),
			},
		})
	}
	return checks
}

// certChecks covers the panel's and the subscription's certificate files and what every
// TLS/Hysteria inbound actually serves (from the certificate watcher).
func (s *HealthAlertService) certChecks(now time.Time, days int) []health.Check {
	if days <= 0 {
		return nil
	}
	var checks []health.Check
	add := func(key, where, subject string, notBefore, notAfter time.Time) {
		left := notAfter.Sub(now)
		checks = append(checks, health.Check{
			Key: key, Event: string(eventbus.EventCertExpiring),
			Problem: health.CertExpiring(notBefore, notAfter, now, days),
			Params: map[string]string{
				"Name": subject, "Where": where,
				"Days": strconv.Itoa(int(left.Hours() / 24)), "Hours": strconv.Itoa(int(left.Hours())),
				"Date": notAfter.Local().Format("2006-01-02 15:04"),
			},
		})
	}
	files := []struct {
		where string
		get   func() (string, error)
	}{
		{"panel", s.settingService.GetCertFile},
		{"subscription", s.settingService.GetSubCertFile},
	}
	seen := map[string]bool{}
	for _, f := range files {
		path, err := f.get()
		if err != nil || strings.TrimSpace(path) == "" || seen[path] {
			continue
		}
		seen[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		cert, err := health.ParseCertificate(data)
		if err != nil {
			continue
		}
		add("cert:file:"+path, f.where+" ("+path+")", certSubject(cert.Subject.CommonName, cert.DNSNames), cert.NotBefore, cert.NotAfter)
	}
	for _, st := range CertificateStatuses() {
		info := st.Served
		if info == nil || st.State == certwatch.StateRenewed {
			// A renewal the core has not taken yet is the watcher's business, and it restarts the core.
			continue
		}
		add("cert:inbound:"+st.Tag, "inbound "+st.Tag, info.Subject, time.Unix(info.NotBefore, 0), time.Unix(info.NotAfter, 0))
	}
	return checks
}

func certSubject(cn string, dns []string) string {
	if len(dns) > 0 {
		return dns[0]
	}
	return cn
}

// backupCheck reports the last local backup since the panel started, if any ran.
func backupCheck() (health.Check, bool) {
	last := LastLocalBackup()
	if last == nil {
		return health.Check{}, false
	}
	return health.Check{
		Key: "backup", Event: string(eventbus.EventBackupFailed), Problem: last.Error != "",
		Params: map[string]string{"Error": oneLineCut(last.Error, 300), "File": last.File},
	}, true
}

func mib(b uint64) string { return strconv.FormatUint(b>>20, 10) + " MB" }

func oneLineCut(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
