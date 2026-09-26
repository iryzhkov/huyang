package service

// Friction spool retention. The spool gains one file per host per day, over
// a megabyte on a busy day, and nothing used to remove any of them.
//
// The reader is toolfeedback (homelab-cli): `report` reads a --since window,
// `ship` pushes lines up to 30 days old to Loki, `pull` copies other hosts'
// day files into this same directory, and its own `prune` drops files older
// than its RETENTION_DAYS of 90. The service therefore keeps its files for
// as long as that reader does by default, so it never deletes a day the
// reader would still count, and it deletes only the files it writes itself:
// this host's. Other hosts' files arrived by `pull` and are the reader's to
// prune. A size cap on top of the age bounds a host that spools far more
// than usual; the current day's file is never removed.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	frictionDayLayout = "2006-01-02"
	// defaultFrictionRetentionDays matches toolfeedback's RETENTION_DAYS.
	defaultFrictionRetentionDays = 90
	// frictionSpoolMaxBytes caps this host's day files together.
	frictionSpoolMaxBytes = 256 << 20
)

var (
	frictionPruneMu  sync.Mutex
	frictionPrunedOn string
)

// frictionRetention is how long this host's day files are kept:
// HUYANG_FRICTION_RETENTION_DAYS when it is a positive number of days, else
// the default.
func frictionRetention() time.Duration {
	days := defaultFrictionRetentionDays
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("HUYANG_FRICTION_RETENTION_DAYS"))); err == nil && value > 0 {
		days = value
	}
	return time.Duration(days) * 24 * time.Hour
}

// frictionDayRotated reports whether day differs from the day the spool was
// last pruned on, and records it, so retention runs once when the day file
// rotates rather than on every call.
func frictionDayRotated(day string) bool {
	frictionPruneMu.Lock()
	defer frictionPruneMu.Unlock()
	if frictionPrunedOn == day {
		return false
	}
	frictionPrunedOn = day
	return true
}

// pruneFrictionSpool applies retention to this host's day files when the
// spool is enabled. The service calls it at startup; writeFriction calls
// the same retention again whenever the day file rotates.
func pruneFrictionSpool(now time.Time) {
	if !frictionEnabled() {
		return
	}
	dir := frictionDir()
	if dir == "" {
		return
	}
	frictionIdent()
	host := frictionHost
	if host == "" {
		host = "unknown"
	}
	frictionDayRotated(now.UTC().Format(frictionDayLayout))
	pruneFrictionDir(filepath.Join(dir, frictionSource), host, now, frictionRetention(), frictionSpoolMaxBytes)
}

// pruneFrictionDir removes host's day files in dir that are older than
// maxAge, then the oldest of the rest while together they exceed maxBytes.
// Only files named exactly <host>-<YYYY-MM-DD>.jsonl are considered, and
// today's file is always kept. Like everything else in the spool, failures
// are dropped in silence.
func pruneFrictionDir(dir, host string, now time.Time, maxAge time.Duration, maxBytes int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type dayFile struct {
		path string
		day  time.Time
		size int64
	}
	today := now.UTC().Format(frictionDayLayout)
	var files []dayFile
	var total int64
	for _, entry := range entries {
		name := entry.Name()
		stamp, ok := strings.CutPrefix(strings.TrimSuffix(name, ".jsonl"), host+"-")
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") || !ok || stamp == today {
			continue
		}
		day, err := time.Parse(frictionDayLayout, stamp)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if day.Before(now.Add(-maxAge)) {
			os.Remove(filepath.Join(dir, name))
			continue
		}
		files = append(files, dayFile{path: filepath.Join(dir, name), day: day, size: info.Size()})
		total += info.Size()
	}
	if current, err := os.Stat(filepath.Join(dir, host+"-"+today+".jsonl")); err == nil {
		total += current.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].day.Before(files[j].day) })
	for _, file := range files {
		if total <= maxBytes {
			break
		}
		if os.Remove(file.path) == nil {
			total -= file.size
		}
	}
}
