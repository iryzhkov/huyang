package mcpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Plain replies: the model-facing presentation of a finalized envelope.
//
// The canonical envelope, the durable receipt and the replay record are not
// touched; this is the last step before the transport, and it reads the same
// structure the JSON style would send. The renderer keeps a ledger of the
// fields it consumed: a field it does not know (a new data key, a new nested
// variant, a new envelope key) is appended as an explicit JSON detail line
// rather than dropped, so recovery, guards and findings cannot vanish because
// a renderer predates them. Findings, provisional and unavailable reasons,
// truncation, replay status and every guard token (full revision_id, wsrev,
// handles, result-set handles, prepared identities, refusal codes and next
// steps) are always printed.

// RenderPlain renders an envelope as the one text block of a plain reply.
// arguments are the call's normalized arguments; they select wording only
// (the action verb of an edit, whether handles were asked for).
func RenderPlain(tool string, arguments, envelope map[string]any) (string, error) {
	normalized, err := plainNormalize(tool, envelope)
	if err != nil {
		return "", err
	}
	reply := &plainReply{tool: tool, arguments: arguments, envelope: newPlainFields(normalized)}
	reply.render()
	return reply.text(), nil
}

// plainNormalize turns the envelope into the generic JSON form the JSON
// style would carry in structuredContent, so every value has one shape
// whatever Go type a handler used. Numbers stay exact.
func plainNormalize(tool string, envelope map[string]any) (map[string]any, error) {
	structured := CompactStructuredEnvelope(TrimWireDiffs(tool, envelope))
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(structured); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(&buffer)
	decoder.UseNumber()
	var normalized map[string]any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

// plainFields is one object being rendered and the keys already consumed.
type plainFields struct {
	values map[string]any
	used   map[string]bool
}

func newPlainFields(values map[string]any) *plainFields {
	if values == nil {
		values = map[string]any{}
	}
	return &plainFields{values: values, used: map[string]bool{}}
}

// take returns a value and marks it consumed.
func (f *plainFields) take(key string) any {
	f.used[key] = true
	return f.values[key]
}

// peek returns a value without consuming it.
func (f *plainFields) peek(key string) any { return f.values[key] }

func (f *plainFields) has(key string) bool {
	_, ok := f.values[key]
	return ok
}

// rest is every key not consumed, for the detail line.
func (f *plainFields) rest() map[string]any {
	left := map[string]any{}
	for key, value := range f.values {
		if !f.used[key] {
			left[key] = value
		}
	}
	return left
}

type plainReply struct {
	tool      string
	arguments map[string]any
	envelope  *plainFields
	out       strings.Builder
	findings  plainFindings
	// summaryShown records that the body already printed the summary.
	summaryShown bool
	// said is the refusal line just printed; a recovery value it already
	// quotes verbatim is not repeated.
	said string
}

func (r *plainReply) line(text string) {
	r.out.WriteString(text)
	r.out.WriteByte('\n')
}

func (r *plainReply) linef(format string, values ...any) { r.line(fmt.Sprintf(format, values...)) }

// block writes text that is content, not presentation, ending it with a
// newline so the next line is never read as part of it.
func (r *plainReply) block(text string) {
	r.out.WriteString(text)
	if text != "" && !strings.HasSuffix(text, "\n") {
		r.out.WriteByte('\n')
	}
}

// text is the reply. Every line, content included, ends with a newline, so
// trailing blank lines of a document survive exactly.
func (r *plainReply) text() string { return r.out.String() }

// refused reports an outcome that carries no result, only a refusal.
func (r *plainReply) refused() bool {
	switch plainString(r.envelope.peek("outcome")) {
	case "failed", "conflict", "unavailable":
		return true
	}
	return false
}

func (r *plainReply) render() {
	r.envelope.take("api_version")
	// The guide restates the tool descriptions; plain replies leave it out.
	r.envelope.take("guide")
	data, isMap := r.envelope.take("data").(map[string]any)
	fields := newPlainFields(data)
	// A diagnostic delta is merged with the notice stream on every reply,
	// whichever renderer handles the rest of it: a refusal, a batch or a
	// tool without a dedicated renderer prints its findings the same way.
	var unknownDelta map[string]any
	if plainObject(fields.peek("diagnostic_delta")) != nil {
		unknownDelta = r.findings.addDelta(fields.take("diagnostic_delta"))
	}
	switch {
	case r.refused():
		r.refusal(fields)
	case !isMap && r.envelope.peek("data") != nil:
		r.generic(fields)
		r.linef("data: %s", plainJSON(r.envelope.peek("data")))
	case r.tool == "read":
		r.read(fields)
	case r.tool == "search":
		r.search(fields)
	case r.tool == "edit_apply":
		r.edit(fields)
	case r.tool == "workspace_open":
		r.workspaceOpen(fields)
	default:
		r.generic(fields)
	}
	r.detail("diagnostic_delta detail", unknownDelta)
	r.detail("detail", fields.rest())
	r.suffix()
}

// generic is the conservative rendering of a tool or variant without a
// dedicated renderer: its summary, then its whole data as compact JSON.
func (r *plainReply) generic(fields *plainFields) {
	summary := plainString(r.envelope.take("summary"))
	r.summaryShown = true
	if outcome := plainString(r.envelope.peek("outcome")); outcome != "ok" && outcome != "" {
		summary = outcome + ": " + summary
	}
	r.line(summary)
	if left := fields.rest(); len(left) > 0 {
		r.linef("data: %s", plainJSON(left))
		for key := range left {
			fields.take(key)
		}
	}
}

// detail prints unconsumed fields as one JSON line, so nothing a renderer
// does not know is lost.
func (r *plainReply) detail(label string, left map[string]any) {
	if len(left) == 0 {
		return
	}
	r.linef("%s: %s", label, plainJSON(left))
}

// suffix is shared by every reply: status the body did not print, findings,
// warnings, evidence, unknown envelope fields and the next step.
func (r *plainReply) suffix() {
	envelope := r.envelope
	outcome := plainString(envelope.take("outcome"))
	summary := plainString(envelope.take("summary"))
	code := plainString(envelope.take("code"))
	if !r.summaryShown && outcome != "ok" && outcome != "" {
		label := outcome
		if code != "" {
			label = code
		}
		r.linef("%s: %s", label, summary)
	} else if !r.summaryShown && code != "" {
		r.linef("%s: %s", code, summary)
	}
	r.status()
	r.findings.add("", envelope.take("diagnostic_updates"))
	r.findings.render(r)
	if plainBool(envelope.take("diagnostic_updates_truncated")) {
		r.line("findings truncated: more diagnostic changes than one reply carries; read them with diagnostics")
	}
	for _, warning := range plainList(envelope.take("warnings")) {
		r.linef("warning: %s", plainString(warning))
	}
	// Evidence IDs address diagnostic and failure evidence; an applied edit
	// with an authoritative verdict and no findings has nothing to look up.
	evidence := envelope.take("evidence")
	if r.tool != "edit_apply" || outcome != "ok" || len(r.findings.items) > 0 || len(r.findings.resolved) > 0 {
		r.evidence(evidence)
	}
	workspace := newPlainFields(plainObject(envelope.take("workspace")))
	workspace.take("id")
	workspace.take("revision")
	for _, key := range []string{"kind", "root", "epoch", "state_seq"} {
		workspace.take(key)
	}
	if left := workspace.rest(); len(left) > 0 {
		r.linef("workspace: %s", plainJSON(left))
	}
	next := envelope.take("next")
	r.detail("detail", envelope.rest())
	r.next("next", plainList(next))
}

// status prints the envelope facts that change what the reply means:
// transaction state, a replayed receipt, a receipt that was not persisted,
// and the request ID where support would need it.
func (r *plainReply) status() {
	envelope := r.envelope
	if transaction := newPlainFields(plainObject(envelope.take("transaction"))); len(transaction.values) > 0 {
		text := fmt.Sprintf("transaction %s state=%s", plainString(transaction.take("id")), plainString(transaction.take("state")))
		if left := transaction.rest(); len(left) > 0 {
			text += " " + plainJSON(left)
		}
		r.line(text)
	}
	if plainString(envelope.take("idempotency")) == "replayed" && r.tool != "edit_apply" {
		r.line("Replayed original receipt; this call made no new mutation")
	}
	persisted, present := envelope.take("idempotency_persisted").(bool)
	if present && !persisted {
		r.line("receipt not persisted: a retry with the same idempotency_key is not protected; inspect the current revision before retrying")
	}
	requestID := plainString(envelope.take("request_id"))
	code := plainString(envelope.peek("code"))
	if requestID != "" && (code == "request_timeout" || code == "mutation_receipt_persist_failed" ||
		(present && !persisted) || (r.refused() && code == "")) {
		r.linef("request_id=%s", requestID)
	}
	if retryable, ok := envelope.take("retryable").(bool); ok && !r.refused() {
		r.linef("retryable=%t", retryable)
	}
}

func (r *plainReply) evidence(value any) {
	evidence := newPlainFields(plainObject(value))
	var ids []string
	for _, id := range plainList(evidence.take("ids")) {
		ids = append(ids, plainString(id))
	}
	truncated := plainBool(evidence.take("truncated"))
	left := evidence.rest()
	if len(ids) == 0 && !truncated && len(left) == 0 {
		return
	}
	text := "evidence: " + strings.Join(ids, ", ")
	if truncated {
		text += " (truncated)"
	}
	if len(left) > 0 {
		text += " " + plainJSON(left)
	}
	r.line(text)
}

// plainUnknown is the part of a nested record outside the keys a renderer
// prints or deliberately summarises, so a field added later still shows.
func plainUnknown(value map[string]any, known ...string) map[string]any {
	left := map[string]any{}
	for key, item := range value {
		if !slices.Contains(known, key) {
			left[key] = item
		}
	}
	return left
}

// next prints the recovery steps as one line of copyable calls.
func (r *plainReply) next(label string, steps []any) {
	var rendered []string
	for _, step := range steps {
		if text := plainStep(step); text != "" {
			rendered = append(rendered, text)
		}
	}
	if len(rendered) > 0 {
		r.linef("%s: %s", label, strings.Join(rendered, "; "))
	}
}

// plainStep renders one next entry as tool key=value ... (action) - note.
// Values are JSON so the step can be copied into a call; read's path is
// written as target.path, the argument read takes.
func plainStep(step any) string {
	entry, ok := step.(map[string]any)
	if !ok {
		return plainScalar(step)
	}
	tool := plainString(entry["tool"])
	action := plainString(entry["action"])
	note := plainString(entry["note"])
	var parts []string
	if tool != "" {
		parts = append(parts, tool)
	}
	for _, key := range plainSortedKeys(entry) {
		if key == "tool" || key == "action" || key == "note" {
			continue
		}
		name := key
		if tool == "read" && key == "path" {
			name = "target.path"
		}
		parts = append(parts, name+"="+plainValue(entry[key]))
	}
	text := strings.Join(parts, " ")
	if action != "" {
		if text == "" {
			text = action
		} else {
			text += " (" + action + ")"
		}
	}
	if note != "" {
		text += " - " + note
	}
	return text
}

// refusal is a reply with no result: code and message, the recovery data
// the refusal carries, and (from suffix) the next step.
func (r *plainReply) refusal(fields *plainFields) {
	code := plainString(r.envelope.take("code"))
	summary := plainString(r.envelope.take("summary"))
	outcome := plainString(r.envelope.take("outcome"))
	if code == "" {
		code = outcome
	}
	header := code + ": " + summary
	if retryable, ok := r.envelope.take("retryable").(bool); ok {
		header += fmt.Sprintf(" (retryable=%t)", retryable)
	}
	r.line(header)
	r.summaryShown = true
	r.said = header
	if r.tool == "edit_apply" {
		r.batchEffect(fields)
		if revision := plainString(plainObject(r.envelope.peek("workspace"))["revision"]); revision != "" {
			r.linef("workspace revision=%s", revision)
		}
	}
	r.recovery(fields)
}

// recovery prints the data a refusal carries for its repair: candidates,
// the actual text of a whitespace mismatch, locations, counts, and every
// other field as key=value or JSON.
func (r *plainReply) recovery(fields *plainFields) {
	if actual, ok := fields.peek("actual").(string); ok {
		fields.take("actual")
		r.line("actual:")
		r.block(actual)
	}
	for _, key := range []string{"candidates", "locations"} {
		if values, ok := plainStrings(fields.peek(key)); ok && len(values) > 0 {
			fields.take(key)
			r.linef("%s: %s", key, strings.Join(values, ", "))
		}
	}
	if coverage := plainObject(fields.peek("coverage")); coverage != nil {
		fields.take("coverage")
		r.coverage(coverage)
	}
	r.keyValues(fields)
}

// keyValues prints the remaining fields: scalars on one key=value line,
// structures as one JSON line each.
func (r *plainReply) keyValues(fields *plainFields) {
	var scalars []string
	for _, key := range plainSortedKeys(fields.values) {
		if fields.used[key] {
			continue
		}
		switch value := fields.values[key].(type) {
		case map[string]any:
			if len(value) > 0 {
				r.linef("%s: %s", key, plainJSON(value))
			}
		case []any:
			if len(value) > 0 {
				r.linef("%s: %s", key, plainJSON(value))
			}
		case string:
			// A path or name the refusal line already quotes is not repeated.
			if len(value) < 3 || !strings.Contains(r.said, value) {
				scalars = append(scalars, key+"="+plainValue(value))
			}
		default:
			scalars = append(scalars, key+"="+plainValue(value))
		}
		fields.take(key)
	}
	if len(scalars) > 0 {
		r.line(strings.Join(scalars, " "))
	}
}

// coverage prints a coverage record only when it limits the claim: an
// incomplete, capped or skipping walk. A complete one says nothing new.
func (r *plainReply) coverage(coverage map[string]any) {
	complete, known := coverage["complete"].(bool)
	clean := known && complete && !plainBool(coverage["capped"]) && len(plainList(coverage["skipped"])) == 0
	unknown := plainUnknown(coverage, "complete", "files_considered", "files_read", "bytes_read", "skipped",
		"skipped_count", "symlinks_skipped", "capped", "semantic")
	switch {
	case !clean:
		r.linef("coverage incomplete: %s", plainJSON(coverage))
	case len(unknown) > 0:
		// Complete, but carrying a field this renderer does not know.
		r.linef("coverage: %s", plainJSON(coverage))
	}
}

// batchEffect states what an operations list did before a refusal stopped
// it: which operations stay applied (or were only previewed), so a retry
// never repeats an applied prefix, and the failed operation's own recovery.
func (r *plainReply) batchEffect(fields *plainFields) {
	if !fields.has("failed_operation") {
		return
	}
	failed, _ := plainInt(fields.take("failed_operation"))
	applied, _ := plainInt(fields.take("applied_operations"))
	previewed, wasPreview := plainInt(fields.take("previewed_operations"))
	changed, _ := fields.take("canonical_changed").(bool)
	paths, _ := plainStrings(fields.take("changed_paths"))
	switch {
	case wasPreview:
		r.linef("operation %d refused; operations 0-%d were previews only; canonical_changed=false", failed, previewed-1)
	case applied > 0:
		r.linef("operation %d refused; operations 0-%d stay applied; canonical_changed=%t; changed_paths=%s",
			failed, applied-1, changed, plainJSON(paths))
	default:
		r.linef("operation %d refused; no operation was applied; canonical_changed=%t", failed, changed)
	}
	r.next("failed operation recovery", plainList(fields.take("operation_recovery")))
}

// plainFinding is one diagnostic in a reply, from the edit's own delta or
// from the notice stream, merged by its stable ID.
type plainFinding struct {
	id, kind    string
	late        bool
	values      map[string]any
	attribution map[string]any
}

// plainFindings merges diagnostic_delta and diagnostic_updates by finding
// ID: a finding announced by both is printed once, with the notice's
// attribution and its later state winning.
type plainFindings struct {
	items    []*plainFinding
	byID     map[string]*plainFinding
	resolved map[string]bool
}

// addDelta records an edit's diagnostic_delta: new findings and resolved
// IDs. It returns the delta's fields it does not know, for the detail line.
func (p *plainFindings) addDelta(value any) map[string]any {
	delta := newPlainFields(plainObject(value))
	p.add("new", delta.take("new"))
	for _, id := range plainList(delta.take("resolved")) {
		p.resolve(plainString(id))
	}
	return delta.rest()
}

func (p *plainFindings) resolve(id string) {
	if p.resolved == nil {
		p.resolved = map[string]bool{}
	}
	p.resolved[id] = true
	if existing := p.byID[id]; existing != nil {
		existing.kind = "resolved"
	}
}

// add records findings; kind is the kind of an edit delta entry, empty for
// diagnostic_updates, whose entries carry their own.
func (p *plainFindings) add(kind string, value any) {
	if p.byID == nil {
		p.byID = map[string]*plainFinding{}
	}
	for _, raw := range plainList(value) {
		item := plainObject(raw)
		if item == nil {
			continue
		}
		finding := &plainFinding{id: plainString(item["id"]), kind: kind, late: kind == "", values: item}
		if finding.late {
			finding.kind = plainString(item["kind"])
			finding.attribution = plainObject(item["attribution"])
		}
		if finding.kind == "resolved" {
			p.resolve(finding.id)
			continue
		}
		// A later active announcement wins over an earlier resolution: the
		// finding is current, so it is no longer counted as resolved.
		delete(p.resolved, finding.id)
		if existing := p.byID[finding.id]; existing != nil && finding.id != "" {
			p.merge(existing, finding)
			continue
		}
		p.items = append(p.items, finding)
		if finding.id != "" {
			p.byID[finding.id] = finding
		}
	}
}

// merge folds a later announcement of the same finding into the first:
// missing fields are filled, the attribution and the last kind win.
func (p *plainFindings) merge(existing, later *plainFinding) {
	for key, value := range later.values {
		if _, present := existing.values[key]; !present {
			existing.values[key] = value
		}
	}
	if later.attribution != nil {
		existing.attribution = later.attribution
	}
	if later.kind != existing.kind {
		existing.kind = later.kind
		existing.late = later.late
	}
}

func (p *plainFindings) render(r *plainReply) {
	for _, finding := range p.items {
		if finding.kind == "resolved" {
			continue
		}
		r.line(finding.text())
	}
	if len(p.resolved) > 0 {
		r.linef("resolved: %d finding(s)", len(p.resolved))
	}
}

// text is path:line: severity: message [code] (producer), then what kind of
// announcement it is when it did not come from this call's own delta.
func (f *plainFinding) text() string {
	fields := newPlainFields(f.values)
	for _, key := range []string{"id", "kind", "attribution"} {
		fields.take(key)
	}
	location := plainString(fields.take("path"))
	if location == "" {
		location = "(no path)"
	}
	if line, ok := plainInt(fields.take("line")); ok && line > 0 {
		location += ":" + strconv.FormatInt(line, 10)
	}
	text := location + ": " + plainSeverity(fields.take("severity")) + ": " + plainString(fields.take("message"))
	if code := plainString(fields.take("code")); code != "" {
		text += " [" + code + "]"
	}
	if producer := plainString(fields.take("producer")); producer != "" {
		text += " (" + producer + ")"
	}
	if plainBool(fields.take("baseline_missing")) {
		text += " (no baseline: new or pre-existing)"
	}
	if label := f.label(); label != "" {
		text += " (" + label + ")"
	}
	if left := fields.rest(); len(left) > 0 {
		text += " " + plainJSON(left)
	}
	return text
}

// label words where a finding came from and who it is attributed to. A
// notice-stream finding says so, and without an attribution it is not
// claimed for this call. An attribution is printed whenever one was given,
// including on a finding this call's delta also announced.
func (f *plainFinding) label() string {
	label := ""
	switch {
	case !f.late:
	case f.kind == "persisting":
		label = "still present"
	case f.kind == "new" || f.kind == "":
		label = "late finding"
	default:
		label = "update kind=" + f.kind
	}
	if f.attribution == nil {
		if f.late {
			return label + "; not attributed to this call"
		}
		return label
	}
	if label != "" {
		label += "; "
	}
	var parts []string
	for _, key := range plainSortedKeys(f.attribution) {
		value := f.attribution[key]
		if values, ok := plainStrings(value); ok {
			parts = append(parts, key+"="+strings.Join(values, ","))
			continue
		}
		parts = append(parts, key+"="+plainScalar(value))
	}
	return label + "attribution: " + strings.Join(parts, " ")
}

// plainSeverity names an LSP severity and keeps any other value explicit.
func plainSeverity(value any) string {
	severity, ok := plainInt(value)
	if !ok || severity == 0 {
		return "severity unspecified"
	}
	switch severity {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	}
	return "severity=" + strconv.FormatInt(severity, 10)
}

func plainString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	case json.Number:
		return typed.String()
	}
	return fmt.Sprint(value)
}

func plainInt(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Int64()
		return number, err == nil
	case float64:
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	}
	return 0, false
}

func plainBool(value any) bool {
	typed, _ := value.(bool)
	return typed
}

func plainList(value any) []any {
	typed, _ := value.([]any)
	return typed
}

func plainObject(value any) map[string]any {
	typed, _ := value.(map[string]any)
	return typed
}

// plainStrings reads a list whose every item is a string.
func plainStrings(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

func plainSortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// plainJSON is compact JSON without HTML escaping; map keys are sorted.
func plainJSON(value any) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Sprintf("%q", fmt.Sprint(value))
	}
	return strings.TrimRight(buffer.String(), "\n")
}

// plainValue is a value as an argument would be written: strings quoted,
// numbers and booleans bare, structures as JSON.
func plainValue(value any) string {
	switch typed := value.(type) {
	case json.Number:
		return typed.String()
	case bool:
		return strconv.FormatBool(typed)
	}
	return plainJSON(value)
}

// plainScalar is a value as prose: strings bare, everything else as JSON.
func plainScalar(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return plainValue(value)
}

// plainPath writes a path as-is unless it could be misread inside a header:
// whitespace, separators, quotes or control characters make it JSON-quoted.
func plainPath(path string) string {
	if path == "" || strings.ContainsAny(path, " \t\n\r\"();#") || strings.IndexFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return plainJSON(path)
	}
	return path
}
