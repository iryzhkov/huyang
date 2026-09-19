package mcpapi

// Byte controls and concise replies are incubated in the experimental catalog.
// Copy every changed schema level so frozen profiles remain byte-identical.
func usabilityDescriptor(descriptor ToolDescriptor) ToolDescriptor {
	extra := map[string]any{
		"response_mode":   map[string]any{"type": "string", "enum": []any{"full", "compact"}, "description": "compact returns one text payload by default, concise edit receipts, and read source windows of 64 KiB unless max_bytes is supplied. full preserves the existing response. Refusal and verification information is retained."},
		"response_format": map[string]any{"type": "string", "enum": []any{"both", "text", "structured"}, "description": "both preserves text and structured payloads; text omits their duplicate structured copy; structured returns structured data with a short text receipt. Overrides the response_mode transport default."},
	}
	switch descriptor.Name {
	case "read":
		for key, value := range readByteProperties() {
			extra[key] = value
		}
		props := descriptor.InputSchema["properties"].(map[string]any)
		targets := CloneEnvelope(props["targets"].(map[string]any))
		targets["items"] = withExperimentalProperties(targets["items"].(map[string]any), readByteProperties())
		extra["targets"] = targets
		descriptor.Description += " Experimental: max_bytes bounds each source selection, including a single minified line; byte_offset and expected_revision_id continue that same selection safely. Offsets count UTF-8 bytes after line selection and numbering; retain the same target/options on continuation. Outline/history views do not accept byte windows."
	case "debug_session":
		extra["program"] = stringSchema("Program to launch. For Delve in a nested Go module, use the absolute module directory for both program and cwd. Relative program/cwd combinations are adapter-dependent; inspect build output on failure.")
		extra["cwd"] = stringSchema("Debuggee working directory. Prefer an absolute path to the directory containing the target Go module go.mod when using Delve; relative cwd can build successfully but fail to launch.")
		descriptor.Description += " A launch build error is not an adapter capability refusal: inspect adapter output and check program/cwd/module paths before retrying with a new idempotency key."
	case "workspace_inspect":
		extra["view"] = enumSchema("status", "overview", "map", "revision")
		descriptor.Description += " view=revision refreshes known documents and returns only the current workspace revision, without loading provider or pipeline details."
	case "edit_apply", "change_plan":
		extra["include_diagnostics"] = map[string]any{"type": "boolean", "description": "In compact mode include diagnostic bodies/notices; otherwise return their availability and verification status, with diagnostics as the follow-up."}
		descriptor.Description += " response_mode=compact returns a concise receipt and optional diagnostics; evidence and refusal recovery remain available."
	}
	descriptor.InputSchema = withExperimentalProperties(descriptor.InputSchema, extra)
	return descriptor
}

func readByteProperties() map[string]any {
	return map[string]any{
		"max_bytes":            map[string]any{"type": "integer", "minimum": 4, "maximum": 1 << 20, "description": "Maximum UTF-8 content bytes per source target, including numbering. Metadata/JSON escaping are additional. Explicit opt-in; compact mode defaults to 65536. Does not bound acquisition memory."},
		"byte_offset":          map[string]any{"type": "integer", "minimum": 0, "description": "Zero-based byte offset into the same selected/rendered source. Nonzero offsets require expected_revision_id. Use returned next_byte_offset to preserve UTF-8 characters."},
		"expected_revision_id": stringSchema("Document revision from the preceding read; a changed document refuses continuation instead of mixing versions."),
	}
}

// TrimWireDiffs drops the per-file content hashes from an edit reply on the
// wire. They are two sha256 strings per changed file, about a third of an
// ordinary edit reply, and no caller acts on them: what changed is in
// locations, and the guard for the next call is the document revision beside
// it. The receipt this envelope was also recorded as keeps them, so
// revision_diff and the affected-test coverage still read them back, and a
// diff record that carries its patch (a preview, or verbose) is left alone
// because that patch is what the caller asked for.
func TrimWireDiffs(tool string, envelope map[string]any) map[string]any {
	if tool != "edit_apply" {
		return envelope
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		return envelope
	}
	diffs, present := data["diffs"].([]map[string]any)
	if !present || len(diffs) == 0 {
		return envelope
	}
	for _, diff := range diffs {
		if _, patched := diff["patch"]; patched {
			return envelope
		}
	}
	trimmed := CloneEnvelope(envelope)
	trimmedData := CloneEnvelope(data)
	delete(trimmedData, "diffs")
	trimmed["data"] = trimmedData
	return trimmed
}

// CompactReceipt changes presentation only. It must never change mutation
// execution, durable receipts, verification semantics or failure recovery.
func CompactReceipt(tool string, arguments, envelope map[string]any) map[string]any {
	if arguments["response_mode"] != "compact" || (tool != "edit_apply" && tool != "change_plan") {
		return envelope
	}
	if envelope["outcome"] != "ok" && envelope["outcome"] != "provisional" {
		return envelope
	}
	compact := CloneEnvelope(envelope)
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		return compact
	}
	if tool == "change_plan" {
		return compactPlanReceipt(compact, data, arguments)
	}
	receipt := map[string]any{}
	for _, key := range []string{"canonical_changed", "changed_paths", "revision", "from_revision", "document_revision", "document_revisions", "replacements", "verification", "applied_operations", "failed_operation", "original_canonical_changed", "replayed_request", "preview_only", "git", "relocated", "format"} {
		if value, exists := data[key]; exists {
			receipt[key] = value
		}
	}
	compact["data"] = receipt
	receipt["details_omitted"] = true
	if arguments["include_diagnostics"] == true {
		for _, key := range []string{"diagnostic_delta", "diagnostics"} {
			if value, exists := data[key]; exists {
				receipt[key] = value
			}
		}
	} else {
		delete(compact, "diagnostic_updates")
		delete(compact, "diagnostic_updates_truncated")
		receipt["diagnostics"] = "Inspect with diagnostics; verification status above is unchanged."
	}
	return compact
}
