package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const testHistoryVersion = 1

type VariantPolicy struct {
	Name     string   `toml:"name" json:"name"`
	Files    []string `toml:"files" json:"files,omitempty"`
	Required bool     `toml:"required" json:"required,omitempty"`
}

type ImpactPolicy struct {
	MaxFiles int `toml:"max_files" json:"max_files"`
	MaxEdges int `toml:"max_edges" json:"max_edges"`
}

type impactSource struct {
	node    ImpactNode
	content string
}

type ImpactNode struct {
	Path      string `json:"path"`
	Language  string `json:"language,omitempty"`
	Test      bool   `json:"test,omitempty"`
	Generated bool   `json:"generated,omitempty"`
	Config    bool   `json:"config,omitempty"`
}

type ImpactEdge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Kind       string `json:"kind"`
	Adapter    string `json:"adapter"`
	Confidence string `json:"confidence"`
}

type ImpactRisk struct {
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail"`
}

type ImpactGraph struct {
	Revision string   `json:"revision"`
	Changed  []string `json:"changed"`
	// Snapshot and Producers name where these relations came from: which
	// immutable analysis of which revision, and who contributed to it.
	Snapshot      string       `json:"snapshot,omitempty"`
	Producers     []string     `json:"producers,omitempty"`
	Nodes         []ImpactNode `json:"nodes"`
	Edges         []ImpactEdge `json:"edges"`
	Affected      []string     `json:"affected"`
	Untested      []string     `json:"affected_without_tests,omitempty"`
	Risks         []ImpactRisk `json:"risks,omitempty"`
	Coverage      Coverage     `json:"coverage"`
	Adapters      []string     `json:"adapters"`
	Included      []string     `json:"variants_included,omitempty"`
	Omitted       []string     `json:"variants_omitted,omitempty"`
	RecommendFull bool         `json:"recommend_full"`
}

type SelectedTest struct {
	Name     string   `json:"name"`
	Command  []string `json:"command"`
	Reasons  []string `json:"reasons"`
	Variants []string `json:"variants,omitempty"`
}

type TargetedTestResult struct {
	Status   string         `json:"status"`
	Selected []SelectedTest `json:"selected,omitempty"`
	Executed []string       `json:"executed,omitempty"`
	Graph    ImpactGraph    `json:"graph"`
}

type TestHistoryEntry struct {
	Revision   string   `json:"revision"`
	Scope      string   `json:"scope"`
	Test       string   `json:"test"`
	Variants   []string `json:"variants,omitempty"`
	Status     string   `json:"status"`
	DurationMS int64    `json:"duration_ms"`
	RecordedAt string   `json:"recorded_at"`
}

type testHistoryState struct {
	Version int                `json:"version"`
	Entries []TestHistoryEntry `json:"entries"`
}

var (
	goImportPattern     = regexp.MustCompile(`(?m)^\s*import\s+(?:[A-Za-z0-9_.]+\s+)?["]([^"]+)["]`)
	goBlockPattern      = regexp.MustCompile(`(?m)^\s*(?:[A-Za-z0-9_.]+\s+)?["]([^"]+)["]`)
	jsImportPattern     = regexp.MustCompile(`(?m)(?:from\s+|require\s*\(\s*|import\s*\(\s*)["']([^"']+)["']`)
	pythonImportPattern = regexp.MustCompile(`(?m)^\s*(?:from\s+([A-Za-z0-9_.]+)\s+import|import\s+([A-Za-z0-9_.]+))`)
	luaImportPattern    = regexp.MustCompile(`require\s*\(?\s*["']([^"']+)["']`)
)

func defaultImpactPolicy(policy ImpactPolicy) ImpactPolicy {
	if policy.MaxFiles <= 0 {
		policy.MaxFiles = 2000
	}
	if policy.MaxEdges <= 0 {
		policy.MaxEdges = 5000
	}
	return policy
}

func BuildImpactGraph(root, revision string, changed []string, policy ImpactPolicy, variants []VariantPolicy) (ImpactGraph, error) {
	policy = defaultImpactPolicy(policy)
	graph := ImpactGraph{Revision: revision, Changed: uniqueSorted(changed)}
	sources, walkErr := collectImpactSources(root, policy, &graph)
	if walkErr != nil {
		return graph, walkErr
	}
	linkImpactSources(sources, policy, &graph)
	if graph.Coverage.Capped {
		graph.Coverage.Skipped = append(graph.Coverage.Skipped, "impact_graph_cap")
		graph.Risks = append(graph.Risks, ImpactRisk{Kind: "graph_cap", Detail: "file or edge cap reached"})
	}
	graph.Affected = impactClosure(graph.Changed, graph.Edges)
	graph.Untested = untestedAffected(graph.Affected, sources)
	for _, variant := range variants {
		if variantApplies(variant, graph.Affected) {
			graph.Included = append(graph.Included, variant.Name)
			continue
		}
		graph.Omitted = append(graph.Omitted, variant.Name)
		if variant.Required {
			graph.Risks = append(graph.Risks, ImpactRisk{Kind: "variant_omitted", Detail: variant.Name})
		}
	}
	sort.Slice(graph.Nodes, func(i, j int) bool { return graph.Nodes[i].Path < graph.Nodes[j].Path })
	sort.Slice(graph.Edges, func(i, j int) bool {
		if graph.Edges[i].From == graph.Edges[j].From {
			return graph.Edges[i].To < graph.Edges[j].To
		}
		return graph.Edges[i].From < graph.Edges[j].From
	})
	graph.Adapters = adaptersFor(graph.Nodes)
	graph.Untested = uniqueSorted(graph.Untested)
	graph.Included = uniqueSorted(graph.Included)
	graph.Omitted = uniqueSorted(graph.Omitted)
	graph.Coverage.Complete = !graph.Coverage.Capped && len(graph.Risks) == 0
	graph.RecommendFull = !graph.Coverage.Complete || len(graph.Untested) > 0 || len(graph.Omitted) > 0
	return graph, nil
}

// collectImpactSources reads every source file under root up to the file cap, recording
// coverage and the risks that make a static graph incomplete.
func collectImpactSources(root string, policy ImpactPolicy, graph *ImpactGraph) (map[string]impactSource, error) {
	sources := map[string]impactSource{}
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			graph.Risks = append(graph.Risks, ImpactRisk{Kind: "unreadable", Detail: err.Error()})
			graph.Coverage.Complete = false
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		graph.Coverage.FilesConsidered++
		if len(sources) >= policy.MaxFiles {
			graph.Coverage.Capped = true
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		language := impactLanguage(relative)
		node := ImpactNode{Path: relative, Language: language, Test: isTestPath(relative), Config: isConfigPath(relative)}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			graph.Risks = append(graph.Risks, ImpactRisk{Kind: "unreadable", Path: relative, Detail: readErr.Error()})
			return nil
		}
		graph.Coverage.FilesRead++
		graph.Coverage.BytesRead += int64(len(content))
		node.Generated = generatedContent(content)
		if node.Generated {
			graph.Risks = append(graph.Risks, ImpactRisk{Kind: "generated_code", Path: relative, Detail: "generated dependencies may not match source-time edges"})
		}
		if node.Config {
			graph.Risks = append(graph.Risks, ImpactRisk{Kind: "configuration", Path: relative, Detail: "configuration/schema effects are not statically complete"})
		}
		text := string(content)
		if dynamicContent(language, text) {
			graph.Risks = append(graph.Risks, ImpactRisk{Kind: "dynamic_or_reflection", Path: relative, Detail: "reflection or dynamic loading prevents a complete static graph"})
		}
		sources[relative] = impactSource{node: node, content: text}
		return nil
	})
	return sources, walkErr
}

// linkImpactSources adds every source as a node and every resolvable import as an edge,
// up to the edge cap.
func linkImpactSources(sources map[string]impactSource, policy ImpactPolicy, graph *ImpactGraph) {
	for _, item := range sources {
		graph.Nodes = append(graph.Nodes, item.node)
		for _, imported := range impactImports(item.node.Language, item.content) {
			target, confidence := resolveImpactTarget(item.node.Path, imported, sources)
			if target == "" {
				graph.Risks = append(graph.Risks, ImpactRisk{Kind: "unresolved_dependency", Path: item.node.Path, Detail: imported})
				continue
			}
			if len(graph.Edges) >= policy.MaxEdges {
				graph.Coverage.Capped = true
				break
			}
			graph.Edges = append(graph.Edges, ImpactEdge{From: item.node.Path, To: target, Kind: "imports", Adapter: item.node.Language, Confidence: confidence})
		}
	}
}

// untestedAffected lists affected non-test sources whose directory holds no affected test.
func untestedAffected(affected []string, sources map[string]impactSource) []string {
	testDirs := map[string]bool{}
	for _, path := range affected {
		if node, ok := sources[path]; ok && node.node.Test {
			testDirs[filepath.ToSlash(filepath.Dir(path))] = true
		}
	}
	var untested []string
	for _, path := range affected {
		node, ok := sources[path]
		if ok && !node.node.Test && !testDirs[filepath.ToSlash(filepath.Dir(path))] {
			untested = append(untested, path)
		}
	}
	return untested
}

func SelectAffectedTests(graph ImpactGraph, tests []CommandPolicy, history []TestHistoryEntry) []SelectedTest {
	var selected []SelectedTest
	for index, test := range tests {
		reasons := []string{}
		for _, pattern := range test.Covers {
			for _, path := range graph.Affected {
				if pathPattern(pattern, path) {
					reasons = append(reasons, "affected_package")
					break
				}
			}
		}
		for _, variant := range test.Variants {
			if containsString(graph.Included, variant) {
				reasons = append(reasons, "configured_variant")
			}
		}
		if test.Required {
			reasons = append(reasons, "configured_required_suite")
		}
		for _, prior := range history {
			if prior.Test == test.Name && prior.Status == string(VerificationFailed) {
				reasons = append(reasons, "prior_failure")
				break
			}
		}
		if len(test.Covers) == 0 && len(test.Variants) == 0 {
			for _, path := range graph.Affected {
				if isTestPath(path) {
					reasons = append(reasons, "colocated_test")
					break
				}
			}
		}
		if len(reasons) == 0 {
			continue
		}
		name := test.Name
		if name == "" {
			name = "test_" + strconv.Itoa(index+1)
		}
		selected = append(selected, SelectedTest{Name: name, Command: append([]string(nil), test.Command...), Reasons: uniqueSorted(reasons), Variants: append([]string(nil), test.Variants...)})
	}
	return selected
}

func runAffectedTests(ctx context.Context, sandbox *Sandbox, policy PipelinePolicy, request VerificationRequest, affected []string) ([]VerificationStage, *TargetedTestResult, error) {
	// The impact model is now derived from a snapshot: the import reader is
	// one contributor to it, and the graph carries the snapshot it came from
	// so a later answer can say which analysis it trusted.
	_, graph, err := AnalyzeImpact(ctx, AnalysisRequest{
		Key: AnalysisKey{
			WorkspaceID: request.WorkspaceID, Epoch: request.ProviderEpoch, Revision: request.Revision,
			Profile: "impact/v1", ConfigHash: PipelineFingerprint(policy),
		},
		Root: sandbox.Tree, Changed: affected,
	}, policy.Impact, policy.Variants, request.Contributors...)
	if err != nil {
		return nil, nil, err
	}
	priorHistory, err := readTestHistory(request.TestHistoryPath)
	if err != nil {
		return nil, nil, err
	}
	selected := SelectAffectedTests(graph, policy.Tests, priorHistory)
	targeted := &TargetedTestResult{Status: "unavailable", Selected: selected, Graph: graph}
	if len(selected) == 0 {
		stage := VerificationStage{
			Stage: "tests", Mode: "check", StartedRevision: request.Revision, Exit: -1,
			Status: VerificationSkipped, TestScope: "affected", TestVerdict: "affected_tests_unavailable",
			Coverage: graph.Coverage,
		}
		stage.Coverage.Complete = false
		stage.Coverage.Skipped = append(stage.Coverage.Skipped, "no_affected_test_association")
		return []VerificationStage{stage}, targeted, nil
	}
	var stages []VerificationStage
	var history []TestHistoryEntry
	targeted.Status = "affected_tests_passed"
	for _, test := range selected {
		command := CommandPolicy{Name: test.Name, Command: test.Command}
		stage, _, runErr := commandStage(ctx, sandbox.Tree, request.Revision, "tests", "check", command, policy, false)
		stage.TestScope = "affected"
		stage.SelectedTests = []SelectedTest{test}
		stage.ExecutedTests = []string{test.Name}
		stage.TestVerdict = "affected_tests_passed"
		stage.Coverage = graph.Coverage
		stage.Coverage.Complete = graph.Coverage.Complete && len(graph.Untested) == 0 && len(graph.Omitted) == 0
		stages = append(stages, stage)
		targeted.Executed = append(targeted.Executed, test.Name)
		history = append(history, historyEntry(request.Revision, "affected", test.Name, test.Variants, stage))
		if runErr != nil {
			targeted.Status = "affected_tests_failed"
			stages[len(stages)-1].TestVerdict = targeted.Status
			_ = recordTestHistory(request.TestHistoryPath, history)
			return stages, targeted, runErr
		}
	}
	if err := recordTestHistory(request.TestHistoryPath, history); err != nil {
		return stages, targeted, err
	}
	return stages, targeted, nil
}

func readTestHistory(path string) ([]TestHistoryEntry, error) {
	if path == "" {
		return nil, nil
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state testHistoryState
	if err := json.Unmarshal(content, &state); err != nil {
		return nil, err
	}
	if state.Version != testHistoryVersion {
		return nil, errors.New("unsupported test history version")
	}
	return append([]TestHistoryEntry(nil), state.Entries...), nil
}

func recordTestHistory(path string, entries []TestHistoryEntry) error {
	if path == "" || len(entries) == 0 {
		return nil
	}
	var state testHistoryState
	content, err := os.ReadFile(path)
	if err == nil {
		if decodeErr := json.Unmarshal(content, &state); decodeErr != nil {
			return decodeErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state.Version = testHistoryVersion
	state.Entries = append(state.Entries, entries...)
	if len(state.Entries) > 1000 {
		state.Entries = state.Entries[len(state.Entries)-1000:]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content, err = json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, append(content, '\n'), 0o600)
}

func historyEntry(revision, scope, name string, variants []string, stage VerificationStage) TestHistoryEntry {
	return TestHistoryEntry{Revision: revision, Scope: scope, Test: name, Variants: append([]string(nil), variants...), Status: string(stage.Status), DurationMS: stage.DurationMS, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}
}

func impactLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return "typescript"
	case ".py":
		return "python"
	case ".lua":
		return "lua"
	default:
		return ""
	}
}

func impactImports(language, content string) []string {
	var result []string
	switch language {
	case "go":
		for _, match := range goImportPattern.FindAllStringSubmatch(content, -1) {
			result = append(result, match[1])
		}
		if start := strings.Index(content, "import ("); start >= 0 {
			if end := strings.Index(content[start:], ")"); end >= 0 {
				for _, match := range goBlockPattern.FindAllStringSubmatch(content[start:start+end], -1) {
					result = append(result, match[1])
				}
			}
		}
	case "typescript":
		for _, match := range jsImportPattern.FindAllStringSubmatch(content, -1) {
			result = append(result, match[1])
		}
	case "python":
		for _, match := range pythonImportPattern.FindAllStringSubmatch(content, -1) {
			if match[1] != "" {
				result = append(result, match[1])
			} else {
				result = append(result, match[2])
			}
		}
	case "lua":
		for _, match := range luaImportPattern.FindAllStringSubmatch(content, -1) {
			result = append(result, match[1])
		}
	}
	return uniqueSorted(result)
}

func resolveImpactTarget(from, imported string, sources map[string]impactSource) (string, string) {
	candidates := []string{}
	base := filepath.ToSlash(filepath.Dir(from))
	if strings.HasPrefix(imported, ".") {
		clean := filepath.ToSlash(filepath.Clean(filepath.Join(base, imported)))
		candidates = append(candidates, clean, clean+".ts", clean+".tsx", clean+".js", clean+".py", clean+".lua", clean+"/index.ts", clean+"/index.js")
		switch strings.ToLower(filepath.Ext(clean)) {
		case ".js":
			stem := strings.TrimSuffix(clean, filepath.Ext(clean))
			candidates = append(candidates, stem+".ts", stem+".tsx")
		case ".jsx":
			stem := strings.TrimSuffix(clean, filepath.Ext(clean))
			candidates = append(candidates, stem+".tsx", stem+".ts")
		case ".mjs":
			stem := strings.TrimSuffix(clean, filepath.Ext(clean))
			candidates = append(candidates, stem+".mts")
		case ".cjs":
			stem := strings.TrimSuffix(clean, filepath.Ext(clean))
			candidates = append(candidates, stem+".cts")
		}
	} else {
		module := strings.ReplaceAll(imported, ".", "/")
		candidates = append(candidates, module+".py", module+".lua", module+"/__init__.py", imported+".go")
	}
	for _, candidate := range candidates {
		if _, ok := sources[candidate]; ok {
			return candidate, "exact"
		}
	}
	for path := range sources {
		dir := filepath.ToSlash(filepath.Dir(path))
		if strings.HasSuffix(imported, "/"+filepath.Base(dir)) || imported == filepath.Base(dir) {
			return path, "package"
		}
	}
	return "", ""
}

func impactClosure(changed []string, edges []ImpactEdge) []string {
	seen := map[string]bool{}
	queue := append([]string(nil), changed...)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if seen[current] {
			continue
		}
		seen[current] = true
		for _, edge := range edges {
			if edge.To == current && !seen[edge.From] {
				queue = append(queue, edge.From)
			}
		}
	}
	result := make([]string, 0, len(seen))
	for path := range seen {
		result = append(result, path)
	}
	return uniqueSorted(result)
}

func generatedContent(content []byte) bool {
	lower := strings.ToLower(string(content))
	return strings.Contains(lower, "code generated") && strings.Contains(lower, "do not edit")
}

func dynamicContent(language, content string) bool {
	switch language {
	case "go":
		return strings.Contains(content, "reflect.") || strings.Contains(content, "plugin.Open(")
	case "typescript":
		return strings.Contains(content, "import(") || strings.Contains(content, "require(variable")
	case "python":
		return strings.Contains(content, "getattr(") || strings.Contains(content, "importlib.") || strings.Contains(content, "eval(")
	case "lua":
		return strings.Contains(content, "load(") || strings.Contains(content, "loadstring(")
	}
	return false
}

func isTestPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, "_test.go") || strings.Contains(lower, ".test.") || strings.Contains(lower, ".spec.") || strings.HasPrefix(filepath.Base(lower), "test_")
}

func isConfigPath(path string) bool {
	lower := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".toml" || ext == ".yaml" || ext == ".yml" || ext == ".json" || lower == "go.mod" || strings.HasPrefix(lower, "tsconfig")
}

func variantApplies(variant VariantPolicy, affected []string) bool {
	if len(variant.Files) == 0 {
		return true
	}
	for _, pattern := range variant.Files {
		for _, path := range affected {
			if pathPattern(pattern, path) {
				return true
			}
		}
	}
	return false
}

func pathPattern(pattern, path string) bool {
	pattern, path = filepath.ToSlash(pattern), filepath.ToSlash(path)
	if pattern == "**" || pattern == "**/*" {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "**"))
	}
	matched, _ := filepath.Match(pattern, path)
	if matched {
		return true
	}
	matched, _ = filepath.Match(pattern, filepath.Base(path))
	return matched
}

func adaptersFor(nodes []ImpactNode) []string {
	var adapters []string
	for _, node := range nodes {
		if node.Language != "" {
			adapters = append(adapters, node.Language)
		}
	}
	return uniqueSorted(adapters)
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
