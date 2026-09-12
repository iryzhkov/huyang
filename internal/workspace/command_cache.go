package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The compiler and package caches every verification command shares.
//
// A repository command runs with a throwaway HOME so it can neither read the
// user's configuration nor leave anything behind in it. Caches were part of
// that throwaway home, which meant every stage compiled and downloaded from
// scratch: one verify_run of a Go module paid a cold build twice, 7.7 s
// against 0.06 s warm on the benchmark fixture, and the same is true of every
// other toolchain that keeps a cache under the home or the cache directory.
//
// The caches therefore live here instead: one Huyang-owned directory beside
// the service state, shared by every workspace and every run. It is safe to
// share because each of these caches is addressed by the content or the exact
// version of what it holds, so an entry found in it cannot make a later build
// or check produce a different answer than a cold one would; that is the same
// reasoning that already hands a command the user's GOMODCACHE and CARGO_HOME.
// Package and registry homes still come from the user, so a command can
// resolve dependencies the machine has already fetched; only caches are
// redirected here.
//
// The directory is disposable by construction: deleting it costs the next run
// a cold build and nothing else.
const (
	// commandCacheMaxBytes bounds the whole shared cache. A cache over the
	// bound is removed entirely rather than trimmed: every entry in it is
	// reproducible, so the cheapest correct action is to start again.
	commandCacheMaxBytes = int64(4) << 30
	// commandCacheMaxAge removes a cache nothing has refreshed for this long,
	// so a machine that stopped building a language does not keep its cache
	// for ever.
	commandCacheMaxAge = 30 * 24 * time.Hour
	// commandCachePruneInterval is how often the bound is checked; the marker
	// file's modification time records the last check.
	commandCachePruneInterval = 24 * time.Hour
	commandCacheMarker        = ".huyang-command-cache"
)

var commandCache struct {
	mu   sync.RWMutex
	root string
	set  bool
}

// SetCommandCacheRoot names the directory holding the shared command caches.
// The service calls it once at startup with a directory beside its state, so
// the caches follow the service's --state-dir rather than the environment.
// Without it the root is derived from HUYANG_STATE_DIR or the XDG state home.
func SetCommandCacheRoot(path string) {
	commandCache.mu.Lock()
	defer commandCache.mu.Unlock()
	commandCache.root, commandCache.set = path, true
}

// commandCacheRoot is the configured root, the environment-derived fallback,
// or an empty string when the caches are disabled or no location exists.
func commandCacheRoot() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HUYANG_COMMAND_CACHE"))) {
	case "off", "0", "false":
		return ""
	}
	commandCache.mu.RLock()
	root, set := commandCache.root, commandCache.set
	commandCache.mu.RUnlock()
	if set {
		return root
	}
	base := strings.TrimSpace(os.Getenv("HUYANG_STATE_DIR"))
	if base == "" {
		base = strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				return ""
			}
			base = filepath.Join(home, ".local", "state")
		}
		base = filepath.Join(base, "huyang")
	}
	return filepath.Join(base, "command-cache")
}

// commandCacheDir creates and returns one named cache directory, or an empty
// string when the shared cache is unavailable. A caller that receives an
// empty string keeps whatever throwaway directory it had.
func commandCacheDir(name string) string {
	root := commandCacheRoot()
	if root == "" {
		return ""
	}
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return ""
	}
	return path
}

// sharedCommandCaches are the cache locations a command is given, beyond the
// cache home below. Each entry names a toolchain variable that is not derived
// from XDG_CACHE_HOME on every platform, so naming it explicitly is what makes
// the cache shared on macOS and Windows as well as on Linux. Only caches
// appear here: an install root, where a wrong entry would change what a build
// links against rather than how fast it gets there, is never redirected.
var sharedCommandCaches = map[string]string{
	"GOCACHE":              "go-build",      // Go build cache
	"GOLANGCI_LINT_CACHE":  "golangci-lint", // golangci-lint analysis cache
	"CCACHE_DIR":           "ccache",        // C and C++ compiler cache
	"SCCACHE_DIR":          "sccache",       // Rust and C++ compiler cache
	"ZIG_GLOBAL_CACHE_DIR": "zig",           // Zig build cache
	"npm_config_cache":     "npm",           // npm keeps its cache under the home, not XDG
	"YARN_CACHE_FOLDER":    "yarn",          // Yarn download cache
	"PIP_CACHE_DIR":        "pip",           // pip wheel and HTTP cache
	"UV_CACHE_DIR":         "uv",            // uv download and build cache
	"DENO_DIR":             "deno",          // Deno module cache
	"COMPOSER_CACHE_DIR":   "composer",      // Composer download cache
}

// commandCacheEnvironment returns the cache variables a verification command
// runs with, and the cache home to use in place of the throwaway one. Both
// are empty when the shared cache is unavailable or disabled.
func commandCacheEnvironment() (variables []string, cacheHome string) {
	if commandCacheRoot() == "" {
		return nil, ""
	}
	// The cache home catches every tool that follows the XDG base directory
	// specification without needing a variable of its own.
	cacheHome = commandCacheDir("xdg")
	if cacheHome == "" {
		return nil, ""
	}
	names := make([]string, 0, len(sharedCommandCaches))
	for name := range sharedCommandCaches {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if path := commandCacheDir(sharedCommandCaches[name]); path != "" {
			variables = append(variables, name+"="+path)
		}
	}
	touchCommandCacheMarker()
	return variables, cacheHome
}

func touchCommandCacheMarker() {
	root := commandCacheRoot()
	if root == "" {
		return
	}
	marker := filepath.Join(root, commandCacheMarker)
	now := time.Now()
	if err := os.Chtimes(marker, now, now); err == nil {
		return
	}
	if file, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = file.Close()
	}
}

// PruneCommandCache enforces the bound on the shared command cache. It
// removes the whole cache when it is larger than commandCacheMaxBytes or when
// nothing has used it for commandCacheMaxAge, and does nothing when it was
// checked within commandCachePruneInterval. The service calls it at startup.
// It reports whether the cache was removed.
func PruneCommandCache() (bool, error) {
	root := commandCacheRoot()
	if root == "" {
		return false, nil
	}
	marker := filepath.Join(root, commandCacheMarker)
	info, err := os.Stat(marker)
	switch {
	case err == nil && time.Since(info.ModTime()) > commandCacheMaxAge:
		return true, os.RemoveAll(root)
	case err == nil && time.Since(info.ModTime()) < commandCachePruneInterval:
		return false, nil
	}
	if _, statErr := os.Stat(root); os.IsNotExist(statErr) {
		return false, nil
	}
	over, err := directoryExceeds(root, commandCacheMaxBytes)
	if err != nil {
		return false, err
	}
	if over {
		return true, os.RemoveAll(root)
	}
	touchCommandCacheMarker()
	return false, nil
}

// directoryExceeds reports whether the tree at root holds more than limit
// bytes. It stops walking as soon as the limit is passed, so the cost is
// bounded by the limit and not by the size of the tree.
func directoryExceeds(root string, limit int64) (bool, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		total += info.Size()
		if total > limit {
			return fs.SkipAll
		}
		return nil
	})
	return total > limit, err
}
