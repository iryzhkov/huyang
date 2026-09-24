package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// search dispatches on the request shape: a bounded Git history search, a
// refinement of a frozen result set, or a fresh query over current source.
func (h *Handlers) search(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	if source, ok := arguments["git_history"].(map[string]any); ok {
		return searchHistory(requestID, workspace, source)
	}
	if mode, _ := arguments["mode"].(string); semanticRelations[mode] {
		return h.semanticSearch(ctx, requestID, workspace, mode, arguments)
	}
	if parent, _ := arguments["result_set_handle"].(string); parent != "" {
		return refineSearch(requestID, workspace, parent, arguments)
	}
	return searchSource(requestID, workspace, arguments)
}

func searchHistory(requestID string, workspace *workspacecore.Workspace, source map[string]any) map[string]any {
	fields := make([]string, 0)
	for _, value := range mcpapi.AnySlice(source["fields"]) {
		if field, ok := value.(string); ok {
			fields = append(fields, field)
		}
	}
	query, _ := source["query"].(string)
	ref, _ := source["ref"].(string)
	result, err := workspace.SearchHistory(workspacecore.HistorySearchRequest{
		Query: query, Fields: fields, Ref: ref, Limit: argInt(source, "limit", 20),
	})
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "git_history_search_failed", err)
	}
	noun := "matches"
	if len(result.Hits) == 1 {
		noun = "match"
	}
	// The window, not only the count: limit is how many commits were read,
	// so "0 matches" in a five-commit window is not "not in this history".
	summary := fmt.Sprintf("%d historical %s in the %d most recent commits", len(result.Hits), noun, result.ScannedCommits)
	return mcpapi.Envelope(requestID, workspace, "ok", "", summary, result)
}

func refineSearch(requestID string, workspace *workspacecore.Workspace, parent string, arguments map[string]any) map[string]any {
	refine, _ := arguments["refine"].(map[string]any)
	matched, _ := refine["matched_text"].(map[string]any)
	path, _ := refine["path"].(string)
	literal, _ := matched["literal"].(string)
	regex, _ := matched["regex"].(string)
	result, err := workspace.RefineResultSet(workspacecore.ResultSetID(parent), workspacecore.ResultRefinement{
		Path: path, MatchLiteral: literal, MatchRegex: regex,
	})
	if err != nil {
		var conflict *workspacecore.Conflict
		if errors.As(err, &conflict) {
			return mcpapi.Envelope(requestID, workspace, "conflict", string(conflict.Code), conflict.Error(), map[string]any{"result_set_handle": parent})
		}
		return mcpapi.Failure(requestID, workspace, "search_refinement_failed", err)
	}
	limit := argInt(arguments, "limit", 50)
	includeRanges, _ := arguments["include_ranges"].(bool)
	includeHandles, _ := arguments["include_handles"].(bool)
	hits, truncated := mcpapi.CompactSearchHits(result.Matches, limit, includeRanges, includeHandles, literal)
	data := map[string]any{
		"hits": hits, "returned": len(hits), "total": result.Retained, "result_set": mcpapi.CompactResultSet(&result),
	}
	if !result.Coverage.Complete {
		data["coverage"] = result.Coverage
	}
	envelope := mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("%d matches retained; %d eliminated", result.Retained, result.Eliminated), data)
	if truncated {
		envelope["warnings"] = []string{fmt.Sprintf("response limited to %d of %d retained matches", len(hits), result.Retained)}
		envelope["next"] = []any{map[string]any{"tool": "search", "action": "refine", "result_set_handle": result.Handle}}
	}
	return envelope
}

func searchSource(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	query, _ := arguments["query"].(string)
	mode := workspacecore.SearchMode("literal")
	if requested, _ := arguments["mode"].(string); requested != "" {
		mode = workspacecore.SearchMode(requested)
	}
	// The scope goes into the search rather than over its answer: filtering
	// hits afterwards spends the match cap on files the caller excluded, and
	// freezes them into the result set an all-match replacement rewrites.
	result, err := workspace.Search(workspacecore.SearchRequest{
		Query: query, Mode: mode, Paths: argStrings(arguments["paths"]),
	})
	if err != nil {
		failure := mcpapi.Failure(requestID, workspace, "search_failed", err)
		if mode == workspacecore.SearchRegex {
			failure["code"] = "invalid_regex"
			failure["summary"] = "The regular expression is invalid: " + err.Error()
			failure["next"] = []any{map[string]any{
				"tool": "search", "action": "retry_as_literal", "query": query, "mode": "literal",
			}}
		}
		return failure
	}
	limit := argInt(arguments, "limit", 50)
	includeRanges, _ := arguments["include_ranges"].(bool)
	includeHandles, _ := arguments["include_handles"].(bool)
	hits, truncated := mcpapi.CompactSearchHits(result.Hits, limit, includeRanges, includeHandles, literalEcho(arguments, query))
	contextWarning := attachRequestedContext(workspace, hits, arguments)
	// A capped search stopped collecting; the matches it holds are the bound
	// it stopped at, not the matches in the workspace. Saying "1000 matches"
	// for a hundred thousand is the one number an agent uses to decide
	// whether to refine, so the count says it is a floor and names the bound.
	stoppedAtBound := result.Coverage.Capped && len(result.Hits) >= result.MatchLimit && result.MatchLimit > 0
	summary := fmt.Sprintf("%d matches", len(result.Hits))
	if len(result.Hits) == 1 {
		summary = "1 match"
	}
	if stoppedAtBound {
		summary = fmt.Sprintf("at least %d matches; the search stopped at its %d-match bound", len(result.Hits), result.MatchLimit)
	}
	if !result.Coverage.Complete {
		summary += "; search coverage incomplete"
	}
	data := map[string]any{
		"hits": hits, "returned": len(hits), "total": len(result.Hits),
		"result_set": mcpapi.CompactResultSet(result.ResultSet),
	}
	if stoppedAtBound {
		data["total_is_lower_bound"], data["match_limit"] = true, result.MatchLimit
	}
	if !result.Coverage.Complete {
		data["coverage"] = result.Coverage
	}
	envelope := mcpapi.Envelope(requestID, workspace, "ok", "", summary, data)
	if truncated {
		of := fmt.Sprintf("%d", len(result.Hits))
		if stoppedAtBound {
			of = "at least " + of
		}
		envelope["warnings"] = []string{fmt.Sprintf("response limited to %d of %s matches", len(hits), of)}
		envelope["next"] = []any{map[string]any{"tool": "search", "action": "refine", "result_set_handle": result.ResultSet.Handle}}
		if stoppedAtBound {
			// Refining a frozen set that is itself a truncation of the
			// workspace narrows the wrong thing; a narrower query or a
			// scoped path searches the files this one never reached.
			envelope["next"] = append(envelope["next"].([]any), map[string]any{
				"tool": "search", "action": "narrow_the_query_or_scope_it_with_paths", "query": result.Query,
			})
		}
	} else if !result.Coverage.Complete {
		envelope["next"] = []any{map[string]any{"tool": "read", "action": "read_known_path"}}
	}
	// "0 matches" under a scope that named no file is not an answer about
	// the code: the scope was probably a guessed path.
	if len(result.Scope) > 0 && result.Coverage.FilesConsidered == 0 && result.Coverage.Complete {
		envelope = appendWarning(envelope, fmt.Sprintf("paths %q matched no file in the workspace; a pattern is a path substring or a glob over the path or base name", result.Scope))
	}
	return appendWarning(envelope, contextWarning)
}

// attachRequestedContext attaches the context_lines a search asked for. More
// than mcpapi.MaxSearchContextLines is reduced to that bound rather than
// refused, and the warning it returns says so and where the rest is: a
// caller who wants a wider window around a hit wants read with a line
// window, not a larger block repeated beside every hit.
func attachRequestedContext(workspace *workspacecore.Workspace, hits []map[string]any, arguments map[string]any) string {
	requested := argInt(arguments, "context_lines", 0)
	if requested <= 0 {
		return ""
	}
	attachSearchContext(workspace, hits, min(requested, mcpapi.MaxSearchContextLines))
	if requested <= mcpapi.MaxSearchContextLines {
		return ""
	}
	return fmt.Sprintf("context_lines %d was reduced to %d, the most search attaches to each hit; for a wider window read the file with start_line and end_line around the hit",
		requested, mcpapi.MaxSearchContextLines)
}

// appendWarning adds one warning to an envelope; an empty one is none.
func appendWarning(envelope map[string]any, warning string) map[string]any {
	if warning != "" {
		warnings, _ := envelope["warnings"].([]string)
		envelope["warnings"] = append(warnings, warning)
	}
	return envelope
}

// semanticRelations are the search modes answered by the language server
// through navigate: the query is a symbol name and the hits are semantic.
var semanticRelations = map[string]bool{
	"references": true, "definition": true, "implementation": true, "type_definition": true,
	"incoming_calls": true, "outgoing_calls": true,
}

// semanticSearch answers search with a semantic mode by resolving the query
// as a symbol name and asking the language server for the relation. When
// no server can answer, it falls back to the literal search and says so.
func (h *Handlers) semanticSearch(ctx context.Context, requestID string, workspace *workspacecore.Workspace, relation string, arguments map[string]any) map[string]any {
	query, _ := arguments["query"].(string)
	result := h.navigateProvider(ctx, requestID, workspace, map[string]any{
		"relation": relation, "symbol": query, "paths": arguments["paths"], "search_mode": relation,
	})
	switch result["code"] {
	case "language_server_unavailable", "semantic_provider_start_failed", "semantic_provider_unavailable":
		delete(arguments, "mode")
		fallback := searchSource(requestID, workspace, arguments)
		fallback["warnings"] = append(fallback["warnings"].([]string), fmt.Sprintf("no language server answered %s for %q; these are literal matches", relation, query))
		return fallback
	}
	return result
}

// argStrings reads an array-of-strings argument.
// literalEcho is the text every hit of this search will have matched, which
// is the query itself for a literal search and nothing for a regular
// expression or a semantic mode, where the matched text is what the caller
// does not know yet.
func literalEcho(arguments map[string]any, query string) string {
	switch mode, _ := arguments["mode"].(string); mode {
	case "", "literal":
		return query
	default:
		return ""
	}
}

func argStrings(value any) []string {
	var out []string
	for _, raw := range mcpapi.AnySlice(value) {
		if text, ok := raw.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

// attachSearchContext adds the numbered lines around each hit, the way
// grep -C does, so the agent sees what it found without a read call.
func attachSearchContext(workspace *workspacecore.Workspace, hits []map[string]any, around int) {
	cache := map[string][][]byte{}
	for _, hit := range hits {
		path, _ := hit["path"].(string)
		line, _ := hit["line"].(int)
		lines, known := cache[path]
		if !known {
			read, err := workspace.Read(path)
			if err != nil {
				continue
			}
			lines = bytes.Split(read.Content, []byte("\n"))
			if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
				lines = lines[:len(lines)-1]
			}
			cache[path] = lines
		}
		start, end := max(line-around, 1), min(line+around, len(lines))
		if start > end {
			continue
		}
		var out strings.Builder
		for number := start; number <= end; number++ {
			fmt.Fprintf(&out, "%d\t%s\n", number, lines[number-1])
		}
		hit["context"] = out.String()
	}
}

// providerIncompleteness names why a provider's find_symbol answer does not
// cover everything it was asked about - it said so itself, or its optional
// language-server enrichment is still pending - or returns "" when it does.
// Without it a provider that answered with half its sources was reported as
// complete coverage.
func providerIncompleteness(value map[string]any) string {
	if enrichment, ok := value["enrichment"].(map[string]any); ok && enrichment["status"] == "pending" {
		if reason, _ := enrichment["reason"].(string); reason != "" {
			return "provider_enrichment_pending: " + reason
		}
		return "provider_enrichment_pending"
	}
	if complete, present := value["complete"].(bool); present && !complete {
		return "provider_evidence_incomplete"
	}
	return ""
}

// symbolFind answers from the native text core when it has parser coverage
// and otherwise asks the semantic provider, registering its matches as
// durable symbol handles so later locators can select them.
func (h *Handlers) symbolFind(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	requested, _ := arguments["query"].(string)
	query := canonicalNamePath(requested)
	records, coverage, err := workspace.FindSymbols(query)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "symbol_find_failed", err)
	}
	providerEvidence := any(nil)
	providerWarning := ""
	if !coverage.Complete {
		backend, release, providerErr := h.pool.Canonical(ctx, workspace)
		defer release()
		if providerErr == nil {
			providerEvidence, providerErr = providerpool.CallCanonical(ctx, requestID, workspace, backend, "find_symbol", map[string]any{
				"root": workspace.Identity().Root, "name": query, "include_body": false,
			})
		}
		if providerErr != nil {
			providerWarning = "Embedded semantic provider fallback failed: " + providerErr.Error()
		} else {
			value, _ := providerEvidence.(map[string]any)
			provided, warning := registerProviderMatches(workspace, mcpapi.AnySlice(value["matches"]))
			records, providerWarning = mergeSymbolRecords(records, provided), warning
			coverage = workspacecore.Coverage{Complete: providerWarning == "", Semantic: "embedded_nvim"}
			if reason := providerIncompleteness(value); reason != "" {
				coverage.Complete = false
				coverage.Skipped = append(coverage.Skipped, reason)
			}
		}
	}
	includeSource, _ := arguments["include_source"].(bool)
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{"handle": record}
		if includeSource {
			read, readErr := workspace.Read(record.Locator.Path)
			if readErr == nil && record.Locator.ByteStart >= 0 && record.Locator.ByteEnd <= len(read.Content) {
				item["source"] = string(read.Content[record.Locator.ByteStart:record.Locator.ByteEnd])
			}
		}
		items = append(items, item)
	}
	outcome, code := "ok", ""
	if !coverage.Complete {
		outcome, code = "partial", "semantic_coverage_partial"
	}
	summary := fmt.Sprintf("%d symbols found", len(records))
	if len(records) == 0 && !coverage.Complete {
		summary = "Symbol search could not establish results because semantic coverage is incomplete"
	}
	data := map[string]any{"ranked_handles": items, "coverage": coverage}
	if providerEvidence != nil {
		data["provider_evidence"] = providerEvidence
	}
	result := mcpapi.Envelope(requestID, workspace, outcome, code, summary, data)
	if providerWarning != "" {
		result["warnings"] = []string{providerWarning}
	}
	if !coverage.Complete {
		result["next"] = []any{
			map[string]any{"tool": "language_server_status", "action": "inspect_attachment"},
			map[string]any{"tool": "search", "action": "literal_fallback", "query": query},
		}
	}
	return result
}

// registerProviderMatches turns the kernel's find_symbol matches (file,
// name path, kind and a first-last line range) into symbol handles. The
// returned warning names the last match that could not be registered.
func registerProviderMatches(workspace *workspacecore.Workspace, matches []any) ([]workspacecore.HandleRecord, string) {
	records := make([]workspacecore.HandleRecord, 0, len(matches))
	warning := ""
	for _, raw := range matches {
		match, _ := raw.(map[string]any)
		path, name, kind := fmt.Sprint(match["file"]), fmt.Sprint(match["name_path"]), fmt.Sprint(match["kind"])
		read, err := workspace.Read(path)
		if err != nil {
			warning = "Some embedded semantic provider matches could not be registered: " + err.Error()
			continue
		}
		start, end, err := providerLineByteRange(read.Content, fmt.Sprint(match["lines"]))
		if err != nil {
			warning = "Some embedded semantic provider matches could not be registered: " + err.Error()
			continue
		}
		record, err := workspace.RegisterSymbolHandle(path, name, kind, start, end)
		if err != nil {
			warning = "Some embedded semantic provider matches could not be registered: " + err.Error()
			continue
		}
		records = append(records, record)
	}
	return records, warning
}

// mergeSymbolRecords adds the provider's declarations to the native ones,
// skipping any declaration the native sectioner already found at the same
// path and name.
func mergeSymbolRecords(native, provided []workspacecore.HandleRecord) []workspacecore.HandleRecord {
	seen := make(map[string]bool, len(native))
	for _, record := range native {
		seen[record.Locator.Path+"\x00"+record.Locator.NamePath] = true
	}
	merged := append([]workspacecore.HandleRecord(nil), native...)
	for _, record := range provided {
		if !seen[record.Locator.Path+"\x00"+record.Locator.NamePath] {
			merged = append(merged, record)
		}
	}
	return merged
}
