package service

// tf-call-v1 correlates original calls locally without exporting their inputs.
// Equal calls in one key epoch have equal MACs; a MAC is not a unique call ID.
// Disabled capture does no canonicalization or key I/O. Enabled capture bounds
// parsing to 1 MiB and 64 nested containers, reads only an explicitly provisioned
// private host key, and never falls back to an unkeyed digest. Errors are fixed
// and are not logged: privacy and tool results take precedence over feedback.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	frictionCorrelationVersion = "tf-call-v1"
	correlationMaxBytes        = 1 << 20
	correlationMaxDepth        = 64
	correlationActiveMaxBytes  = 4096
)

var errCorrelationUnavailable = errors.New("toolfeedback correlation unavailable")
var errCorrelationInput = errors.New("unsupported correlation input")

// All fields are immutable strings, with no reference to input or key bytes.
type frictionCorrelation struct {
	V    string `json:"v"`
	Kind string `json:"kind"`
	KID  string `json:"kid"`
	MAC  string `json:"mac"`
}

type correlationKey struct {
	id     string
	secret [32]byte
}

// loadCorrelationKey never creates or repairs files. active.json requires a
// lowercase 16-byte hex kid; keys/<kid>.key holds exactly 32 raw bytes. Other
// active metadata is ignored. Each call rereads active, so rotation needs no
// restart and an atomic publication can never mix a kid with another key.
// Open directory/file descriptors without following symlinks; validate the
// descriptors actually read, rather than racing Lstat against a later open.
func loadCorrelationKey() (correlationKey, error) {
	var key correlationKey
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return key, errCorrelationUnavailable
		}
		config = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(config) {
		return key, errCorrelationUnavailable
	}
	dir, err := openCorrelationDirectory(filepath.Join(config, "toolfeedback", "correlation"))
	if err != nil {
		return key, errCorrelationUnavailable
	}
	defer unix.Close(dir)
	active, err := readCorrelationFile(dir, "active.json", correlationActiveMaxBytes)
	if err != nil {
		return key, errCorrelationUnavailable
	}
	value, err := parseCorrelationJSON(active)
	object, ok := value.(map[string]any)
	if err != nil || !ok {
		return key, errCorrelationUnavailable
	}
	id, _ := object["kid"].(string)
	if !validCorrelationKeyID(id) {
		return key, errCorrelationUnavailable
	}
	keys, err := unix.Openat(dir, "keys", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return key, errCorrelationUnavailable
	}
	defer unix.Close(keys)
	if !privateCorrelationNode(keys, true) {
		return key, errCorrelationUnavailable
	}
	secret, err := readCorrelationFile(keys, id+".key", len(key.secret))
	if err != nil || len(secret) != len(key.secret) {
		return key, errCorrelationUnavailable
	}
	key.id = id
	copy(key.secret[:], secret)
	return key, nil
}

func validCorrelationKeyID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Ancestors can have ordinary directory permissions; toolfeedback/correlation
// themselves and keys must be owned by this account and exactly 0700. Every
// path component, including config ancestors, is opened with O_NOFOLLOW.
func openCorrelationDirectory(path string) (int, error) {
	if !filepath.IsAbs(path) {
		return -1, errCorrelationUnavailable
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, errCorrelationUnavailable
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator))
	for i, part := range parts {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return -1, errCorrelationUnavailable
		}
		fd = next
		if i >= len(parts)-2 && !privateCorrelationNode(fd, true) {
			unix.Close(fd)
			return -1, errCorrelationUnavailable
		}
	}
	return fd, nil
}

func privateCorrelationNode(fd int, directory bool) bool {
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
		return false
	}
	kind, permissions := uint32(unix.S_IFREG), uint32(0o600)
	if directory {
		kind, permissions = unix.S_IFDIR, 0o700
	}
	mode := uint32(stat.Mode)
	return mode&unix.S_IFMT == kind && mode&0o7777 == permissions
}

func readCorrelationFile(dir int, name string, max int) ([]byte, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errCorrelationUnavailable
	}
	file := os.NewFile(uintptr(fd), "toolfeedback-correlation")
	defer file.Close()
	if !privateCorrelationNode(fd, false) {
		return nil, errCorrelationUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(max)+1))
	if err != nil || len(data) > max {
		return nil, errCorrelationUnavailable
	}
	return data, nil
}

// canonicalCorrelationValue is an independent strict JSON path. In particular,
// strings containing JSON stay strings and duplicate keys never collapse.
func canonicalCorrelationValue(raw []byte) ([]byte, error) {
	value, err := parseCorrelationJSON(raw)
	if err != nil {
		return nil, errCorrelationInput
	}
	return appendCorrelationValue(nil, value), nil
}

func parseCorrelationJSON(raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > correlationMaxBytes || !utf8.Valid(raw) || !validCorrelationUnicode(raw) {
		return nil, errCorrelationInput
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeCorrelationValue(decoder, 0)
	if err != nil {
		return nil, errCorrelationInput
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errCorrelationInput
	}
	return value, nil
}

func decodeCorrelationValue(decoder *json.Decoder, depth int) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, errCorrelationInput
	}
	switch value := token.(type) {
	case json.Delim:
		if depth >= correlationMaxDepth {
			return nil, errCorrelationInput
		}
		switch value {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok {
					return nil, errCorrelationInput
				}
				if _, duplicate := object[key]; duplicate {
					return nil, errCorrelationInput
				}
				child, err := decodeCorrelationValue(decoder, depth+1)
				if err != nil {
					return nil, errCorrelationInput
				}
				object[key] = child
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errCorrelationInput
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				child, err := decodeCorrelationValue(decoder, depth+1)
				if err != nil {
					return nil, errCorrelationInput
				}
				array = append(array, child)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errCorrelationInput
			}
			return array, nil
		}
	case nil, bool, string, json.Number:
		return token, nil
	}
	return nil, errCorrelationInput
}

// encoding/json replaces invalid UTF-16 escape sequences with U+FFFD. Reject
// them first, while preserving legitimate U+FFFD and paired surrogate escapes.
func validCorrelationUnicode(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue // the JSON decoder checks the other escape spellings
		}
		code, ok := correlationHex4(raw[i+1:])
		if !ok {
			return false
		}
		i += 4
		if code >= 0xDC00 && code <= 0xDFFF {
			return false
		}
		if code >= 0xD800 && code <= 0xDBFF {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, ok := correlationHex4(raw[i+3:])
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return !inString
}

func correlationHex4(raw []byte) (uint16, bool) {
	if len(raw) < 4 {
		return 0, false
	}
	var value uint16
	for _, c := range raw[:4] {
		value <<= 4
		switch {
		case c >= '0' && c <= '9':
			value |= uint16(c - '0')
		case c >= 'a' && c <= 'f':
			value |= uint16(c-'a') + 10
		case c >= 'A' && c <= 'F':
			value |= uint16(c-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func appendCorrelationValue(out []byte, value any) []byte {
	switch value := value.(type) {
	case nil:
		return append(out, 'z')
	case bool:
		if value {
			return append(out, 't')
		}
		return append(out, 'f')
	case string:
		out = append(out, 's')
		out = strconv.AppendInt(out, int64(len(value)), 10)
		out = append(out, ':')
		return append(out, value...)
	case json.Number:
		number := normalizeCorrelationNumber(string(value))
		out = append(out, 'n')
		out = strconv.AppendInt(out, int64(len(number)), 10)
		out = append(out, ':')
		return append(out, number...)
	case []any:
		out = append(out, 'a')
		out = strconv.AppendInt(out, int64(len(value)), 10)
		out = append(out, ':')
		for _, child := range value {
			out = appendCorrelationValue(out, child)
		}
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys) // Go strings compare unsigned UTF-8 bytes
		out = append(out, 'o')
		out = strconv.AppendInt(out, int64(len(keys)), 10)
		out = append(out, ':')
		for _, key := range keys {
			out = appendCorrelationValue(out, key)
			out = appendCorrelationValue(out, value[key])
		}
	}
	return out
}

// Input is already a strictly parsed JSON number. Normalize decimal text
// without ever converting its coefficient or exponent to floating point.
// The arbitrary-precision exponent is not expanded into a giant coefficient.
func normalizeCorrelationNumber(number string) string {
	sign := ""
	if strings.HasPrefix(number, "-") {
		sign, number = "-", number[1:]
	}
	mantissa, exponentText := number, "0"
	if i := strings.IndexAny(number, "eE"); i >= 0 {
		mantissa, exponentText = number[:i], number[i+1:]
	}
	fraction := 0
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		fraction = len(mantissa) - i - 1
		mantissa = mantissa[:i] + mantissa[i+1:]
	}
	coefficient := strings.TrimLeft(mantissa, "0")
	if coefficient == "" {
		return "0"
	}
	trimmed := strings.TrimRight(coefficient, "0")
	exponent := new(big.Int)
	exponent.SetString(exponentText, 10) // grammar already verified by JSON
	exponent.Add(exponent, big.NewInt(int64(len(coefficient)-len(trimmed)-fraction)))
	return sign + trimmed + "e" + exponent.String()
}

// canonicalTool is the registered descriptor name, never a suffix guessed from
// a foreign namespace. Transcript aliases are consumer-side and source-scoped.
func correlateMCPCall(canonicalTool string, original json.RawMessage) frictionCorrelation {
	return correlateMCPCallWithKeyLoader(canonicalTool, original, loadCorrelationKey)
}

func correlateMCPCallWithKeyLoader(canonicalTool string, original json.RawMessage, load func() (correlationKey, error)) frictionCorrelation {
	if !frictionEnabled() {
		return frictionCorrelation{}
	}
	arguments, err := canonicalCorrelationValue(original)
	if err != nil {
		return frictionCorrelation{}
	}
	key, err := load()
	if err != nil {
		return frictionCorrelation{}
	}
	message := []byte("a5:")
	for _, field := range []string{frictionCorrelationVersion, "mcp", frictionSource, canonicalTool} {
		message = appendCorrelationValue(message, field)
	}
	message = append(message, arguments...)
	mac := hmac.New(sha256.New, key.secret[:])
	_, _ = mac.Write(message)
	return frictionCorrelation{
		V: frictionCorrelationVersion, Kind: "mcp", KID: key.id,
		MAC: hex.EncodeToString(mac.Sum(nil)),
	}
}
