// Command protocol is the protocol-level half of the agent-efficiency
// benchmark. It replays the cheapest correct Huyang call sequence for every
// scenario against a private service on a copy of the fixtures and records
// exact request and response sizes, next to modelled built-in and Bash
// equivalents. See bench/agent-efficiency/README.md.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkoukk/tiktoken-go"
)

// Call is one recorded tool call: what the agent had to send and read.
type Call struct {
	Scenario       string `json:"scenario"`
	Language       string `json:"language"`
	Family         string `json:"family"`
	Step           int    `json:"step"`
	Tool           string `json:"tool"`
	Note           string `json:"note,omitempty"`
	Modelled       bool   `json:"modelled"`
	RequestBytes   int    `json:"request_bytes"`
	RequestTokens  int    `json:"request_tokens"`
	ResponseBytes  int    `json:"response_bytes"`
	ResponseTokens int    `json:"response_tokens"`
	// StructuredBytes is the size of the structured content the MCP reply
	// also carries; harnesses that render it pay for it twice.
	StructuredBytes int    `json:"structured_bytes,omitempty"`
	Outcome         string `json:"outcome,omitempty"`
	Milliseconds    int64  `json:"ms"`
}

// Report is the output of one run.
type Report struct {
	Label     string   `json:"label"`
	Huyang    string   `json:"huyang_version"`
	Tokenizer string   `json:"tokenizer"`
	Calls     []Call   `json:"calls"`
	Notes     []string `json:"notes"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "protocol:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("usage: protocol <run|count|table|compare> ...")
	}
	switch arguments[0] {
	case "count":
		return countCommand(arguments[1:])
	case "run":
		return runCommand(arguments[1:])
	case "table":
		return tableCommand(arguments[1:])
	case "compare":
		return compareCommand(arguments[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", arguments[0])
	}
}

// repoRoot walks up from the working directory to the module root.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			if _, luaErr := os.Stat(filepath.Join(dir, "lua", "huyang")); luaErr == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run from inside the huyang repository")
		}
		dir = parent
	}
}

// tokenizer loads cl100k_base, caching the BPE file under bench/.cache.
func tokenizer(root string) (*tiktoken.Tiktoken, error) {
	cache := filepath.Join(root, "bench", "agent-efficiency", ".cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, err
	}
	if os.Getenv("TIKTOKEN_CACHE_DIR") == "" {
		_ = os.Setenv("TIKTOKEN_CACHE_DIR", cache)
	}
	return tiktoken.GetEncoding("cl100k_base")
}

// countCommand prints the token count of stdin, or with --lines one count
// per JSON-encoded string line.
func countCommand(arguments []string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	encoder, err := tokenizer(root)
	if err != nil {
		return err
	}
	if len(arguments) > 0 && arguments[0] == "--lines" {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 1<<20), 64<<20)
		writer := bufio.NewWriter(os.Stdout)
		for scanner.Scan() {
			var text string
			if err := json.Unmarshal(scanner.Bytes(), &text); err != nil {
				text = scanner.Text()
			}
			fmt.Fprintln(writer, len(encoder.Encode(text, nil, nil)))
			if err := writer.Flush(); err != nil {
				return err
			}
		}
		return scanner.Err()
	}
	content, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	fmt.Println(len(encoder.Encode(string(content), nil, nil)))
	return nil
}

// readReport decodes a run report from a file.
func readReport(path string) (Report, error) {
	var report Report
	content, err := os.ReadFile(path)
	if err != nil {
		return report, err
	}
	return report, json.Unmarshal(content, &report)
}

// aggregate is one scenario/language/family row of the summary table.
type aggregate struct {
	Scenario, Language, Family string
	Calls                      int
	RequestTokens              int
	ResponseTokens             int
	Milliseconds               int64
	Modelled                   bool
}

func aggregateCalls(calls []Call) []aggregate {
	var rows []aggregate
	index := map[string]int{}
	for _, call := range calls {
		key := call.Scenario + "|" + call.Language + "|" + call.Family
		position, ok := index[key]
		if !ok {
			position = len(rows)
			index[key] = position
			rows = append(rows, aggregate{Scenario: call.Scenario, Language: call.Language, Family: call.Family, Modelled: call.Modelled})
		}
		rows[position].Calls++
		rows[position].RequestTokens += call.RequestTokens
		rows[position].ResponseTokens += call.ResponseTokens
		rows[position].Milliseconds += call.Milliseconds
	}
	return rows
}

func tableCommand(arguments []string) error {
	if len(arguments) != 1 {
		return errors.New("usage: protocol table REPORT.json")
	}
	report, err := readReport(arguments[0])
	if err != nil {
		return err
	}
	fmt.Printf("Run %q, Huyang %s, tokenizer %s\n\n", report.Label, report.Huyang, report.Tokenizer)
	fmt.Println("| Scenario | Lang | Family | Calls | Req tokens | Resp tokens | ms |")
	fmt.Println("|---|---|---|---|---|---|---|")
	for _, row := range aggregateCalls(report.Calls) {
		ms := fmt.Sprint(row.Milliseconds)
		if row.Modelled {
			ms = "n/a"
		}
		fmt.Printf("| %s | %s | %s | %d | %d | %d | %s |\n", row.Scenario, row.Language, row.Family, row.Calls, row.RequestTokens, row.ResponseTokens, ms)
	}
	if len(report.Notes) > 0 {
		fmt.Println()
		for _, note := range report.Notes {
			fmt.Printf("- %s\n", note)
		}
	}
	return nil
}

// compareCommand prints the huyang-family delta between two runs.
func compareCommand(arguments []string) error {
	if len(arguments) != 2 {
		return errors.New("usage: protocol compare BEFORE.json AFTER.json")
	}
	before, err := readReport(arguments[0])
	if err != nil {
		return err
	}
	after, err := readReport(arguments[1])
	if err != nil {
		return err
	}
	byKey := map[string]aggregate{}
	for _, row := range aggregateCalls(before.Calls) {
		byKey[row.Scenario+"|"+row.Language+"|"+row.Family] = row
	}
	fmt.Printf("| Scenario | Lang | Family | Calls %s → %s | Req tokens | Resp tokens | ms |\n", before.Label, after.Label)
	fmt.Println("|---|---|---|---|---|---|---|")
	for _, row := range aggregateCalls(after.Calls) {
		old, ok := byKey[row.Scenario+"|"+row.Language+"|"+row.Family]
		if !ok || row.Family != "huyang" {
			continue
		}
		fmt.Printf("| %s | %s | %s | %d → %d | %d → %d (%s) | %d → %d (%s) | %d → %d |\n",
			row.Scenario, row.Language, row.Family, old.Calls, row.Calls,
			old.RequestTokens, row.RequestTokens, percent(old.RequestTokens, row.RequestTokens),
			old.ResponseTokens, row.ResponseTokens, percent(old.ResponseTokens, row.ResponseTokens),
			old.Milliseconds, row.Milliseconds)
	}
	return nil
}

func percent(before, after int) string {
	if before == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%+.0f%%", float64(after-before)*100/float64(before))
}

// firstLine returns the first line of text, for notes.
func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}
