package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"golang.org/x/sys/unix"
)

// All enabled producer tests isolate both key and spool files under this
// scratch tree. The fixture key is public synthetic test material, never an
// operational key. These helpers never consult the account's actual key store.
func correlationScratch(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(home, ".cache", "huyang-friction", "m3-producer")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "t-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func provisionCorrelationTestKey(t *testing.T) (string, correlationKey) {
	t.Helper()
	config := correlationScratch(t)
	t.Setenv("XDG_CONFIG_HOME", config)
	key := correlationKey{id: "00112233445566778899aabbccddeeff"}
	for i := range key.secret {
		key.secret[i] = byte(i)
	}
	writeCorrelationTestKey(t, config, key)
	return config, key
}

func writeCorrelationTestKey(t *testing.T, config string, key correlationKey) {
	t.Helper()
	dir := filepath.Join(config, "toolfeedback", "correlation")
	if err := os.MkdirAll(filepath.Join(dir, "keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", key.id+".key"), key.secret[:], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "active.json"), []byte("{\"kid\":\""+key.id+"\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCorrelationSharedCanonicalVectors(t *testing.T) {
	var fixture struct {
		Protocol, Kind, Source, Tool string
		PublicKeyHex                 string `json:"public_test_key_hex"`
		PublicKID                    string `json:"public_test_kid"`
		MCPPrefix                    string `json:"mcp_prefix"`
		Vectors                      []struct {
			Name      string
			Input     string `json:"input_json"`
			Canonical string
		}
		Invalid []string `json:"invalid_json"`
	}
	data, err := os.ReadFile("testdata/friction-correlation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	config, key := provisionCorrelationTestKey(t)
	secret, err := hex.DecodeString(fixture.PublicKeyHex)
	if err != nil || len(secret) != 32 {
		t.Fatal("bad public fixture key")
	}
	key.id = fixture.PublicKID
	copy(key.secret[:], secret)
	writeCorrelationTestKey(t, config, key)
	t.Setenv("HUYANG_FRICTION_DIR", correlationScratch(t))
	t.Setenv("HUYANG_FRICTION", "1")
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			canonical, err := canonicalCorrelationValue([]byte(vector.Input))
			if err != nil || string(canonical) != vector.Canonical {
				t.Fatalf("canonical bytes = %x, want %x; err=%v", canonical, vector.Canonical, err)
			}
			// Use the independently specified literal prefix and canonical
			// fixture bytes, not the producer's envelope encoder.
			expected := hmac.New(sha256.New, secret)
			expected.Write([]byte(fixture.MCPPrefix + vector.Canonical))
			got := correlateMCPCall(fixture.Tool, json.RawMessage(vector.Input))
			want := frictionCorrelation{V: fixture.Protocol, Kind: fixture.Kind, KID: key.id, MAC: hex.EncodeToString(expected.Sum(nil))}
			if got != want {
				t.Fatalf("correlation = %+v, want %+v", got, want)
			}
		})
	}
	for _, input := range fixture.Invalid {
		if _, err := canonicalCorrelationValue([]byte(input)); err == nil {
			t.Fatalf("accepted unsupported JSON %q", input)
		}
	}
}

func TestCorrelationStrictJSONLimitsAndUnicode(t *testing.T) {
	atLimit := []byte("\"" + strings.Repeat("a", correlationMaxBytes-2) + "\"")
	if _, err := canonicalCorrelationValue(atLimit); err != nil {
		t.Fatal("rejected exactly 1 MiB")
	}
	if _, err := canonicalCorrelationValue(append(atLimit, ' ')); err == nil {
		t.Fatal("accepted input over 1 MiB")
	}
	atDepth := strings.Repeat("[", correlationMaxDepth) + "null" + strings.Repeat("]", correlationMaxDepth)
	if _, err := canonicalCorrelationValue([]byte(atDepth)); err != nil {
		t.Fatal("rejected 64 nested containers")
	}
	if _, err := canonicalCorrelationValue([]byte("[" + atDepth + "]")); err == nil {
		t.Fatal("accepted 65 nested containers")
	}
	for _, invalid := range [][]byte{
		nil, []byte(""), {'"', 0xff, '"'}, []byte("\"control\nbyte\""),
		[]byte("{\"nested\":{\"key\":1,\"key\":2}}"), []byte("1e+"), []byte("-Infinity"),
	} {
		if _, err := canonicalCorrelationValue(invalid); err != errCorrelationInput {
			t.Fatalf("unsupported input did not return a fixed error: %v", err)
		}
	}
	// Astral code points, legitimate replacement characters and equivalent
	// escape forms remain exact, without normalizing distinct Unicode text.
	for _, pair := range [][2]string{
		{`"😀"`, `"\uD83D\uDE00"`},
		{`"�"`, `"\ufffd"`},
		{`{"a":1}`, `{"\u0061":1.00e+0}`},
	} {
		a, aErr := canonicalCorrelationValue([]byte(pair[0]))
		b, bErr := canonicalCorrelationValue([]byte(pair[1]))
		if aErr != nil || bErr != nil || !bytes.Equal(a, b) {
			t.Fatal("equivalent escapes or decimal spellings did not agree")
		}
	}
}

func TestCorrelationMissingAndRotatedKeys(t *testing.T) {
	config, key := provisionCorrelationTestKey(t)
	t.Setenv("HUYANG_FRICTION_DIR", correlationScratch(t))
	t.Setenv("HUYANG_FRICTION", "1")
	before := correlateMCPCall("read", json.RawMessage("{}"))
	if before.V == "" {
		t.Fatal("fixture key unavailable")
	}
	rotated := key
	rotated.id = "ffeeddccbbaa99887766554433221100"
	for i := range rotated.secret {
		rotated.secret[i] ^= 0xff
	}
	writeCorrelationTestKey(t, config, rotated)
	after := correlateMCPCall("read", json.RawMessage("{}"))
	if after.KID != rotated.id || before.MAC == after.MAC {
		t.Fatal("key rotation did not select the newly active key")
	}
	if _, err := os.Stat(filepath.Join(config, "toolfeedback", "correlation", "keys", key.id+".key")); err != nil {
		t.Fatal("producer removed retained historical key")
	}
	if err := os.Remove(filepath.Join(config, "toolfeedback", "correlation", "keys", rotated.id+".key")); err != nil {
		t.Fatal(err)
	}
	if got := correlateMCPCall("read", json.RawMessage("{}")); got != (frictionCorrelation{}) {
		t.Fatal("missing active key fell back to another key or an unkeyed hash")
	}
	missing := filepath.Join(correlationScratch(t), "unprovisioned")
	t.Setenv("XDG_CONFIG_HOME", missing)
	if _, err := loadCorrelationKey(); err != errCorrelationUnavailable {
		t.Fatalf("missing key error = %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("key reader created the missing store")
	}
}

func TestCorrelationUsesExplicitXDGOrDefaultConfigOnly(t *testing.T) {
	config, key := provisionCorrelationTestKey(t)
	// Ignore private fields in active metadata; only kid selects a key.
	active := filepath.Join(config, "toolfeedback", "correlation", "active.json")
	mustCorrelationWrite(t, active, []byte("{\"kid\":\""+key.id+"\",\"v\":\"tf-call-v1\",\"metadata\":\"not-exported\"}"))
	loaded, err := loadCorrelationKey()
	if err != nil || loaded != key {
		t.Fatal("explicit XDG key unavailable")
	}
	home := correlationScratch(t)
	writeCorrelationTestKey(t, filepath.Join(home, ".config"), key)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	loaded, err = loadCorrelationKey()
	if err != nil || loaded != key {
		t.Fatal("default ~/.config key unavailable")
	}
	t.Setenv("XDG_CONFIG_HOME", "relative-config")
	if _, err := loadCorrelationKey(); err != errCorrelationUnavailable {
		t.Fatal("relative config accepted")
	}
}

func TestCorrelationKeyStoreRejectsUnsafeFiles(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, string, string, string)
	}{
		{"active_permissions", func(t *testing.T, dir, active, key string) { mustCorrelationChmod(t, active, 0o644) }},
		{"key_permissions", func(t *testing.T, dir, active, key string) { mustCorrelationChmod(t, key, 0o644) }},
		{"directory_permissions", func(t *testing.T, dir, active, key string) { mustCorrelationChmod(t, dir, 0o755) }},
		{"keys_permissions", func(t *testing.T, dir, active, key string) {
			mustCorrelationChmod(t, filepath.Join(dir, "keys"), 0o755)
		}},
		{"parent_permissions", func(t *testing.T, dir, active, key string) { mustCorrelationChmod(t, filepath.Dir(dir), 0o755) }},
		{"short_key", func(t *testing.T, dir, active, key string) { mustCorrelationWrite(t, key, []byte("short")) }},
		{"long_key", func(t *testing.T, dir, active, key string) { mustCorrelationWrite(t, key, make([]byte, 33)) }},
		{"oversized_active", func(t *testing.T, dir, active, key string) {
			mustCorrelationWrite(t, active, bytes.Repeat([]byte(" "), correlationActiveMaxBytes+1))
		}},
		{"invalid_kid", func(t *testing.T, dir, active, key string) {
			mustCorrelationWrite(t, active, []byte("{\"kid\":\"../private\"}"))
		}},
		{"duplicate_kid", func(t *testing.T, dir, active, key string) {
			mustCorrelationWrite(t, active, []byte("{\"kid\":\"a\",\"kid\":\"b\"}"))
		}},
		{"active_symlink", func(t *testing.T, dir, active, key string) { replaceCorrelationTestSymlink(t, active) }},
		{"key_symlink", func(t *testing.T, dir, active, key string) { replaceCorrelationTestSymlink(t, key) }},
		{"keys_symlink", func(t *testing.T, dir, active, key string) {
			replaceCorrelationTestSymlink(t, filepath.Join(dir, "keys"))
		}},
		{"store_symlink", func(t *testing.T, dir, active, key string) { replaceCorrelationTestSymlink(t, dir) }},
		{"active_fifo", func(t *testing.T, dir, active, key string) {
			if err := os.Remove(active); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(active, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"foreign_owner", func(t *testing.T, dir, active, key string) {
			// Chaldea's disposable namespace can exercise ownership without
			// accessing any real host file. Other environments may lack chown.
			if err := os.Chown(key, os.Getuid()+1, -1); err != nil {
				t.Skip("cannot create foreign-owned fixture")
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, key := provisionCorrelationTestKey(t)
			dir := filepath.Join(config, "toolfeedback", "correlation")
			test.change(t, dir, filepath.Join(dir, "active.json"), filepath.Join(dir, "keys", key.id+".key"))
			if _, err := loadCorrelationKey(); err != errCorrelationUnavailable {
				t.Fatalf("unsafe key store accepted or returned private details: %v", err)
			}
		})
	}
}

func mustCorrelationWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustCorrelationChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func replaceCorrelationTestSymlink(t *testing.T, path string) {
	t.Helper()
	original := path + ".original"
	if err := os.Rename(path, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, path); err != nil {
		t.Fatal(err)
	}
}

func TestCorrelationDisabledAndUnsupportedInputNeverLoadKeys(t *testing.T) {
	spool := correlationScratch(t)
	t.Setenv("HUYANG_FRICTION_DIR", spool)
	t.Setenv("HUYANG_FRICTION", "0")
	loads := 0
	loader := func() (correlationKey, error) {
		loads++
		return correlationKey{}, errCorrelationUnavailable
	}
	for _, input := range []json.RawMessage{json.RawMessage("{}"), json.RawMessage("{"), nil} {
		got := correlateMCPCallWithKeyLoader("read", input, loader)
		logFriction("", "read", "", nil, nil, time.Now(), got)
		if got != (frictionCorrelation{}) {
			t.Fatal("disabled capture produced metadata")
		}
	}
	if loads != 0 {
		t.Fatal("disabled capture accessed keys")
	}
	if entries, err := os.ReadDir(spool); err != nil || len(entries) != 0 {
		t.Fatalf("disabled capture wrote the spool: %v, %v", entries, err)
	}
	t.Setenv("HUYANG_FRICTION", "1")
	for _, input := range []json.RawMessage{json.RawMessage("{"), json.RawMessage("{\"a\":1,\"a\":2}"), make([]byte, correlationMaxBytes+1)} {
		if got := correlateMCPCallWithKeyLoader("read", input, loader); got != (frictionCorrelation{}) {
			t.Fatal("unsupported input produced metadata")
		}
	}
	if loads != 0 {
		t.Fatal("unsupported inputs accessed keys")
	}
}

func TestCorrelationPreservesOriginalThroughArgumentMutation(t *testing.T) {
	provisionCorrelationTestKey(t)
	spool := correlationScratch(t)
	t.Setenv("HUYANG_FRICTION_DIR", spool)
	t.Setenv("HUYANG_FRICTION", "1")
	original := json.RawMessage("{\"root\":\".\",\"path\":\"private.go\",\"max_lines\":\"1\"}")
	before := append([]byte(nil), original...)
	correlation := correlateMCPCall("read", original)
	arguments, err := mcpapi.DecodeArguments(original)
	if err != nil {
		t.Fatal(err)
	}
	arguments = mcpapi.NormalizeArguments(arguments)
	mcpapi.ApplyArgumentAliases("read", arguments)
	// Simulate M2 root injection/resolution and arbitrary handler mutation.
	arguments["root"] = "/private/injected-root"
	arguments["target"].(map[string]any)["path"] = "changed.go"
	mutated, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if transformed := correlateMCPCall("read", mutated); transformed.MAC == correlation.MAC {
		t.Fatal("normalization, aliasing and roots ceased to distinguish original calls")
	}
	started := time.Date(2026, 10, 17, 8, 0, 0, 123000000, time.UTC)
	logFriction("adapter-random", "read", "", arguments, textResult("ok", false), started, correlation)
	event := singleFrictionToolEvent(t, spool, "read")
	if event.Correlation == nil || *event.Correlation != correlation || event.StartedAt != "2026-10-17T08:00:00.123Z" {
		t.Fatalf("original-call metadata changed: %+v", event)
	}
	if !bytes.Equal(original, before) {
		t.Fatal("correlation changed the original request bytes")
	}
	if event.Session != "adapter-random" || event.TS == "" || event.Proc == "" || event.Seq == 0 {
		t.Fatal("producer identity or completion fields were lost")
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"private.go", "changed.go", "/private/injected-root"} {
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatal("raw original or mutated inputs leaked")
		}
	}
	// Repeated identical calls intentionally have equal MACs, not unique IDs.
	if !reflect.DeepEqual(correlation, correlateMCPCall("read", original)) {
		t.Fatal("identical calls did not correlate deterministically")
	}
}
