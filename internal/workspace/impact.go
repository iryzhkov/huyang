package workspace

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

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
