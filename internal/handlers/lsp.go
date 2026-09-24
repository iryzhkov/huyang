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

func languageServerAttachmentConfirmed(value any) bool {
	switch attached := value.(type) {
	case bool:
		return attached
	case string:
		attached = strings.TrimSpace(attached)
		return attached != "" && attached != "false" && attached != "none"
	default:
		return false
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
	starting       []string
	installOptions map[string][]string
}

func summariseLanguageSupport(support map[string]any) languageSupport {
	tally := languageSupport{attachedServers: []string{}, missing: []string{}, failedAttachments: []string{}, starting: []string{}, installOptions: map[string][]string{}}
	seenServers := map[string]struct{}{}
	for _, raw := range mcpapi.AnySlice(support["languages"]) {
		entry, _ := raw.(map[string]any)
		reconcileParserSupport(entry)
		language := fmt.Sprint(entry["filetype"])
		lsp := fmt.Sprint(entry["lsp"])
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

func (h *Handlers) languageServerStatus(ctx context.Context, requestID string, workspace *workspacecore.Workspace) map[string]any {
	backend, release, err := h.pool.Canonical(ctx, workspace)
	defer release()
	if err != nil {
		result := mcpapi.Failure(requestID, workspace, "semantic_provider_start_failed", err)
		result["next"] = []any{map[string]any{"tool": "language_server_setup", "action": "restart", "use_new_idempotency_key": true}}
		return result
	}
	value, err := providerpool.CallCanonical(ctx, requestID, workspace, backend, "workspace_support", map[string]any{"root": workspace.Identity().Root})
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
	} else if len(tally.attachedServers) == 0 {
		outcome, code, summary = "unavailable", "language_server_unavailable", "No workspace language server is attached"
	} else if len(tally.starting) > 0 {
		outcome, code = "partial", "language_server_starting"
		summary = fmt.Sprintf("%d language server(s) attached; still starting for: %s", len(tally.attachedServers), strings.Join(tally.starting, ", "))
	} else if len(tally.failedAttachments) > 0 {
		outcome, code = "partial", "language_server_attachment_incomplete"
		summary = fmt.Sprintf("%d language server(s) attached, but configured servers did not attach for: %s", len(tally.attachedServers), strings.Join(tally.failedAttachments, ", "))
	}
	result := mcpapi.Envelope(requestID, workspace, outcome, code, summary, map[string]any{
		"provider": providerpool.Status(ctx, backend), "language_servers": mcpapi.CompactLanguageSupport(support),
		"attached_language_count": tally.attachedLanguages, "attached_server_count": len(tally.attachedServers),
		"attached_servers": tally.attachedServers, "missing_languages": tally.missing,
		"failed_attachment_languages": tally.failedAttachments, "starting_languages": tally.starting, "install_options": tally.installOptions,
	})
	if len(tally.missing) > 0 {
		result["warnings"] = []string{"Some workspace languages have no attached language server."}
		result["next"] = installMissingLanguagesNext(tally)
	}
	return result
}

// installMissingLanguagesNext offers an install for the first two missing
// languages, pointing at their install options when the provider listed any.
func installMissingLanguagesNext(tally languageSupport) []any {
	next := make([]any, 0, 2)
	for _, language := range tally.missing {
		if len(next) == 2 {
			break
		}
		action := map[string]any{"tool": "language_server_setup", "action": "install", "language": language, "use_new_idempotency_key": true}
		if options := tally.installOptions[language]; len(options) > 0 {
			action["install_options_key"] = language
			action["selection"] = "automatic_preferred"
		}
		next = append(next, action)
	}
	return next
}

func failedLanguageServerInstallNext(status, language, requestedServer string) []any {
	statusAction := map[string]any{"tool": "language_server_status", "action": "inspect_attachment"}
	if status != "failed" {
		return []any{statusAction}
	}
	retry := map[string]any{
		"tool": "language_server_setup", "action": "install", "language": language,
		"use_new_idempotency_key": true,
	}
	if requestedServer != "" {
		retry["server"] = requestedServer
	}
	return []any{retry, statusAction}
}

func (h *Handlers) languageServerSetup(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	action, _ := arguments["action"].(string)
	switch action {
	case "restart":
		return h.restartLanguageServers(ctx, requestID, workspace)
	case "install":
		return h.installLanguageServer(ctx, requestID, workspace, arguments)
	default:
		return mcpapi.Envelope(requestID, workspace, "failed", "invalid_language_server_action", "action must be install or restart", map[string]any{})
	}
}

// restartLanguageServers replaces the canonical provider and verifies that
// every server attached before the restart attached again.
func (h *Handlers) restartLanguageServers(ctx context.Context, requestID string, workspace *workspacecore.Workspace) map[string]any {
	before := h.languageServerStatus(ctx, requestID, workspace)
	previouslyAttached := attachedServerNames(before)
	backend, release, err := h.pool.Restart(ctx, workspace)
	defer release()
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_provider_restart_failed", err)
	}
	verified := h.languageServerStatus(ctx, requestID, workspace)
	data, _ := verified["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	data["provider"] = providerpool.Status(ctx, backend)
	verified["data"] = data
	current := attachedServerNames(verified)
	lost := []string{}
	for _, server := range previouslyAttached {
		if !slices.Contains(current, server) {
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
	names := []string{}
	data, _ := status["data"].(map[string]any)
	for _, server := range mcpapi.AnySlice(data["attached_servers"]) {
		if name := strings.TrimSpace(fmt.Sprint(server)); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// installLanguageServer asks the provider to install support for one
// language and classifies what the provider reports about the server.
func (h *Handlers) installLanguageServer(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	language, _ := arguments["language"].(string)
	if strings.TrimSpace(language) == "" {
		return mcpapi.Envelope(requestID, workspace, "failed", "language_required", "language is required for install", map[string]any{})
	}
	backend, release, err := h.pool.Canonical(ctx, workspace)
	defer release()
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_provider_start_failed", err)
	}
	providerArguments := map[string]any{"root": workspace.Identity().Root, "language": language}
	if server, ok := arguments["server"].(string); ok && server != "" {
		providerArguments["server"] = server
	}
	if parser, ok := arguments["parser"].(bool); ok {
		providerArguments["parser"] = parser
	}
	value, err := providerpool.CallCanonical(ctx, requestID, workspace, backend, "install_language", providerArguments)
	if err != nil {
		return modernProviderFailure(requestID, workspace, "language_server_install_failed", err)
	}
	data := map[string]any{"installation": value, "provider": providerpool.Status(ctx, backend)}
	installation, _ := value.(map[string]any)
	serverResult, _ := installation["server"].(map[string]any)
	requestedServer := strings.TrimSpace(fmt.Sprint(arguments["server"]))
	return installOutcome(requestID, workspace, language, requestedServer, serverResult, data)
}

// installOutcome turns the provider's server report into the tool result:
// parser-only success, an installed server that did not attach, a failed
// install, an unconfirmed attachment, or a confirmed one.
func installOutcome(requestID string, workspace *workspacecore.Workspace, language, requestedServer string, serverResult, data map[string]any) map[string]any {
	status := strings.ToLower(strings.TrimSpace(fmt.Sprint(serverResult["status"])))
	note := strings.TrimSpace(fmt.Sprint(serverResult["note"]))
	if requestedServer == "" && (len(serverResult) == 0 || status == "skipped" || status == "none") {
		result := mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("Parser support installation completed for %s; no language server was requested", language), data)
		result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "verify_parser"}}
		return result
	}
	if attached, present := serverResult["attached"]; present && attached == false {
		if note == "" {
			note = "the language server did not attach after installation"
		}
		requirement := note
		if strings.Contains(strings.ToLower(note), "cargo not found") {
			requirement = "Install the Rust toolchain so cargo is executable in the Huyang user service PATH, then restart the provider."
		}
		result := mcpapi.Envelope(requestID, workspace, "provisional", "language_server_not_attached",
			fmt.Sprintf("Language support was installed for %s, but its server did not attach: %s", language, note), data)
		result["warnings"] = []string{requirement}
		result["next"] = []any{
			map[string]any{"tool": "language_server_setup", "action": "restart", "prerequisite": requirement, "use_new_idempotency_key": true},
			map[string]any{"tool": "language_server_status", "action": "verify_attachment"},
		}
		return result
	}
	if status == "failed" || status == "unknown" || status == "unsupported" || status == "skipped" {
		if note == "" {
			note = "the provider did not supply installation diagnostics"
		}
		result := mcpapi.Envelope(requestID, workspace, "failed", "language_server_install_failed",
			fmt.Sprintf("Language support installation failed for %s: %s", language, note), data)
		result["warnings"] = []string{note}
		result["next"] = failedLanguageServerInstallNext(status, language, requestedServer)
		return result
	}
	if attached, present := serverResult["attached"]; !present || !languageServerAttachmentConfirmed(attached) {
		if note == "" || note == "<nil>" {
			note = "the provider did not positively confirm attachment"
		}
		result := mcpapi.Envelope(requestID, workspace, "provisional", "language_server_attachment_unconfirmed",
			fmt.Sprintf("Language support was installed for %s, but server attachment is unconfirmed: %s", language, note), data)
		result["warnings"] = []string{note}
		result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "verify_attachment"}}
		return result
	}
	result := mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("Language support installation completed and attached for %s", language), data)
	result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "verify_attachment"}}
	return result
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
		result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "inspect_attachment"}}
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
// A search's paths scope keeps only the declarations inside it, and a search
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
		if record.Locator.NamePath != name && leaf != name {
			continue
		}
		if len(scope) > 0 && !workspacecore.MatchesPathScope(record.Locator.Path, scope) {
			continue
		}
		exact = append(exact, record)
	}
	switch len(exact) {
	case 1:
		return map[string]any{"path": exact[0].Locator.Path, "name_path": exact[0].Locator.NamePath}, nil
	case 0:
		summary := fmt.Sprintf("no declaration named %q; name the file with target.symbol_locator or search literally", symbol)
		if searchMode != "" {
			summary = fmt.Sprintf("no declaration named %q; search literally", symbol)
			if len(scope) > 0 {
				summary = fmt.Sprintf("no declaration named %q inside paths %q; widen paths or search literally", symbol, scope)
			}
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
	// Search takes no target, so each follow-up repeats the search scoped to
	// one candidate's file under that candidate's full name path, which
	// resolves to it alone.
	result := mcpapi.Envelope(requestID, workspace, "conflict", "symbol_ambiguous", fmt.Sprintf("%d declarations are named %q; narrow with paths or query a name path from data.declarations", len(exact), symbol), data)
	next := []any{}
	for _, record := range exact {
		if len(next) == 2 {
			break
		}
		next = append(next, map[string]any{
			"tool": "search", "action": "scope_to_candidate", "query": record.Locator.NamePath, "mode": searchMode, "paths": []string{record.Locator.Path},
		})
	}
	result["next"] = next
	return nil, result
}

func (h *Handlers) codeActionsProvider(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	target, _ := arguments["target"].(map[string]any)
	providerArguments, err := modernProviderTarget(workspace, target)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_target_invalid", err)
	}
	backend, release, err := h.pool.Canonical(ctx, workspace)
	defer release()
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_provider_start_failed", err)
	}
	value, err := providerpool.CallCanonical(ctx, requestID, workspace, backend, "code_actions", providerArguments)
	if err != nil {
		result := modernProviderFailure(requestID, workspace, "language_server_unavailable", err)
		result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "inspect_attachment"}}
		return result
	}
	return mcpapi.Envelope(requestID, workspace, "ok", "", "Code actions retrieved through the workspace language server", map[string]any{"code_actions": value, "provider": providerpool.Status(ctx, backend)})
}
