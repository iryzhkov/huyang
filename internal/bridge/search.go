package bridge

import (
	"context"
	"errors"
	"fmt"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *toolHandlers) search(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	if source, ok := arguments["git_history"].(map[string]any); ok {
		fields := make([]string, 0)
		for _, value := range anySlice(source["fields"]) {
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
			return modernFailure(requestID, workspace, "git_history_search_failed", err)
		}
		noun := "matches"
		if len(result.Hits) == 1 {
			noun = "match"
		}
		return modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("%d historical %s", len(result.Hits), noun), result)
	}
	if parent, _ := arguments["result_set_handle"].(string); parent != "" {
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
				return modernEnvelope(requestID, workspace, "conflict", string(conflict.Code), conflict.Error(), map[string]any{"result_set_handle": parent})
			}
			return modernFailure(requestID, workspace, "search_refinement_failed", err)
		}
		limit := argInt(arguments, "limit", 50)
		includeRanges, _ := arguments["include_ranges"].(bool)
		hits, truncated := compactSearchHits(result.Matches, limit, includeRanges)
		envelope := modernEnvelope(requestID, workspace, "ok", "", fmt.Sprintf("%d matches retained; %d eliminated", result.Retained, result.Eliminated), map[string]any{
			"hits": hits, "returned": len(hits), "total": result.Retained, "result_set": result, "coverage": result.Coverage,
		})
		if truncated {
			envelope["warnings"] = []string{fmt.Sprintf("response limited to %d of %d retained matches", len(hits), result.Retained)}
			envelope["next"] = []any{map[string]any{"tool": "search", "action": "refine", "result_set_handle": result.Handle}}
		}
		return envelope
	}
	query, _ := arguments["query"].(string)
	mode := workspacecore.SearchMode("literal")
	if requested, _ := arguments["mode"].(string); requested != "" {
		mode = workspacecore.SearchMode(requested)
	}
	result, err := workspace.Search(workspacecore.SearchRequest{Query: query, Mode: mode})
	if err != nil {
		code := "search_failed"
		failure := modernFailure(requestID, workspace, code, err)
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
	hits, truncated := compactSearchHits(result.Hits, limit, includeRanges)
	summary := fmt.Sprintf("%d matches", len(result.Hits))
	if len(result.Hits) == 1 {
		summary = "1 match"
	}
	if !result.Coverage.Complete {
		summary += "; search coverage incomplete"
	}
	data := map[string]any{
		"workspace": result.Workspace, "hits": hits, "returned": len(hits), "total": len(result.Hits),
		"coverage": result.Coverage, "query": result.Query, "mode": result.Mode, "result_set": result.ResultSet,
	}
	envelope := modernEnvelope(requestID, workspace, "ok", "", summary, data)
	if truncated {
		envelope["warnings"] = []string{fmt.Sprintf("response limited to %d of %d matches", len(hits), len(result.Hits))}
		envelope["next"] = []any{map[string]any{"tool": "search", "action": "refine", "result_set_handle": result.ResultSet.Handle}}
	} else if !result.Coverage.Complete {
		envelope["next"] = []any{map[string]any{"tool": "read", "action": "read_known_path"}}
	}
	return envelope
}

func (h *toolHandlers) symbolFind(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	query, _ := arguments["query"].(string)
	records, coverage, err := workspace.FindSymbols(query)
	if err != nil {
		return modernFailure(requestID, workspace, "symbol_find_failed", err)
	}
	providerEvidence := any(nil)
	providerWarning := ""
	if !coverage.Complete {
		backend, providerErr := h.pool.canonical(ctx, workspace)
		if providerErr == nil {
			providerEvidence, providerErr = callCanonicalProvider(ctx, requestID, workspace, backend, "find_symbol", map[string]any{
				"root": workspace.Identity().Root, "name": query, "include_body": false,
			})
		}
		if providerErr != nil {
			providerWarning = "Embedded semantic provider fallback failed: " + providerErr.Error()
		} else {
			value, _ := providerEvidence.(map[string]any)
			providerRecords := make([]workspacecore.HandleRecord, 0, len(anySlice(value["matches"])))
			for _, raw := range anySlice(value["matches"]) {
				match, _ := raw.(map[string]any)
				path, name, kind := fmt.Sprint(match["file"]), fmt.Sprint(match["name_path"]), fmt.Sprint(match["kind"])
				read, readErr := workspace.Read(path)
				if readErr != nil {
					providerWarning = "Some embedded semantic provider matches could not be registered: " + readErr.Error()
					continue
				}
				start, end, rangeErr := providerLineByteRange(read.Content, fmt.Sprint(match["lines"]))
				if rangeErr != nil {
					providerWarning = "Some embedded semantic provider matches could not be registered: " + rangeErr.Error()
					continue
				}
				record, registerErr := workspace.RegisterSymbolHandle(path, name, kind, start, end)
				if registerErr != nil {
					providerWarning = "Some embedded semantic provider matches could not be registered: " + registerErr.Error()
					continue
				}
				providerRecords = append(providerRecords, record)
			}
			records = providerRecords
			coverage = workspacecore.Coverage{Complete: providerWarning == "", Semantic: "embedded_nvim"}
		}
	}
	items := make([]map[string]any, 0, len(records))
	includeSource, _ := arguments["include_source"].(bool)
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
	result := modernEnvelope(requestID, workspace, outcome, code, summary, data)
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
