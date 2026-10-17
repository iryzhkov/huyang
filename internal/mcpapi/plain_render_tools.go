package mcpapi

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Tool bodies of a plain reply. Each consumes the data fields it renders;
// whatever it leaves is printed as detail by the caller.

// read renders a source selection, an outline or a batch. History, changes
// and any other variant keep the conservative JSON rendering.
func (r *plainReply) read(fields *plainFields) {
	switch {
	case fields.has("files") && fields.has("entries"):
		r.readBatch(fields)
	case fields.has("sections"):
		r.outline(fields)
	case isString(fields.peek("content")):
		r.source(fields, -1)
	default:
		r.generic(fields)
	}
}

func isString(value any) bool {
	_, ok := value.(string)
	return ok
}

// source prints a header with the full guard and the delivered range, then
// the exact delivered text. batchIndex is the target's position in a batch,
// or -1: in a batch the continuation of a cut target is printed here,
// because a batch reply's top-level next holds only failures.
func (r *plainReply) source(fields *plainFields, batchIndex int) {
	path := plainString(fields.take("path"))
	content := plainString(fields.take("content"))
	header := plainPath(path)
	if name := plainString(fields.take("name_path")); name != "" {
		header += "#" + name
	}
	attributes := r.sourceGuard(fields)
	if kind := plainString(fields.take("kind")); kind != "" {
		attributes = append(attributes, "kind="+kind)
	}
	attributes = append(attributes, sourceRange(fields)...)
	if handle := plainString(fields.take("handle")); handle != "" {
		attributes = append(attributes, "handle="+handle)
	}
	if plainBool(fields.take("relocated")) {
		attributes = append(attributes, "relocated")
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		attributes = append(attributes, "no_final_newline")
	}
	r.linef("%s (%s)", header, strings.Join(attributes, "; "))
	r.block(content)
	if coverage := plainObject(fields.take("coverage")); coverage != nil {
		r.coverage(coverage)
	}
	continuation := plainObject(fields.take("continuation"))
	delivered, hasDelivered := plainInt(fields.take("delivered_end_line"))
	switch {
	case batchIndex >= 0:
		r.batchContinuation(batchIndex, continuation, delivered, hasDelivered)
	case len(plainUnknown(continuation, "byte_offset", "expected_revision_id", "max_bytes")) > 0:
		// The reply's next step repeats the known fields; a field it does
		// not know is shown rather than lost.
		r.linef("continuation: %s", plainJSON(continuation))
	}
	if plainBool(fields.take("implicit_workspace")) {
		r.linef("workspace id=%s (implicit documents workspace)", plainString(plainObject(r.envelope.peek("workspace"))["id"]))
	}
}

// batchContinuation prints the call that continues one cut target of a
// batch. Byte offsets and line numbers count within the target's own
// selection and numbering, so the call repeats the original target (path,
// symbol or handle selector, line window) with the batch's effective options
// and adds only the continuation; any other target would pass the revision
// guard and return unrelated bytes.
func (r *plainReply) batchContinuation(index int, continuation map[string]any, delivered int64, hasDelivered bool) {
	if continuation == nil && !hasDelivered {
		return
	}
	target, known := r.batchTarget(index)
	switch {
	case !known && continuation != nil:
		r.linef("continue: repeat target %d with the same selection and options, adding %s", index, plainPairs(continuation))
		return
	case !known:
		r.linef("continue: repeat target %d with the same selection and options from start_line=%d", index, delivered+1)
		return
	case continuation != nil:
		for key, value := range continuation {
			target[key] = value
		}
	default:
		delete(target, "byte_offset")
		target["start_line"] = delivered + 1
	}
	r.linef("continue: read targets=%s", plainJSON([]any{target}))
}

// batchTarget is target index of the call's targets with the call-level
// options a batch applies to every target that does not set its own.
func (r *plainReply) batchTarget(index int) (map[string]any, bool) {
	targets := plainList(r.arguments["targets"])
	if index < 0 || index >= len(targets) || plainObject(targets[index]) == nil {
		return nil, false
	}
	original := plainObject(targets[index])
	target := make(map[string]any, len(original)+4)
	for key, value := range original {
		target[key] = value
	}
	for _, key := range []string{"numbered", "max_lines", "max_bytes", "expected_revision_id"} {
		if _, own := target[key]; !own {
			if value, present := r.arguments[key]; present {
				target[key] = value
			}
		}
	}
	return target, true
}

// sourceGuard is the revision a later edit is guarded by. A prepared read
// names its prepared revision and plan instead, so staged bytes are never
// mistaken for canonical ones.
func (r *plainReply) sourceGuard(fields *plainFields) []string {
	revision := plainString(fields.take("revision_id"))
	planID := plainString(fields.take("plan_id"))
	planRevision := fields.take("plan_revision")
	if planID != "" || strings.HasPrefix(revision, "prep_") {
		guard := []string{"prepared_revision=" + revision, "plan_id=" + planID}
		if planRevision != nil {
			guard = append(guard, "plan_revision="+plainScalar(planRevision))
		}
		return guard
	}
	if revision == "" {
		return nil
	}
	return []string{"revision_id=" + revision}
}

// sourceRange is lines=START-END/TOTAL for what was delivered, the byte
// window when one applied, and which bound cut the delivery.
func sourceRange(fields *plainFields) []string {
	total, hasTotal := plainInt(fields.take("lines"))
	start, hasStart := plainInt(fields.take("start_line"))
	end, hasEnd := plainInt(fields.take("end_line"))
	truncated := plainBool(fields.take("truncated"))
	delivered, hasDelivered := plainInt(fields.peek("delivered_end_line"))
	var out []string
	switch {
	case hasTotal && total == 0 && !hasStart:
		out = append(out, "lines=0")
	case hasStart || hasTotal:
		if !hasStart {
			start = 1
		}
		if !hasEnd {
			end = total
		}
		if truncated && hasDelivered {
			end = delivered
		}
		clause := fmt.Sprintf("lines=%d-%d", start, end)
		if hasTotal {
			clause += fmt.Sprintf("/%d", total)
		}
		out = append(out, clause)
	}
	if offset, ok := plainInt(fields.take("byte_offset")); ok {
		byteEnd, _ := plainInt(fields.take("byte_end"))
		selection, _ := plainInt(fields.take("selection_bytes"))
		out = append(out, fmt.Sprintf("bytes=%d-%d/%d", offset, byteEnd, selection))
		for _, key := range []string{"delivered_bytes", "max_bytes", "next_byte_offset"} {
			fields.take(key)
		}
	}
	switch {
	case plainBool(fields.take("byte_truncated")):
		out = append(out, "truncated=max_bytes")
	case truncated:
		out = append(out, "truncated=max_lines")
	}
	return out
}

// readBatch prints one block per target in request order. A failed target
// is its own refusal block and never hides its siblings.
func (r *plainReply) readBatch(fields *plainFields) {
	entries := plainList(fields.take("entries"))
	files := plainList(fields.take("files"))
	if len(files) > 4 {
		r.manifest(entries)
	}
	for index, raw := range files {
		target := newPlainFields(plainObject(raw))
		switch {
		case target.has("code") && !target.has("content"):
			r.failedTarget(target)
		case target.has("sections"):
			r.outline(target)
		case isString(target.peek("content")):
			r.source(target, index)
		default:
			r.linef("%s: %s", plainPath(plainString(target.take("path"))), plainJSON(target.rest()))
			continue
		}
		r.detail("detail", target.rest())
	}
	// The entries repeat each target's size; a field beyond that is shown.
	var extra []any
	for index, raw := range entries {
		if left := plainUnknown(plainObject(raw), "path", "bytes", "lines", "name_path", "section_count", "truncated", "code"); len(left) > 0 {
			left["index"] = index
			extra = append(extra, left)
		}
	}
	if len(extra) > 0 {
		r.linef("entries detail: %s", plainJSON(extra))
	}
}

// manifest lists a large batch's targets first, so a client that shows only
// the head of a long reply still shows what it holds.
func (r *plainReply) manifest(entries []any) {
	var names []string
	for _, raw := range entries {
		entry := plainObject(raw)
		name := plainPath(plainString(entry["path"]))
		switch {
		case entry["code"] != nil:
			name += " (" + plainString(entry["code"]) + ")"
		case plainBool(entry["truncated"]):
			name += " (truncated)"
		}
		names = append(names, name)
	}
	r.linef("%d targets: %s", len(entries), strings.Join(names, ", "))
}

func (r *plainReply) failedTarget(target *plainFields) {
	header := fmt.Sprintf("%s: %s: %s", plainPath(plainString(target.take("path"))), plainString(target.take("code")), plainString(target.take("error")))
	r.line(header)
	said := r.said
	r.said = header
	next := plainList(target.take("next"))
	r.recovery(target)
	r.said = said
	r.next("next", next)
}

// outline prints one declaration per line with the handle that addresses it.
func (r *plainReply) outline(fields *plainFields) {
	path := plainPath(plainString(fields.take("path")))
	sections := plainList(fields.take("sections"))
	listed, _ := plainInt(fields.take("listed_count"))
	total, _ := plainInt(fields.take("declaration_count"))
	header := fmt.Sprintf("%s outline (%d of %d declarations", path, listed, total)
	if plainBool(fields.take("sections_truncated")) {
		header += "; truncated"
	}
	r.line(header + ")")
	for _, raw := range sections {
		section := newPlainFields(plainObject(raw))
		start, _ := plainInt(section.take("start_line"))
		end, _ := plainInt(section.take("end_line"))
		row := fmt.Sprintf("%d-%d %s %s", start, end, plainString(section.take("kind")), plainString(section.take("name")))
		if handle := plainString(section.take("handle")); handle != "" {
			row += " handle=" + handle
		}
		if left := section.rest(); len(left) > 0 {
			row += " " + plainJSON(left)
		}
		r.line(row)
	}
	if handle := plainString(fields.take("fallback_handle")); handle != "" {
		r.linef("whole document handle=%s (no declarations)", handle)
	}
	if fallback := fields.take("fallback_range"); fallback != nil {
		r.linef("fallback_range: %s", plainJSON(fallback))
	}
	if coverage := plainObject(fields.take("coverage")); coverage != nil {
		r.coverage(coverage)
	}
}

// search renders current-source hits grep style, or semantic locations.
func (r *plainReply) search(fields *plainFields) {
	switch {
	case plainObject(fields.peek("navigation")) != nil:
		r.navigation(newPlainFields(plainObject(fields.take("navigation"))))
	case fields.has("hits") && fields.has("returned"):
		r.hits(fields)
	default:
		r.generic(fields)
	}
}

// plainHitRows tracks what grep-style output already printed, so the
// context windows of neighbouring hits are merged rather than repeated.
type plainHitRows struct {
	path    string
	last    int64
	printed bool
	unknown int
	cut     bool
	// matched holds path:line for every line a hit matched.
	matched map[string]bool
}

// hitLineCount is how many source lines a hit's match spans.
func hitLineCount(hit map[string]any) int64 {
	if source, ok := hit["source_line"].(string); ok {
		return int64(strings.Count(source, "\n") + 1)
	}
	if match, ok := hit["match"].(string); ok {
		return int64(strings.Count(match, "\n") + 1)
	}
	return 1
}

// hits prints path:line:text for every hit, context lines path-line-text,
// and a footer with the counts, completeness and the set handle when a
// follow-up needs it.
func (r *plainReply) hits(fields *plainFields) {
	rows := &plainHitRows{matched: map[string]bool{}}
	wantHandles := plainBool(r.arguments["include_handles"])
	hits := plainList(fields.take("hits"))
	// Every matched line is known before any context is printed, so a line
	// one hit's window shows and another hit matched is printed as a match.
	for _, raw := range hits {
		hit := plainObject(raw)
		path := plainPath(plainString(hit["path"]))
		line, _ := plainInt(hit["line"])
		for offset := int64(0); offset < hitLineCount(hit); offset++ {
			rows.matched[path+":"+strconv.FormatInt(line+offset, 10)] = true
		}
	}
	for _, raw := range hits {
		r.hit(newPlainFields(plainObject(raw)), rows)
	}
	if rows.unknown > 0 {
		r.linef("note: line text unavailable for %d hit(s): the document changed after the search; read it to see the current text", rows.unknown)
	}
	if rows.cut {
		r.linef("note: lines longer than %d bytes are cut around the match ([...] marks the cut)", MaxPlainLineBytes)
	}
	r.searchFooter(fields, wantHandles)
}

func (r *plainReply) hit(hit *plainFields, rows *plainHitRows) {
	path := plainPath(plainString(hit.take("path")))
	line, _ := plainInt(hit.take("line"))
	match, hasMatch := hit.take("match").(string)
	source, hasSource := hit.take("source_line").(string)
	if plainBool(hit.take("source_line_cut")) {
		rows.cut = true
	}
	if context, ok := hit.take("context").(string); ok {
		r.contextRows(path, line, context, source, hasSource, match, hasMatch, rows)
	} else if hasSource {
		for index, text := range strings.Split(source, "\n") {
			r.linef("%s:%d:%s", path, line+int64(index), text)
		}
	} else if hasMatch {
		r.linef("%s:%d: (matched text) %s", path, line, match)
		rows.unknown++
	} else {
		r.linef("%s:%d", path, line)
		rows.unknown++
	}
	var extra []string
	if handle := plainString(hit.take("handle")); handle != "" {
		extra = append(extra, "handle="+handle)
	}
	if column, ok := plainInt(hit.take("column")); ok {
		extra = append(extra, "column="+strconv.FormatInt(column, 10))
	}
	if left := hit.rest(); len(left) > 0 {
		extra = append(extra, plainJSON(left))
	}
	if len(extra) > 0 {
		r.line("  " + strings.Join(extra, " "))
	}
}

// contextRows prints a hit's context window grep -C style, merged with the
// hit's whole matched span: the handler bounds context around the line the
// match starts on, so the lines of a multi-line match past that window come
// from the hit's source lines. Matched lines use colons, the others
// hyphens; lines already printed are skipped and a -- separates windows
// that do not touch. A matched tail with no text available is reported.
func (r *plainReply) contextRows(path string, line int64, context, source string, hasSource bool, match string, hasMatch bool, rows *plainHitRows) {
	window := map[int64]string{}
	for _, row := range strings.Split(strings.TrimSuffix(context, "\n"), "\n") {
		numberText, text, _ := strings.Cut(row, "\t")
		number, err := strconv.ParseInt(numberText, 10, 64)
		if err != nil {
			r.line(row)
			continue
		}
		window[number] = text
	}
	if hasSource {
		for index, text := range strings.Split(source, "\n") {
			if _, shown := window[line+int64(index)]; !shown {
				window[line+int64(index)] = text
			}
		}
	}
	numbers := make([]int64, 0, len(window))
	for number := range window {
		numbers = append(numbers, number)
	}
	slices.Sort(numbers)
	for _, number := range numbers {
		if rows.printed && rows.path == path && number <= rows.last {
			continue
		}
		if rows.printed && (rows.path != path || number > rows.last+1) {
			r.line("--")
		}
		separator := "-"
		if rows.matched[path+":"+strconv.FormatInt(number, 10)] {
			separator = ":"
		}
		r.line(path + separator + strconv.FormatInt(number, 10) + separator + window[number])
		rows.path, rows.last, rows.printed = path, number, true
	}
	if !hasSource && hasMatch {
		last := line + int64(strings.Count(match, "\n"))
		if _, shown := window[last]; !shown {
			r.linef("  match continues to line %d; its text past the context is not shown, read the file for it", last)
		}
	}
}

// searchFooter states how many matches there are and how complete the
// answer is. The result-set handle is printed whenever a follow-up needs
// it: more matches than shown, an incomplete or refined set, or a caller
// who asked for handles.
func (r *plainReply) searchFooter(fields *plainFields, wantHandles bool) {
	returned, _ := plainInt(fields.take("returned"))
	total, _ := plainInt(fields.take("total"))
	lowerBound := plainBool(fields.take("total_is_lower_bound"))
	limit, hasLimit := plainInt(fields.take("match_limit"))
	set := newPlainFields(plainObject(fields.take("result_set")))
	coverage := plainObject(fields.take("coverage"))
	of := strconv.FormatInt(total, 10)
	if lowerBound {
		of = "at least " + of
	}
	counts := of + plural(total, " match", " matches")
	if returned < total || lowerBound {
		counts = fmt.Sprintf("%d of %s matches", returned, of)
	}
	if files, ok := plainInt(set.take("file_count")); ok {
		counts += fmt.Sprintf(" in %d%s", files, plural(files, " file", " files"))
	}
	set.take("match_count")
	complete, known := set.take("complete").(bool)
	incomplete := (known && !complete) || coverage != nil || lowerBound
	parts := []string{counts, "complete"}
	if incomplete {
		parts[1] = "incomplete"
	}
	if hasLimit {
		parts = append(parts, fmt.Sprintf("match_limit=%d", limit))
	}
	eligible, eligibleKnown := set.take("all_matches_eligible").(bool)
	if eligibleKnown && !eligible {
		parts = append(parts, "all_matches_eligible=false")
	}
	parent := plainString(set.take("parent"))
	if parent != "" {
		parts = append(parts, fmt.Sprintf("refined from %s: retained %s, eliminated %s", parent, plainScalar(set.take("retained")), plainScalar(set.take("eliminated"))))
	}
	handle := plainString(set.take("handle"))
	if handle != "" && (returned < total || incomplete || parent != "" || wantHandles || (eligibleKnown && !eligible)) {
		parts = append(parts, "result_set="+handle)
	}
	if left := set.rest(); len(left) > 0 {
		parts = append(parts, "result_set detail "+plainJSON(left))
	}
	r.line(strings.Join(parts, "; "))
	if coverage != nil {
		r.coverage(coverage)
	}
}

// navigation prints semantic locations and call rows, then its counts, caps
// and notes, which are what tell a complete answer from a capped one.
func (r *plainReply) navigation(navigation *plainFields) {
	for _, raw := range plainList(navigation.take("locations")) {
		location := newPlainFields(plainObject(raw))
		row := plainPath(plainString(location.take("file"))) + ":" + plainScalar(location.take("line"))
		if end := location.take("end_line"); end != nil {
			row += "-" + plainScalar(end)
		}
		row += ":" + plainString(location.take("text"))
		if left := location.rest(); len(left) > 0 {
			row += " " + plainJSON(left)
		}
		r.line(row)
	}
	for _, raw := range plainList(navigation.take("calls")) {
		call := newPlainFields(plainObject(raw))
		row := fmt.Sprintf("%s (%s) %s:%s", plainString(call.take("name")), plainString(call.take("kind")),
			plainPath(plainString(call.take("file"))), plainScalar(call.take("line")))
		if sites := call.take("call_sites"); sites != nil {
			row += " call_sites=" + plainJSON(sites)
		}
		if left := call.rest(); len(left) > 0 {
			row += " " + plainJSON(left)
		}
		r.line(row)
	}
	r.keyValues(navigation)
}

// edit renders an edit receipt: the action, the per-path document revisions
// and the workspace revision, then what else the edit did, the diagnostic
// verdict and its findings. A preview, a replay and an applied edit are
// worded so none can be mistaken for another.
func (r *plainReply) edit(fields *plainFields) {
	if !fields.has("canonical_changed") && !fields.has("replayed_request") {
		// Not a receipt this renderer knows; say what it is in JSON.
		r.generic(fields)
		return
	}
	fields.take("late_evidence_batches")
	fields.take("preview_only")
	replay := plainBool(fields.take("replayed_request"))
	original := fields.take("original_canonical_changed")
	changed, _ := fields.take("canonical_changed").(bool)
	paths, _ := plainStrings(fields.take("changed_paths"))
	revision := plainString(fields.take("revision"))
	from := plainString(fields.take("from_revision"))
	guards := editGuards(paths, fields.take("document_revision"), fields.take("document_revisions"))
	count := ""
	if replacements, ok := plainInt(fields.take("replacements")); ok {
		count = fmt.Sprintf("; replacements=%d", replacements)
	}
	verb, previewVerb := editVerbs(r.arguments)
	switch {
	case replay:
		r.linef("Replayed original receipt; no new mutation (original revision=%s; from_revision=%s; original_canonical_changed=%s)",
			revision, from, plainScalar(original))
		r.guardLines(paths, guards, " (as of the original edit)")
		r.line("Current revision unconfirmed; refresh before another mutation.")
	case !changed:
		r.linef("Preview: %s %s (revision=%s; canonical_changed=false%s)", previewVerb, strings.Join(paths, ", "), revision, count)
	case len(paths) == 1 && len(guards) <= 1 && (len(guards) == 0 || guards[0].path == paths[0] || guards[0].path == ""):
		guard := ""
		if len(guards) == 1 {
			guard = "; revision_id=" + guards[0].revision
		}
		r.linef("%s %s (revision=%s%s%s)", verb, plainPath(paths[0]), revision, guard, count)
	default:
		r.linef("%s %d paths (revision=%s%s)", verb, len(paths), revision, count)
		r.guardLines(paths, guards, "")
	}
	r.editExtras(fields)
	r.verification(fields.take("verification"))
}

// plainGuard is one resulting document revision of an edit.
type plainGuard struct{ path, revision string }

// editGuards reads the scalar or per-path document revisions in changed
// path order. A scalar revision belongs to the only changed path.
func editGuards(paths []string, single, many any) []plainGuard {
	var guards []plainGuard
	if revision := plainString(single); revision != "" {
		path := ""
		if len(paths) == 1 {
			path = paths[0]
		}
		guards = append(guards, plainGuard{path, revision})
	}
	revisions := plainObject(many)
	seen := map[string]bool{}
	for _, path := range paths {
		if revision, ok := revisions[path]; ok {
			guards = append(guards, plainGuard{path, plainString(revision)})
			seen[path] = true
		}
	}
	for _, path := range plainSortedKeys(revisions) {
		if !seen[path] {
			guards = append(guards, plainGuard{path, plainString(revisions[path])})
		}
	}
	return guards
}

// guardLines prints each changed path with its document revision, and any
// revision for a path the changed list does not name.
func (r *plainReply) guardLines(paths []string, guards []plainGuard, suffix string) {
	byPath := map[string]string{}
	for _, guard := range guards {
		byPath[guard.path] = guard.revision
	}
	for _, path := range paths {
		if revision, ok := byPath[path]; ok {
			r.linef("  %s revision_id=%s%s", plainPath(path), revision, suffix)
			delete(byPath, path)
		} else {
			r.linef("  %s (no document revision: removed or not a text document)", plainPath(path))
		}
	}
	for _, guard := range guards {
		if revision, ok := byPath[guard.path]; ok {
			label := guard.path
			if label == "" {
				label = "(document)"
			}
			r.linef("  %s revision_id=%s%s", plainPath(label), revision, suffix)
			delete(byPath, guard.path)
		}
	}
}

// editVerbs names the action from the operation the caller sent.
func editVerbs(arguments map[string]any) (string, string) {
	if operations := plainList(arguments["operations"]); len(operations) > 0 {
		return fmt.Sprintf("Applied %d operations to", len(operations)), fmt.Sprintf("%d operations on", len(operations))
	}
	operation := plainObject(arguments["operation"])
	switch plainString(operation["kind"]) {
	case "create_file":
		if plainBool(operation["replace"]) {
			return "Replaced", "replace"
		}
		return "Created", "create"
	case "move_file":
		return "Moved", "move"
	case "copy_file":
		return "Copied", "copy"
	case "delete_file":
		return "Deleted", "delete"
	case "replace_literal", "replace_range":
		return "Edited", "edit"
	}
	return "Changed", "change"
}

// editExtras prints what else an edit did: locations, formatting, Git
// state, a copy's source, a relocation, requested patches, and the verbose
// records when they were asked for.
func (r *plainReply) editExtras(fields *plainFields) {
	if locations, ok := plainStrings(fields.peek("locations")); ok && len(locations) > 0 {
		fields.take("locations")
		r.linef("at %s", strings.Join(locations, ", "))
	}
	for _, key := range []string{"format", "source", "relocated"} {
		if value := plainObject(fields.peek(key)); value != nil {
			fields.take(key)
			r.linef("%s: %s", key, plainPairs(value))
		}
	}
	if git := plainObject(fields.peek("git")); git != nil {
		fields.take("git")
		r.linef("git: %s (Huyang does not touch the Git index)", plainPairs(git))
	}
	verbose := plainBool(r.arguments["verbose"])
	for _, raw := range plainList(fields.take("diffs")) {
		diff := newPlainFields(plainObject(raw))
		path := plainString(diff.take("path"))
		before, after := diff.take("before_sha256"), diff.take("after_sha256")
		if verbose {
			r.linef("diff %s before_sha256=%s after_sha256=%s", plainPath(path), plainScalar(before), plainScalar(after))
		}
		if patch, ok := diff.take("patch").(string); ok {
			r.block(patch)
		}
		r.detail("diff detail", diff.rest())
	}
	if tools := plainList(fields.peek("tool_delta")); fields.has("tool_delta") && len(tools) == 0 {
		fields.take("tool_delta")
	}
	for _, key := range []string{"change", "resolution", "tool_delta"} {
		if fields.has(key) {
			r.linef("%s: %s", key, plainJSON(fields.take(key)))
		}
	}
	if plainBool(fields.take("implicit_workspace")) {
		r.linef("workspace id=%s (implicit documents workspace)", plainString(plainObject(r.envelope.peek("workspace"))["id"]))
	}
}

// verification states an incomplete diagnostic verdict on every reply that
// has one, including when its reasons repeat the previous reply's.
func (r *plainReply) verification(value any) {
	verification := newPlainFields(plainObject(value))
	if len(verification.values) == 0 {
		return
	}
	confidence := plainString(verification.take("confidence"))
	var reasons []string
	for _, reason := range plainList(verification.take("reasons")) {
		reasons = append(reasons, plainScalar(reason))
	}
	var codes []string
	for _, code := range plainList(verification.take("reason_codes")) {
		codes = append(codes, plainScalar(code))
	}
	text := "diagnostics " + confidence
	if plainBool(verification.take("reasons_unchanged")) {
		text += " (reasons unchanged since your last reply)"
	}
	if len(reasons) > 0 {
		text += ": " + strings.Join(reasons, "; ")
	}
	if len(codes) > 0 {
		text += " [" + strings.Join(codes, ", ") + "]"
	}
	if left := verification.rest(); len(left) > 0 {
		text += " " + plainJSON(left)
	}
	r.line(text)
}

// plainPairs writes an object as key=value pairs, scalars bare.
func plainPairs(value map[string]any) string {
	parts := make([]string, 0, len(value))
	for _, key := range plainSortedKeys(value) {
		parts = append(parts, key+"="+plainScalar(value[key]))
	}
	return strings.Join(parts, " ")
}

// workspaceOpen renders the first overview of a session briefly: identity
// and revision, the top-level names, the commands and their trust state,
// and any capability that is not healthy. A repeated open is one line.
func (r *plainReply) workspaceOpen(fields *plainFields) {
	identity := plainObject(r.envelope.peek("workspace"))
	root := plainPath(plainString(identity["root"]))
	revision := plainString(fields.take("revision"))
	if plainBool(fields.take("overview_repeated")) {
		count, _ := plainInt(fields.take("entry_count"))
		r.linef("Workspace %s (id=%s; revision=%s; %d entries; overview unchanged)", root, plainString(identity["id"]), revision, count)
		return
	}
	overview := newPlainFields(plainObject(fields.take("overview")))
	count := ""
	if entries, ok := plainInt(overview.take("entry_count")); ok {
		count = fmt.Sprintf("; %d entries", entries)
	}
	r.linef("Workspace %s (id=%s; revision=%s; kind=%s%s)", root, plainString(identity["id"]), revision, plainString(identity["kind"]), count)
	r.topLevel(overview)
	if commands := plainObject(fields.peek("commands")); commands != nil {
		fields.take("commands")
		r.commands(newPlainFields(commands))
	}
	if capabilities := plainObject(fields.peek("capabilities")); capabilities != nil {
		fields.take("capabilities")
		r.capabilities(newPlainFields(capabilities))
	}
	if provider := newPlainFields(plainObject(fields.take("semantic_provider"))); len(provider.values) > 0 {
		text := "provider: " + plainString(provider.take("backend")) + " " + plainString(provider.take("state"))
		if failure := plainString(provider.take("failure_code")); failure != "" {
			text += " (" + failure + ")"
		}
		if left := provider.rest(); len(left) > 0 {
			text += " " + plainJSON(left)
		}
		r.line(text)
	}
	if limits, ok := fields.peek("service_limits").(map[string]any); ok {
		fields.take("service_limits")
		if left := plainUnknown(limits, "tool_call_timeout_ms"); len(left) > 0 {
			r.linef("service limits detail: %s", plainJSON(left))
		}
	}
	if commits, ok := fields.peek("recent_commits").(map[string]any); ok {
		fields.take("recent_commits")
		r.recentCommits(commits)
	}
}

// recentCommits leaves the commit list out of a plain overview, as
// orientation an agent rarely acts on, but reports a coverage limitation on
// it and any field it does not know.
func (r *plainReply) recentCommits(commits map[string]any) {
	if commits["coverage"] != nil {
		r.linef("recent commits coverage: %s", plainJSON(commits["coverage"]))
	}
	left := plainUnknown(commits, "commits", "coverage")
	for _, raw := range plainList(commits["commits"]) {
		if len(plainUnknown(plainObject(raw), "abbreviated_id", "subject")) > 0 {
			left["commits"] = commits["commits"]
			break
		}
	}
	if len(left) > 0 {
		r.linef("recent commits detail: %s", plainJSON(left))
	}
}

// topLevel prints the top-level names (directories with a slash), or the
// bounded entry listing of a full overview, which says how much it shows.
func (r *plainReply) topLevel(overview *plainFields) {
	var names []string
	for _, raw := range plainList(overview.take("top_level")) {
		entry := plainObject(raw)
		name := plainPath(plainString(entry["path"]))
		if entry["kind"] == "directory" {
			name += "/"
		}
		// A directory's file count and a file's kind are summarised away;
		// any other field is kept beside the name.
		if left := plainUnknown(entry, "path", "kind", "files"); len(left) > 0 {
			name += plainJSON(left)
		}
		names = append(names, name)
	}
	if len(names) > 0 {
		text := "top level: " + strings.Join(names, " ")
		if plainBool(overview.take("top_level_truncated")) {
			text += fmt.Sprintf(" (truncated; %s top-level entries)", plainScalar(overview.peek("top_level_count")))
		}
		r.line(text)
	}
	overview.take("top_level_count")
	overview.take("top_level_truncated")
	overview.take("workspace")
	if entries := plainList(overview.take("entries")); entries != nil {
		truncated := plainBool(overview.take("entries_truncated"))
		r.linef("entries (%d listed%s):", len(entries), map[bool]string{true: "; truncated, the listing is not complete", false: ""}[truncated])
		for _, raw := range entries {
			entry := newPlainFields(plainObject(raw))
			row := plainPath(plainString(entry.take("path")))
			if left := entry.rest(); len(left) > 0 {
				row += " " + plainPairs(left)
			}
			r.line(row)
		}
	}
	if coverage := plainObject(overview.take("coverage")); coverage != nil {
		r.coverage(coverage)
	}
	r.detail("overview detail", overview.rest())
}

// commands prints the verification commands with their source and trust
// state; configured but untrusted commands are never presented as runnable.
func (r *plainReply) commands(commands *plainFields) {
	source := plainString(commands.take("source"))
	state := plainString(commands.take("state"))
	commands.take("trusted")
	header := "commands (" + strings.Join(nonEmpty(source, state), "; ") + ")"
	if detected := commands.take("detected_from"); detected != nil {
		header += " detected from " + plainScalar(detected)
	}
	var lists []string
	for _, key := range []string{"check", "tests"} {
		if values, ok := plainStrings(commands.peek(key)); ok {
			commands.take(key)
			if len(values) > 0 {
				lists = append(lists, key+": "+strings.Join(values, ", "))
			}
		}
	}
	if len(lists) > 0 {
		header += ": " + strings.Join(lists, "; ")
	}
	r.line(header)
	if gate := commands.take("format_gate"); gate != nil {
		r.linef("format gate: %s", plainScalar(gate))
	}
	for _, key := range []string{"enable", "hint", "run", "override"} {
		if value := commands.take(key); value != nil {
			r.linef("%s: %s", key, plainScalar(value))
		}
	}
	r.detail("commands detail", commands.rest())
}

// capabilities prints the semantic coverage, every capability reported as
// not available with its state, and any failure.
func (r *plainReply) capabilities(capabilities *plainFields) {
	parts := []string{}
	if semantic := capabilities.take("semantic"); semantic != nil {
		parts = append(parts, "semantic="+plainScalar(semantic))
	}
	if missing := plainObject(capabilities.take("not_available")); len(missing) > 0 {
		parts = append(parts, "not available: "+plainPairs(missing))
	}
	if len(parts) > 0 {
		r.line("capabilities: " + strings.Join(parts, "; "))
	}
	if failures := capabilities.take("failures"); failures != nil && len(plainList(failures)) > 0 {
		r.linef("capability failures: %s", plainJSON(failures))
	}
	r.detail("capabilities detail", capabilities.rest())
}

func plural(count int64, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
