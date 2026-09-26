package service

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func writeSpoolDay(t *testing.T, dir, name string, size int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func spoolNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func TestFrictionRetentionRemovesOnlyThisHostsExpiredDays(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	day := func(daysAgo int) string { return now.AddDate(0, 0, -daysAgo).Format(frictionDayLayout) }
	for _, name := range []string{
		"box-" + day(100) + ".jsonl",     // this host, past retention
		"box-" + day(10) + ".jsonl",      // this host, kept
		"box-" + day(0) + ".jsonl",       // today, kept
		"other-" + day(100) + ".jsonl",   // pulled from another host
		"box-pc-" + day(100) + ".jsonl",  // another host whose name extends this one
		"box-" + day(100) + ".jsonl.bak", // not a day file
	} {
		writeSpoolDay(t, dir, name, 10)
	}
	pruneFrictionDir(dir, "box", now, 90*24*time.Hour, 1<<20)
	want := []string{
		"box-" + day(0) + ".jsonl", "box-" + day(10) + ".jsonl",
		"box-" + day(100) + ".jsonl.bak", "box-pc-" + day(100) + ".jsonl", "other-" + day(100) + ".jsonl",
	}
	sort.Strings(want)
	if got := spoolNames(t, dir); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("spool after retention = %v, want %v", got, want)
	}
}

func TestFrictionRetentionCapsSizeOldestFirstAndKeepsToday(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	day := func(daysAgo int) string { return now.AddDate(0, 0, -daysAgo).Format(frictionDayLayout) }
	for daysAgo := 0; daysAgo < 4; daysAgo++ {
		writeSpoolDay(t, dir, "box-"+day(daysAgo)+".jsonl", 100)
	}
	pruneFrictionDir(dir, "box", now, 90*24*time.Hour, 250)
	want := []string{"box-" + day(1) + ".jsonl", "box-" + day(0) + ".jsonl"}
	sort.Strings(want)
	if got := spoolNames(t, dir); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("spool after size cap = %v, want %v", got, want)
	}
	pruneFrictionDir(dir, "box", now, 90*24*time.Hour, 1)
	if got := spoolNames(t, dir); len(got) != 1 || got[0] != "box-"+day(0)+".jsonl" {
		t.Fatalf("size cap removed today's file or kept an older one: %v", got)
	}
}

func TestFrictionRetentionRunsWhenTheDayFileRotates(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("HUYANG_FRICTION_DIR", spool)
	t.Setenv("HUYANG_FRICTION", "1")
	t.Setenv("HUYANG_FRICTION_RETENTION_DAYS", "5")
	frictionIdent()
	host := frictionHost
	if host == "" {
		host = "unknown"
	}
	dir := filepath.Join(spool, frictionSource)
	expired := host + "-" + time.Now().UTC().AddDate(0, 0, -6).Format(frictionDayLayout) + ".jsonl"
	recent := host + "-" + time.Now().UTC().AddDate(0, 0, -4).Format(frictionDayLayout) + ".jsonl"
	writeSpoolDay(t, dir, expired, 10)
	writeSpoolDay(t, dir, recent, 10)

	frictionPruneMu.Lock()
	frictionPrunedOn = ""
	frictionPruneMu.Unlock()
	logFriction("", "read", "", nil, map[string]any{"outcome": "ok"}, time.Now())

	if _, err := os.Stat(filepath.Join(dir, expired)); !os.IsNotExist(err) {
		t.Fatalf("expired day file survived the first write of a new day: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, recent)); err != nil {
		t.Fatalf("day file within retention was removed: %v", err)
	}

	// Within the same day, a later write does not scan the directory again.
	writeSpoolDay(t, dir, expired, 10)
	logFriction("", "read", "", nil, map[string]any{"outcome": "ok"}, time.Now())
	if _, err := os.Stat(filepath.Join(dir, expired)); err != nil {
		t.Fatalf("retention ran again before the day rotated: %v", err)
	}
}

func TestFrictionRetentionDaysSetting(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"": 90 * 24 * time.Hour, "30": 30 * 24 * time.Hour, "0": 90 * 24 * time.Hour, "-3": 90 * 24 * time.Hour, "soon": 90 * 24 * time.Hour,
	} {
		t.Setenv("HUYANG_FRICTION_RETENTION_DAYS", value)
		if got := frictionRetention(); got != want {
			t.Errorf("HUYANG_FRICTION_RETENTION_DAYS=%q gives %s, want %s", value, got, want)
		}
	}
}
