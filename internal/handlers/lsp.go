package handlers

import (
	"context"
	"fmt"
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

func (h *Handlers) languageServerStatus(ctx context.Context, requestID string, workspace *workspacecore.Workspace) map[string]any {
	backend, err := h.pool.Canonical(ctx, workspace)
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
	attachedLanguages, missing := 0, []string{}
	failedAttachments := []string{}
	seenServers := map[string]struct{}{}
	attachedServers := []string{}
	installOptions := map[string][]string{}
	for _, raw := range mcpapi.AnySlice(support["languages"]) {
		entry, _ := raw.(map[string]any)
		reconcileParserSupport(entry)
		language := fmt.Sprint(entry["filetype"])
		lsp := fmt.Sprint(entry["lsp"])
		if lsp != "" && lsp != "none" && !strings.HasPrefix(lsp, "none (") {
			attachedLanguages++
			for _, name := range strings.Split(lsp, ",") {
				name = strings.TrimSpace(name)
				if _, seen := seenServers[name]; name != "" && !seen {
					seenServers[name] = struct{}{}
					attachedServers = append(attachedServers, name)
				}
			}
		} else if language != "" {
			missing = append(missing, language)
			if strings.HasPrefix(lsp, "none (configured:") {
				failedAttachments = append(failedAttachments, language)
			}
			for _, rawOption := range mcpapi.AnySlice(entry["install_options"]) {
				if option := strings.TrimSpace(fmt.Sprint(rawOption)); option != "" {
					duplicate := false
					for _, existing := range installOptions[language] {
						if existing == option {
							duplicate = true
							break
						}
					}
					if !duplicate {
						installOptions[language] = append(installOptions[language], option)
					}
				}
			}
		}
	}
	outcome := "ok"
	summary := fmt.Sprintf("%d unique language servers attached across %d workspace language modes", len(attachedServers), attachedLanguages)
	code := ""
	if len(attachedServers) == 0 {
		outcome, code, summary = "unavailable", "language_server_unavailable", "No workspace language server is attached"
	} else if len(failedAttachments) > 0 {
		outcome, code = "partial", "language_server_attachment_incomplete"
		summary = fmt.Sprintf("%d language server(s) attached, but configured servers did not attach for: %s", len(attachedServers), strings.Join(failedAttachments, ", "))
	}
	result := mcpapi.Envelope(requestID, workspace, outcome, code, summary, map[string]any{
		"provider": providerpool.Status(ctx, backend), "language_servers": mcpapi.CompactLanguageSupport(support),
		"attached_language_count": attachedLanguages, "attached_server_count": len(attachedServers),
		"attached_servers": attachedServers, "missing_languages": missing,
		"failed_attachment_languages": failedAttachments, "install_options": installOptions,
	})
	if len(missing) > 0 {
		result["warnings"] = []string{"Some workspace languages have no attached language server."}
		next := make([]any, 0, 2)
		for _, language := range missing {
			if len(next) == 2 {
				break
			}
			action := map[string]any{"tool": "language_server_setup", "action": "install", "language": language, "use_new_idempotency_key": true}
			if options := installOptions[language]; len(options) > 0 {
				action["install_options_key"] = language
				action["selection"] = "automatic_preferred"
			}
			next = append(next, action)
		}
		result["next"] = next
	}
	return result
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
	if action == "restart" {
		before := h.languageServerStatus(ctx, requestID, workspace)
		previouslyAttached := []string{}
		if beforeData, _ := before["data"].(map[string]any); beforeData != nil {
			for _, server := range mcpapi.AnySlice(beforeData["attached_servers"]) {
				if name := strings.TrimSpace(fmt.Sprint(server)); name != "" {
					previouslyAttached = append(previouslyAttached, name)
				}
			}
		}
		backend, err := h.pool.Restart(ctx, workspace)
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
		current := map[string]bool{}
		for _, server := range mcpapi.AnySlice(data["attached_servers"]) {
			current[strings.TrimSpace(fmt.Sprint(server))] = true
		}
		lost := []string{}
		for _, server := range previouslyAttached {
			if !current[server] {
				lost = append(lost, server)
			}
		}
		if len(lost) == 0 && verified["outcome"] == "ok" {
			verified["summary"] = "Owned Neovim provider restarted and installed language-server attachments were verified"
		} else {
			data["lost_attached_servers"] = lost
			verified["outcome"] = "provisional"
			verified["code"] = "language_server_restart_incomplete"
			verified["summary"] = "Owned Neovim provider restarted, but one or more installed language servers did not reattach"
		}
		return verified
	}
	if action != "install" {
		return mcpapi.Envelope(requestID, workspace, "failed", "invalid_language_server_action", "action must be install or restart", map[string]any{})
	}
	language, _ := arguments["language"].(string)
	if strings.TrimSpace(language) == "" {
		return mcpapi.Envelope(requestID, workspace, "failed", "language_required", "language is required for install", map[string]any{})
	}
	backend, err := h.pool.Canonical(ctx, workspace)
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
	status := strings.ToLower(strings.TrimSpace(fmt.Sprint(serverResult["status"])))
	requestedServer := strings.TrimSpace(fmt.Sprint(arguments["server"]))
	if requestedServer == "" && (len(serverResult) == 0 || status == "skipped" || status == "none") {
		result := mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("Parser support installation completed for %s; no language server was requested", language), data)
		result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "verify_parser"}}
		return result
	}
	if attached, present := serverResult["attached"]; present && attached == false {
		note := strings.TrimSpace(fmt.Sprint(serverResult["note"]))
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
		note := strings.TrimSpace(fmt.Sprint(serverResult["note"]))
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
		note := strings.TrimSpace(fmt.Sprint(serverResult["note"]))
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
	providerArguments, err := modernProviderTarget(workspace, target)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_target_invalid", err)
	}
	backend, err := h.pool.Canonical(ctx, workspace)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_provider_start_failed", err)
	}
	value, err := providerpool.CallCanonical(ctx, requestID, workspace, backend, relation, providerArguments)
	if err != nil {
		result := modernProviderFailure(requestID, workspace, "language_server_unavailable", err)
		result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "inspect_attachment"}}
		return result
	}
	navigation, _ := value.(map[string]any)
	if count, present := navigation["count"]; present && fmt.Sprint(count) == "0" {
		result := mcpapi.Envelope(requestID, workspace, "partial", "navigation_not_found",
			fmt.Sprintf("The workspace language server returned no %s location", relation), map[string]any{
				"navigation": value, "provider": providerpool.Status(ctx, backend),
				"coverage": workspacecore.Coverage{Complete: true, Semantic: "lsp"},
			})
		result["next"] = []any{
			map[string]any{"tool": "language_server_status", "action": "inspect_attachment"},
			map[string]any{"tool": "symbol_find", "query": fmt.Sprint(providerArguments["symbol"])},
		}
		return result
	}
	return mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("%s resolved through the workspace language server", relation), map[string]any{
		"navigation": value, "provider": providerpool.Status(ctx, backend),
		"coverage": workspacecore.Coverage{Complete: true, Semantic: "lsp"},
	})
}

func (h *Handlers) codeActionsProvider(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	target, _ := arguments["target"].(map[string]any)
	providerArguments, err := modernProviderTarget(workspace, target)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_target_invalid", err)
	}
	backend, err := h.pool.Canonical(ctx, workspace)
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
