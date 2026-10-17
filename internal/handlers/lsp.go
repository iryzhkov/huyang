package handlers

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func verificationParserAvailable(language string) bool {
	switch language {
	case "go", "json", "jsonl", "toml", "yaml", "yml", "markdown":
		return true
	default:
		return false
	}
}

func reconcileParserSupport(entry map[string]any) {
	installed, _ := entry["treesitter_parser"].(bool)
	available := installed && verificationParserAvailable(fmt.Sprint(entry["filetype"]))
	entry["treesitter_parser_installed"] = installed
	entry["verification_parser"] = available
	entry["treesitter_parser"] = installed
	if installed && !available {
		entry["verification_parser_recovery"] = "The embedded Tree-sitter parser is usable for semantic operations; exact verification uses the trusted configured project check for this language."
	}
}

// languageSupport is what the provider's workspace_support report says
// once the languages are tallied: which servers attached, which languages
// have none, which configured servers failed, and the install options per
// missing language.
type languageSupport struct {
	attachedLanguages int
	attachedServers   []string
	missing           []string
	failedAttachments []string
	// starting lists languages whose server has a client on its way but
	// not yet initialized: neither attached nor failed.
	starting []string
	// onDemand lists languages whose server is enabled and startable but
	// was deliberately not started because the language is incidental to
	// the project; the first call that loads one of its files starts it.
	// onDemandServers names the servers configured for them.
	onDemand        []string
	onDemandServers []string
	installOptions  map[string][]string
}

func summariseLanguageSupport(support map[string]any) languageSupport {
	tally := languageSupport{attachedServers: []string{}, missing: []string{}, failedAttachments: []string{}, starting: []string{}, onDemand: []string{}, onDemandServers: []string{}, installOptions: map[string][]string{}}
	seenServers := map[string]struct{}{}
	for _, raw := range mcpapi.AnySlice(support["languages"]) {
		entry, _ := raw.(map[string]any)
		reconcileParserSupport(entry)
		language := fmt.Sprint(entry["filetype"])
		lsp := fmt.Sprint(entry["lsp"])
		if fmt.Sprint(entry["attach"]) == "on_demand" {
			tally.onDemand = append(tally.onDemand, language)
			for _, server := range mcpapi.AnySlice(entry["configured"]) {
				if name := strings.TrimSpace(fmt.Sprint(server)); name != "" && !slices.Contains(tally.onDemandServers, name) {
					tally.onDemandServers = append(tally.onDemandServers, name)
				}
			}
			continue
		}
		if fmt.Sprint(entry["attach"]) == "starting" || strings.HasPrefix(lsp, "starting (") {
			tally.starting = append(tally.starting, language)
			continue
		}
		if lsp != "" && lsp != "none" && !strings.HasPrefix(lsp, "none (") {
			tally.attachedLanguages++
			for _, name := range strings.Split(lsp, ",") {
				name = strings.TrimSpace(name)
				if _, seen := seenServers[name]; name != "" && !seen {
					seenServers[name] = struct{}{}
					tally.attachedServers = append(tally.attachedServers, name)
				}
			}
			continue
		}
		if language == "" {
			continue
		}
		tally.missing = append(tally.missing, language)
		if strings.HasPrefix(lsp, "none (configured:") {
			tally.failedAttachments = append(tally.failedAttachments, language)
		}
		for _, rawOption := range mcpapi.AnySlice(entry["install_options"]) {
			if option := strings.TrimSpace(fmt.Sprint(rawOption)); option != "" && !slices.Contains(tally.installOptions[language], option) {
				tally.installOptions[language] = append(tally.installOptions[language], option)
			}
		}
	}
	return tally
}

// languageServerStatus probes the canonical provider's language servers.
// languages names filetypes whose servers should be started even when they
// are incidental to the project and would otherwise start on first use.
func (h *Handlers) languageServerStatus(ctx context.Context, requestID string, workspace *workspacecore.Workspace, languages []string) map[string]any {
	backend, release, err := h.pool.Canonical(ctx, workspace)
	defer release()
	if err != nil {
		result := mcpapi.Failure(requestID, workspace, "semantic_provider_start_failed", err)
		result["next"] = []any{map[string]any{"tool": "language_server_setup", "action": "restart", "use_new_idempotency_key": true}}
		return result
	}
	probeArguments := map[string]any{"root": workspace.Identity().Root}
	if len(languages) > 0 {
		probeArguments["languages"] = languages
	}
	value, err := providerpool.CallCanonical(ctx, requestID, workspace, backend, "workspace_support", probeArguments)
	if err != nil {
		result := modernProviderFailure(requestID, workspace, "language_server_probe_failed", err)
		result["data"] = map[string]any{"provider": providerpool.Status(ctx, backend)}
		result["next"] = []any{map[string]any{"tool": "language_server_setup", "action": "restart", "use_new_idempotency_key": true}}
		return result
	}
	support, _ := value.(map[string]any)
	tally := summariseLanguageSupport(support)
	outcome, code := "ok", ""
	summary := fmt.Sprintf("%d unique language servers attached across %d workspace language modes", len(tally.attachedServers), tally.attachedLanguages)
	if len(tally.attachedServers) == 0 && len(tally.starting) > 0 {
		outcome, code = "provisional", "language_server_starting"
		summary = fmt.Sprintf("No language server is attached yet; still starting for: %s. Retry shortly rather than restarting", strings.Join(tally.starting, ", "))
	} else if len(tally.attachedServers) == 0 && len(tally.onDemand) == 0 {
		outcome, code, summary = "unavailable", "language_server_unavailable", "No workspace language server is attached"
	} else if len(tally.starting) > 0 {
		outcome, code = "partial", "language_server_starting"
		summary = fmt.Sprintf("%d language server(s) attached; still starting for: %s", len(tally.attachedServers), strings.Join(tally.starting, ", "))
	} else if len(tally.failedAttachments) > 0 {
		outcome, code = "partial", "language_server_attachment_incomplete"
		summary = fmt.Sprintf("%d language server(s) attached, but configured servers did not attach for: %s", len(tally.attachedServers), strings.Join(tally.failedAttachments, ", "))
	}
	if len(tally.onDemand) > 0 {
		summary += fmt.Sprintf("; servers for %s start on first use (pass languages to start them now)", strings.Join(tally.onDemand, ", "))
	}
	result := mcpapi.Envelope(requestID, workspace, outcome, code, summary, map[string]any{
		"provider": providerpool.Status(ctx, backend), "language_servers": mcpapi.CompactLanguageSupport(support),
		"attached_language_count": tally.attachedLanguages, "attached_server_count": len(tally.attachedServers),
		"attached_servers": tally.attachedServers, "missing_languages": tally.missing,
		"failed_attachment_languages": tally.failedAttachments, "starting_languages": tally.starting, "install_options": tally.installOptions,
		"on_demand_languages": tally.onDemand, "on_demand_servers": tally.onDemandServers,
	})
	if len(tally.missing) > 0 {
		result["warnings"] = []string{"Some workspace languages have no attached language server."}
		result["next"] = outOfBandInstallNext(tally)
	}
	return result
}

// outOfBandInstallNext says how a missing language gets a server. Huyang
// installs nothing itself, so the server is installed outside it and a
// restart is what makes the provider pick it up.
func outOfBandInstallNext(tally languageSupport) []any {
	prerequisite := fmt.Sprintf("Install a language server for %s outside Huyang: with Mason in your Neovim setup or with the system package manager.", strings.Join(tally.missing, ", "))
	if len(tally.installOptions) > 0 {
		prerequisite += " data.install_options lists the Mason packages that serve each language."
	}
	return []any{map[string]any{
		"tool": "language_server_setup", "action": "restart", "prerequisite": prerequisite, "use_new_idempotency_key": true,
	}}
}

// outOfBandInstallMessage is the refusal for any setup action but restart:
// installing language servers is the user's Neovim setup's job, not this
// service's.
const outOfBandInstallMessage = "action must be restart. Huyang does not install language servers or parsers: install them outside it (Mason in your Neovim setup, or the system package manager), then restart"

func (h *Handlers) languageServerSetup(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	action, _ := arguments["action"].(string)
	if action != "restart" {
		return mcpapi.Envelope(requestID, workspace, "failed", "invalid_language_server_action", outOfBandInstallMessage, map[string]any{})
	}
	return h.restartLanguageServers(ctx, requestID, workspace)
}

// restartLanguageServers replaces the canonical provider and verifies that
// every server attached before the restart attached again.
func (h *Handlers) restartLanguageServers(ctx context.Context, requestID string, workspace *workspacecore.Workspace) map[string]any {
	before := h.languageServerStatus(ctx, requestID, workspace, nil)
	previouslyAttached := attachedServerNames(before)
	backend, release, err := h.pool.Restart(ctx, workspace)
	defer release()
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_provider_restart_failed", err)
	}
	verified := h.languageServerStatus(ctx, requestID, workspace, nil)
	data, _ := verified["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	data["provider"] = providerpool.Status(ctx, backend)
	verified["data"] = data
	current := attachedServerNames(verified)
	// A server a tool call had started for an incidental language is not
	// restarted by the probe; it is on demand again, not lost.
	onDemand := stringList(data["on_demand_servers"])
	lost := []string{}
	for _, server := range previouslyAttached {
		if !slices.Contains(current, server) && !slices.Contains(onDemand, server) {
			lost = append(lost, server)
		}
	}
	if len(lost) == 0 && verified["outcome"] == "ok" {
		verified["summary"] = "Owned Neovim provider restarted and installed language-server attachments were verified"
		return verified
	}
	data["lost_attached_servers"] = lost
	verified["outcome"] = "provisional"
	verified["code"] = "language_server_restart_incomplete"
	verified["summary"] = "Owned Neovim provider restarted, but one or more installed language servers did not reattach"
	return verified
}

// attachedServerNames reads the attached servers out of a status envelope.
func attachedServerNames(status map[string]any) []string {
	data, _ := status["data"].(map[string]any)
	return stringList(data["attached_servers"])
}

// stringList reads a list of names out of a decoded value, dropping blanks.
func stringList(value any) []string {
	names := []string{}
	for _, item := range mcpapi.AnySlice(value) {
		if name := strings.TrimSpace(fmt.Sprint(item)); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func (h *Handlers) navigateProvider(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	relation, _ := arguments["relation"].(string)
	target, _ := arguments["target"].(map[string]any)
	if symbol, _ := arguments["symbol"].(string); symbol != "" && target == nil {
		searchMode, _ := arguments["search_mode"].(string)
		locator, failure := h.locatorForSymbol(ctx, requestID, workspace, symbol, argStrings(arguments["paths"]), searchMode)
		if failure != nil {
			return failure
		}
		target = map[string]any{"symbol_locator": locator}
	}
	providerArguments, err := modernProviderTarget(workspace, target)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_target_invalid", err)
	}
	backend, release, err := h.pool.Canonical(ctx, workspace)
	defer release()
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_provider_start_failed", err)
	}
	value, err := providerpool.CallCanonical(ctx, requestID, workspace, backend, relation, providerArguments)
	if err != nil {
		result := modernProviderFailure(requestID, workspace, "language_server_unavailable", err)
		if result["retryable"] != true {
			result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "inspect_attachment"}}
		}
		return result
	}
	// The reply is the navigation itself; the provider's process detail is
	// behind language_server_status and only a degraded state names it here.
	data := map[string]any{"navigation": value}
	if status := providerpool.Status(ctx, backend); fmt.Sprint(status["state"]) != "healthy" {
		data["provider"] = compactProviderStatus(status)
	}
	navigation, _ := value.(map[string]any)
	if count, present := navigation["count"]; present && fmt.Sprint(count) == "0" {
		result := mcpapi.Envelope(requestID, workspace, "partial", "navigation_not_found",
			fmt.Sprintf("The workspace language server returned no %s location", relation), data)
		result["next"] = []any{
			map[string]any{"tool": "language_server_status", "action": "inspect_attachment"},
			map[string]any{"tool": "symbol_find", "query": fmt.Sprint(providerArguments["symbol"])},
		}
		return result
	}
	return mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("%s resolved through the language server", relation), data)
}

// locatorForSymbol turns a bare symbol name into the symbol_locator of its
// one declaration, natively for Go and Python and through the provider
// otherwise, so navigate and semantic search need no path from the caller.
// A search's paths scope says which hits to keep, not where the symbol is
// declared: references in c/ to a function declared in a/ are what
// paths ["c/"] asks for. So the scope only picks among several declarations
// of the name, and only when it keeps at least one of them. A search
// (searchMode names its mode) is told how to narrow in terms search accepts:
// paths, not target.symbol_locator.
func (h *Handlers) locatorForSymbol(ctx context.Context, requestID string, workspace *workspacecore.Workspace, symbol string, scope []string, searchMode string) (map[string]any, map[string]any) {
	name := canonicalNamePath(symbol)
	found := h.symbolFind(ctx, requestID+"_symbol", workspace, map[string]any{"query": name})
	data, _ := found["data"].(map[string]any)
	items, _ := data["ranked_handles"].([]map[string]any)
	var exact []workspacecore.HandleRecord
	for _, item := range items {
		record, _ := item["handle"].(workspacecore.HandleRecord)
		leaf := record.Locator.NamePath
		if slash := strings.LastIndex(leaf, "/"); slash >= 0 {
			leaf = leaf[slash+1:]
		}
		if record.Locator.NamePath == name || leaf == name {
			exact = append(exact, record)
		}
	}
	if len(exact) > 1 && len(scope) > 0 {
		var inside []workspacecore.HandleRecord
		for _, record := range exact {
			if workspacecore.MatchesPathScope(record.Locator.Path, scope) {
				inside = append(inside, record)
			}
		}
		if len(inside) > 0 {
			exact = inside
		}
	}
	switch len(exact) {
	case 1:
		return map[string]any{"path": exact[0].Locator.Path, "name_path": exact[0].Locator.NamePath}, nil
	case 0:
		summary := fmt.Sprintf("no declaration named %q; name the file with target.symbol_locator or search literally", symbol)
		if searchMode != "" {
			summary = fmt.Sprintf("no declaration named %q; search literally", symbol)
		}
		result := mcpapi.Envelope(requestID, workspace, "conflict", "symbol_not_found", summary, map[string]any{"symbol": symbol})
		result["next"] = []any{map[string]any{"tool": "search", "action": "literal_fallback", "query": symbol, "mode": "literal"}}
		return nil, result
	}
	locations := make([]string, 0, len(exact))
	for _, record := range exact {
		locations = append(locations, record.Locator.Path+"#"+record.Locator.NamePath)
	}
	data = map[string]any{"symbol": symbol, "declarations": locations}
	if searchMode == "" {
		return nil, mcpapi.Envelope(requestID, workspace, "conflict", "symbol_ambiguous", fmt.Sprintf("%d declarations are named %q; pick one with target.symbol_locator", len(exact), symbol), data)
	}
	// Search takes no target, and its paths filter the hits rather than
	// choose the declaration, so a search scoped to a candidate's file would
	// drop every hit outside it. Each follow-up is the same relation through
	// navigate, which names the candidate exactly and keeps all its hits.
	result := mcpapi.Envelope(requestID, workspace, "conflict", "symbol_ambiguous", fmt.Sprintf("%d declarations are named %q; navigate with target.symbol_locator from data.declarations to get every hit of one", len(exact), symbol), data)
	next := []any{}
	for _, record := range exact {
		if len(next) == 2 {
			break
		}
		next = append(next, map[string]any{
			"tool": "navigate", "action": "navigate_from_candidate", "relation": searchMode,
			"target": map[string]any{"symbol_locator": map[string]any{"path": record.Locator.Path, "name_path": record.Locator.NamePath}},
		})
	}
	result["next"] = next
	return nil, result
}
