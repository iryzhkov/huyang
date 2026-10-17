//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// All identities here are observation sidecars, never producer version proof.
type m9HistoricalCall struct {
	Name          string
	Reply         map[string]any
	Before, After int
}

type m9HistoricalBatch struct {
	Constructed, Enqueued, Emission map[string]any
	Transport                       map[string]any
	CitingCall                      map[string]any
	Origin                          map[string]any
	Request, Response               map[string]any
	EvidenceID                      string
	Attributed                      bool
}

type m9History struct {
	Process, Client any
	Requests        map[string]map[string]any
	Dispatched      map[string]bool
	Callbacks       map[string]map[string]any
	Cancelled       map[string]bool
	Sent            map[string]bool
	Batches         map[string]*m9HistoricalBatch
	Origins         map[string]map[string]any
	Sequences       map[string]float64
	TransportEnds   map[string]map[string]any
	OpenTokens      map[string]any
	Inflight        map[string]bool
	Latest          map[string]any
	Relevant        []map[string]any
}

func m9Identity(process, id any) string { return fmt.Sprintf("%v:%v", process, id) }

func m9ObserverStatusOK(status map[string]any) bool {
	if status["installed"] != true {
		return false
	}
	switch errors := status["errors"].(type) {
	case []any:
		return len(errors) == 0
	case map[string]any:
		return len(errors) == 0 // Lua may encode an empty table as an object.
	default:
		return false
	}
}

func m9DecodeCut(body []byte) ([]map[string]any, error) {
	if len(body) == 0 || body[len(body)-1] != '\n' {
		return nil, fmt.Errorf("missing/incomplete newline-terminated trace cut")
	}
	var events []map[string]any
	offset := 0
	for _, line := range bytes.Split(body[:len(body)-1], []byte("\n")) {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil || event == nil {
			return nil, fmt.Errorf("malformed/truncated trace cut")
		}
		event["cut_byte_start"] = float64(offset)
		offset += len(line) + 1
		event["cut_byte_end"] = float64(offset)
		events = append(events, event)
	}
	return events, nil
}

// Validate the whole multi-process cut before choosing the canonical lane.
// Sequence numbers are contiguous within one process, never globally unique.
func m9HistoricalTrace(events []map[string]any, fixed *m9PostFix) (*m9History, error) {
	h := &m9History{Requests: map[string]map[string]any{}, Dispatched: map[string]bool{}, Callbacks: map[string]map[string]any{},
		Cancelled: map[string]bool{}, Sent: map[string]bool{}, Batches: map[string]*m9HistoricalBatch{},
		Origins: map[string]map[string]any{}, Sequences: map[string]float64{}, TransportEnds: map[string]map[string]any{},
		OpenTokens: map[string]any{}, Inflight: map[string]bool{}}
	ready, collections := map[string]bool{}, map[string]bool{}
	transportSequences := map[string]float64{}
	for _, event := range events {
		process, pok := event["process_id"].(float64)
		seq, sok := event["trace_seq"].(float64)
		pid := fmt.Sprint(process)
		if !pok || process < 1 || !sok || seq != h.Sequences[pid]+1 {
			return nil, fmt.Errorf("missing/duplicate/out-of-order per-process trace sequence")
		}
		h.Sequences[pid] = seq
		switch event["kind"] {
		case "observer_ready":
			if ready[pid] || seq != 1 {
				return nil, fmt.Errorf("observer installation is ambiguous")
			}
			ready[pid] = true
		case "collection_begin":
			key := m9Identity(process, event["collection_id"])
			if collections[key] {
				return nil, fmt.Errorf("duplicate collection begin")
			}
			collections[key] = true
		case "collection_end":
			key := m9Identity(process, event["collection_id"])
			status := m9Object(event["observer_status"])
			if !collections[key] || status["installed"] != true || len(probeSlice(status["errors"])) != 0 {
				return nil, fmt.Errorf("missing collection or observer telemetry failed")
			}
			delete(collections, key)
		case "transport_end":
			status := m9Object(event["observer_status"])
			key := m9Identity(process, event["provider_rpc_id"])
			if event["provider_rpc_id"] == nil || h.TransportEnds[key] != nil || !m9ObserverStatusOK(status) {
				return nil, fmt.Errorf("missing/duplicate transport-end identity or observer error")
			}
			h.TransportEnds[key] = event
		case "diagnostic_observation":
			if event["stage"] == "transport_emission" {
				if event["provider_rpc_id"] == nil {
					return nil, fmt.Errorf("transport emission lacks actual RPC identity")
				}
				transportSequences[m9Identity(process, event["provider_rpc_id"])] = seq
			}
			switch event["stage"] {
			case "accepted_receive", "constructed", "enqueued", "immediate_emission", "transport_emission":
			case "late_emission":
				if !collections[m9Identity(process, event["collection_id"])] {
					return nil, fmt.Errorf("late emission outside actual collection")
				}
			default:
				return nil, fmt.Errorf("unknown observation stage")
			}
			if event["telemetry_errors"] != float64(0) {
				return nil, fmt.Errorf("observer telemetry failed")
			}
		case "attach", "publish", "progress", "pull_request", "pull_dispatch", "pull_cancel", "pull":
		default:
			return nil, fmt.Errorf("unhandled trace event %v", event["kind"])
		}
		if !ready[pid] {
			return nil, fmt.Errorf("trace starts before observer installation")
		}
		if event["kind"] == "attach" && event["path"] == fixed.Path {
			if event["name"] != "pyright" || event["client_id"] == nil ||
				(h.Process != nil && (h.Process != process || h.Client != event["client_id"])) {
				return nil, fmt.Errorf("ambiguous canonical process/client")
			}
			h.Process, h.Client = process, event["client_id"]
		}
	}
	for key, seq := range transportSequences {
		marker := h.TransportEnds[key]
		if marker == nil || marker["trace_seq"].(float64) <= seq {
			return nil, fmt.Errorf("transport emission lacks later same-RPC transport-end marker")
		}
	}
	if h.Process == nil || len(collections) != 0 {
		return nil, fmt.Errorf("canonical attachment or complete collection cut missing")
	}
	for _, event := range events {
		uri := m9Object(event["params"])["uri"]
		if event["kind"] == "pull" || event["kind"] == "pull_request" {
			uri = m9Object(m9Object(event["request_params"])["textDocument"])["uri"]
		}
		if uri == fixed.uri() && (event["process_id"] != h.Process || event["client_id"] != h.Client) {
			return nil, fmt.Errorf("wrong-process/client canonical event")
		}
		if event["process_id"] != h.Process {
			continue
		}
		id := m9Text(event["request_id"])
		switch event["kind"] {
		case "pull_request":
			if h.Requests[id] != nil || id == "" {
				return nil, fmt.Errorf("duplicate/missing raw request identity")
			}
			h.Requests[id] = event
		case "pull_dispatch":
			rpc, ok := event["rpc_id"].(float64)
			if h.Requests[id] == nil || h.Dispatched[id] {
				return nil, fmt.Errorf("dispatch without unique raw request")
			}
			h.Dispatched[id] = true
			h.Sent[id] = event["sent"] == true && ok && rpc >= 1
			if h.Sent[id] && h.Callbacks[id] == nil && !h.Cancelled[id] && m9Object(m9Object(h.Requests[id]["request_params"])["textDocument"])["uri"] == fixed.uri() {
				h.Inflight[id] = true
			}
		case "pull_cancel":
			request := h.Requests[id]
			if request == nil {
				return nil, fmt.Errorf("cancel without identified request")
			}
			if m9Object(m9Object(request["request_params"])["textDocument"])["uri"] == fixed.uri() {
				h.Relevant = append(h.Relevant, event)
			}
			h.Cancelled[id] = true
			delete(h.Inflight, id)
		case "pull":
			if h.Requests[id] == nil || h.Callbacks[id] != nil {
				return nil, fmt.Errorf("callback without unique request")
			}
			h.Callbacks[id] = event
			delete(h.Inflight, id)
			if uri == fixed.uri() {
				h.Latest = event
				h.Relevant = append(h.Relevant, event)
			}
		case "publish":
			if uri == fixed.uri() {
				h.Latest = event
				h.Relevant = append(h.Relevant, event)
			}
		case "progress":
			data := m9Object(event["data"])
			if data["client_id"] == nil {
				return nil, fmt.Errorf("progress lacks client")
			}
			if data["client_id"] == h.Client {
				params := m9Object(data["params"])
				switch params["token"].(type) {
				case string, float64:
				default:
					return nil, fmt.Errorf("progress lacks token")
				}
				token, _ := json.Marshal(params["token"])
				switch m9Object(params["value"])["kind"] {
				case "begin":
					if h.OpenTokens[string(token)] != nil {
						return nil, fmt.Errorf("duplicate progress begin")
					}
					h.OpenTokens[string(token)] = params["token"]
				case "report":
					if h.OpenTokens[string(token)] == nil {
						return nil, fmt.Errorf("progress report without observed begin")
					}
				case "end":
					if h.OpenTokens[string(token)] == nil {
						return nil, fmt.Errorf("progress end without observed begin")
					}
					delete(h.OpenTokens, string(token))
				default:
					return nil, fmt.Errorf("unhandled progress event")
				}
				h.Relevant = append(h.Relevant, event)
			}
		case "diagnostic_observation":
			stage := m9Text(event["stage"])
			identity := m9Object(event["identity"])
			key := m9Identity(h.Process, identity["batch_id"])
			switch stage {
			case "accepted_receive":
				o := m9Object(event["origin"])
				k := m9Identity(h.Process, o["origin_id"])
				if o["origin_id"] == nil || h.Origins[k] != nil {
					return nil, fmt.Errorf("duplicate/missing accepted origin")
				}
				h.Origins[k] = event
			case "constructed":
				if identity["batch_id"] == nil || h.Batches[key] != nil {
					return nil, fmt.Errorf("missing/duplicate batch sidecar")
				}
				h.Batches[key] = &m9HistoricalBatch{Constructed: event, Origin: m9Object(identity["origin"])}
			case "enqueued":
				b := h.Batches[key]
				if b == nil {
					// Push batches have no pull construction origin.
					if m9Object(event["batch"])["kind"] == "lsp_pull" {
						return nil, fmt.Errorf("pull enqueue without construction")
					}
					continue
				}
				if b.Enqueued != nil || identity["queue_sequence"] != event["queue_sequence"] || event["queue_sequence"] == nil {
					return nil, fmt.Errorf("ambiguous actual queue sequence")
				}
				b.Enqueued = event
			case "late_emission", "immediate_emission":
				if m9Object(event["batch"])["kind"] != "lsp_pull" {
					continue
				}
				b := h.Batches[key]
				if b == nil || b.Emission != nil {
					return nil, fmt.Errorf("pull emission lacks unique construction")
				}
				if stage == "late_emission" {
					if b.Enqueued == nil || event["queue_sequence"] != b.Enqueued["queue_sequence"] ||
						event["collection_id"] == nil {
						return nil, fmt.Errorf("emission lacks actual enqueue/collection relation")
					}
				} else if b.Enqueued != nil || m9Text(m9Object(event["context"])["request_id"]) == "" {
					return nil, fmt.Errorf("immediate emission lacks RPC context")
				}
				if !reflect.DeepEqual(m9Object(identity["origin"]), b.Origin) {
					return nil, fmt.Errorf("emission changed original origin")
				}
				b.Emission = event
			case "transport_emission":
				if m9Object(event["batch"])["kind"] != "lsp_pull" {
					continue
				}
				b := h.Batches[key]
				if b == nil || b.Emission == nil || b.Transport != nil || event["provider_rpc_id"] == nil ||
					event["transport_ok"] != true || !reflect.DeepEqual(identity, m9Object(b.Emission["identity"])) ||
					!m9SameEmittedBatch(m9Object(event["batch"]), m9Object(b.Emission["batch"])) {
					return nil, fmt.Errorf("transport lacks unique actual emitted-table identity")
				}
				b.Transport = event
			default:
				return nil, fmt.Errorf("unhandled observation stage %s", stage)
			}
		}
	}
	for _, b := range h.Batches {
		raw := m9Object(b.Origin["raw"])
		b.Request, b.Response = h.Requests[m9Text(raw["request_id"])], h.Callbacks[m9Text(raw["request_id"])]
		accepted := h.Origins[m9Identity(h.Process, b.Origin["origin_id"])]
		if raw == nil || b.Request == nil || b.Response == nil || accepted == nil {
			continue // Explicitly unattributed; cannot qualify or deliver a later positive.
		}
		watch := m9Object(b.Origin["watch"])
		if raw["process_id"] != h.Process || raw["client_id"] != b.Request["client_id"] ||
			raw["callback_seq"] != b.Response["trace_seq"] ||
			watch["client_id"] != b.Request["client_id"] ||
			watch["document"] != m9Object(b.Request["snapshot"])["path"] ||
			!reflect.DeepEqual(b.Origin, m9Object(accepted["origin"])) ||
			!h.Sent[m9Text(raw["request_id"])] || h.Cancelled[m9Text(raw["request_id"])] {
			return nil, fmt.Errorf("accepted origin conflicts with request/callback/cancellation")
		}
		if b.Response["err"] == nil && !reflect.DeepEqual(accepted["result"], b.Response["params"]) {
			return nil, fmt.Errorf("accepted receive changed raw result")
		}
		acceptedSeq, _ := accepted["trace_seq"].(float64)
		callbackSeq, _ := b.Response["trace_seq"].(float64)
		constructedSeq, _ := b.Constructed["trace_seq"].(float64)
		generation, _ := watch["generation"].(float64)
		if generation < 1 || acceptedSeq <= callbackSeq || constructedSeq <= acceptedSeq {
			return nil, fmt.Errorf("accepted receive/construction order is incomplete")
		}
		if b.Enqueued != nil && b.Enqueued["trace_seq"].(float64) <= constructedSeq {
			return nil, fmt.Errorf("enqueue predates construction")
		}
		if b.Emission != nil && (b.Emission["trace_seq"].(float64) <= constructedSeq ||
			(b.Enqueued != nil && b.Emission["trace_seq"].(float64) <= b.Enqueued["trace_seq"].(float64))) {
			return nil, fmt.Errorf("emission predates construction/queue")
		}
		b.Attributed = true
		constructed := m9Object(b.Constructed["batch"])
		params := m9Object(b.Response["params"])
		if b.Response["err"] == nil && params["kind"] == "full" &&
			!m9EqualFindings(m9NormalizedRawFindings(probeSlice(params["items"])), probeSlice(constructed["findings"])) {
			return nil, fmt.Errorf("constructed findings changed accepted producer body")
		}
		if b.Emission != nil {
			emitted := m9Object(b.Emission["batch"])
			for _, field := range []string{"kind", "provider_id", "document", "document_revision", "transaction_id", "expected_version", "result_id"} {
				if !reflect.DeepEqual(constructed[field], emitted[field]) {
					return nil, fmt.Errorf("emission changed constructed %s", field)
				}
			}
			if !m9EqualFindings(probeSlice(constructed["findings"]), probeSlice(emitted["findings"])) {
				return nil, fmt.Errorf("known-environment normalization changed findings")
			}
		}
	}
	return h, nil
}

// Mirrors the inspected, lossless edit.lua finding conversion; no diagnostic
// message/result is rewritten. Used to check the observed actual construction.
func m9EqualFindings(a, b []any) bool {
	return (len(a) == 0 && len(b) == 0) || reflect.DeepEqual(a, b)
}

func m9NormalizedRawFindings(items []any) []any {
	var out []any
	for _, raw := range items {
		d := m9Object(raw)
		r := m9Object(d["range"])
		a, b := m9Object(r["start"]), m9Object(r["end"])
		number := func(v any) float64 { n, _ := v.(float64); return n }
		severity := number(d["severity"])
		f := map[string]any{"range": map[string]any{
			"start_line": number(a["line"]) + 1, "start_character": number(a["character"]) + 1,
			"end_line": number(b["line"]) + 1, "end_character": number(b["character"]) + 1},
			"severity": severity, "message": m9Text(d["message"])}
		if d["code"] != nil {
			f["code"] = fmt.Sprint(d["code"])
		}
		if d["source"] != nil {
			f["source"] = d["source"]
		}
		out = append(out, f)
	}
	return out
}

// Compare the entire emitted diagnostic payload except documented Go-owned
// additions: pre-edit revision, state_seq, observed_at. Empty omitted slices,
// false optional booleans and empty optional strings have typed zero values.
func m9SameEmittedBatch(emitted, selected map[string]any) bool {
	if emitted == nil || selected == nil {
		return false
	}
	keys := []string{"kind", "provider_id", "producer", "producer_version", "document", "document_revision",
		"document_version", "expected_version", "result_id", "transaction_id", "complete", "timed_out",
		"reason", "progress_pending", "change_barrier", "selected", "baseline", "staged", "dimension", "findings", "candidates"}
	allowed := map[string]bool{"pre_edit_document_revision": true, "state_seq": true, "observed_at": true}
	for _, key := range keys {
		allowed[key] = true
	}
	for _, batch := range []map[string]any{emitted, selected} {
		for key := range batch {
			if !allowed[key] {
				return false
			}
		}
	}
	for _, key := range keys {
		a, b := emitted[key], selected[key]
		switch key {
		case "findings", "candidates":
			if len(probeSlice(a)) == 0 && len(probeSlice(b)) == 0 {
				continue
			}
		case "complete", "timed_out", "progress_pending", "change_barrier", "selected", "baseline", "staged":
			a, b = a == true, b == true
		case "producer_version", "reason", "result_id":
			a, b = m9Text(a), m9Text(b)
		}
		if !reflect.DeepEqual(a, b) {
			return false
		}
	}
	return true
}

// First full delivery establishes actual ledger IDs. Later empty observations
// cannot silently erase those IDs from any subsequent full current/stale report.
func m9RetainedPositiveIDs(replies []map[string]any, evidenceID string, finding any) ([]string, int, error) {
	var ids []string
	first := -1
	for n, reply := range replies {
		report := m9Report(reply)
		if report == nil {
			continue
		}
		retained := map[string]bool{}
		delivered := map[string]bool{}
		for _, group := range []string{"current", "stale"} {
			for _, raw := range probeSlice(report[group]) {
				item := m9Object(raw)
				id := m9Text(item["id"])
				if id == "" || item["status"] != group {
					continue
				}
				retained[id] = true
				if !reflect.DeepEqual(item["finding"], finding) {
					continue
				}
				for _, ev := range probeSlice(item["evidence_ids"]) {
					if ev == evidenceID {
						delivered[id] = true
					}
				}
			}
		}
		if first == -1 && len(delivered) > 0 {
			first = n
			for id := range delivered {
				ids = append(ids, id)
			}
			sort.Strings(ids)
		}
		if first != -1 {
			for _, id := range ids {
				if !retained[id] {
					return nil, first, fmt.Errorf("later positive finding %s disappeared from full report %d", id, n)
				}
			}
		}
	}
	if first == -1 {
		return nil, first, fmt.Errorf("later positive lacks full selected evidence/body delivery")
	}
	return ids, first, nil
}

// A completion marker is written after observer.transport, including completions
// with no batches. Byte bounds bind it to the actual citing MCP call.
func m9CallTransportEnd(h *m9History, call m9HistoricalCall) map[string]any {
	var last map[string]any
	for _, marker := range h.TransportEnds {
		start, _ := marker["cut_byte_start"].(float64)
		end, _ := marker["cut_byte_end"].(float64)
		if marker["process_id"] == h.Process && end > start &&
			start >= float64(call.Before) && end <= float64(call.After) &&
			(last == nil || marker["trace_seq"].(float64) > last["trace_seq"].(float64)) {
			last = marker
		}
	}
	return last
}

func m9HistoricalResult(events []map[string]any, fixed *m9PostFix, evidence map[string]map[string]any, replies []map[string]any, calls []m9HistoricalCall) (map[string]any, error) {
	h, err := m9HistoricalTrace(events, fixed)
	if err != nil {
		return nil, err
	}
	if fixed.Revision == "" || fixed.Transaction == "" || len(fixed.IDs) == 0 || len(replies) == 0 {
		return nil, fmt.Errorf("missing fix receipt/positive gate/final report")
	}
	for _, reply := range replies {
		if _, err := m9PostFixLedger(reply, fixed, true); err != nil {
			return nil, err
		}
	}
	for _, event := range h.Relevant {
		if event["kind"] == "publish" {
			return nil, fmt.Errorf("push observed: historical pull cannot conceal available/mixed proof path")
		}
	}
	// Source-backed unique emission -> selected evidence linkage. Every
	// candidate emission is observed, including timeout/nil constructions;
	// native/discarded callbacks cannot enter this set through content equality.
	for id, selected := range evidence {
		if selected["provider_id"] != fmt.Sprintf("pyright#%v", h.Client) || selected["document_revision"] != fixed.Revision ||
			selected["transaction_id"] != fixed.Transaction || selected["kind"] != "lsp_pull" {
			continue
		}
		var matches []*m9HistoricalBatch
		for _, b := range h.Batches {
			if b.Emission != nil && m9SameEmittedBatch(m9Object(b.Emission["batch"]), selected) {
				matches = append(matches, b)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("selected evidence %s has %d possible emitted origins", id, len(matches))
		}
		b := matches[0]
		if b.EvidenceID != "" {
			return nil, fmt.Errorf("one emission matched multiple immutable evidence records")
		}
		var first *m9HistoricalCall
		for n := range calls {
			call := &calls[n]
			cited := m9CitedBatches(call.Reply)
			for _, raw := range probeSlice(m9Object(call.Reply["evidence"])["ids"]) {
				cited[m9Text(raw)] = true
			}
			if cited[id] && (m9Report(call.Reply) != nil || call.Name == "edit_apply") {
				first = call
				break
			}
		}
		start, _ := b.Transport["cut_byte_start"].(float64)
		end, _ := b.Transport["cut_byte_end"].(float64)
		if b.Transport == nil || first == nil || end <= start || start < float64(first.Before) || end > float64(first.After) {
			return nil, fmt.Errorf("selected evidence lacks unique actual transport inside its first citing call")
		}
		marker := h.TransportEnds[m9Identity(h.Process, b.Transport["provider_rpc_id"])]
		markerStart, _ := marker["cut_byte_start"].(float64)
		markerEnd, _ := marker["cut_byte_end"].(float64)
		if marker == nil || markerEnd <= markerStart || markerStart < float64(first.Before) || markerEnd > float64(first.After) {
			return nil, fmt.Errorf("same-RPC transport-end marker lies outside first citing call")
		}
		b.CitingCall = map[string]any{"name": first.Name, "request_id": first.Reply["request_id"],
			"trace_bytes_before": first.Before, "trace_bytes_after": first.After, "transport_end": marker}
		observed, e := time.Parse(time.RFC3339Nano, m9Text(selected["observed_at"]))
		at, _ := b.Transport["unix_us"].(float64)
		if e != nil || float64(observed.UnixMicro()) < at {
			return nil, fmt.Errorf("selected evidence predates actual emission")
		}
		b.EvidenceID = id
	}
	final := replies[len(replies)-1]
	for i := len(replies) - 1; i >= 0; i-- {
		if m9Report(replies[i]) != nil {
			final = replies[i]
			break
		}
	}
	var candidates []*m9HistoricalBatch
	for _, b := range h.Batches {
		selected := evidence[b.EvidenceID]
		if b.EvidenceID == "" || !b.Attributed || b.Origin == nil || m9Object(b.Origin["raw"]) == nil || b.Emission == nil ||
			selected["selected"] != true || selected["complete"] != false || selected["document_version"] != nil ||
			selected["timed_out"] == true || selected["staged"] == true || selected["dimension"] != "edited_documents" ||
			!m9CitedBatches(final)[b.EvidenceID] || selected["expected_version"] != m9Object(b.Request["snapshot"])["local_version"] ||
			(selected["document"] != fixed.Path && selected["document"] != "main.py") {
			continue
		}
		if m9Text(m9Object(b.Response["params"])["resultId"]) != "" && m9CorrelatedEmpty(b.Request, b.Response, fixed) == nil {
			candidates = append(candidates, b)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Response["trace_seq"].(float64) < candidates[j].Response["trace_seq"].(float64)
	})
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no exact-origin selected historical post-fix empty observation")
	}
	candidate := candidates[0]
	var finalMarker map[string]any
	for n := len(calls) - 1; n >= 0; n-- {
		call := calls[n]
		if call.Name == "diagnostics" && m9Report(call.Reply) != nil {
			if m9CitedBatches(call.Reply)[candidate.EvidenceID] {
				finalMarker = m9CallTransportEnd(h, call)
			}
			break
		}
	}
	if finalMarker == nil {
		return nil, fmt.Errorf("final citing full report lacks in-stream transport-end cut marker")
	}
	seq := candidate.Response["trace_seq"].(float64)
	var superseding, duplicates []any
	classification := "historical_empty_latest_payload_reported"
	laterPositive := false
	var laterDeliveries []any
	for _, event := range h.Relevant {
		s := event["trace_seq"].(float64)
		if s <= seq {
			continue
		}
		superseding = append(superseding, s)
		if event["kind"] == "progress" {
			classification = "historical_empty_superseded_by_progress"
		}
		if event["kind"] != "pull" {
			continue
		}
		params := m9Object(event["params"])
		if reflect.DeepEqual(params, m9Object(candidate.Response["params"])) {
			duplicates = append(duplicates, map[string]any{"request_id": event["request_id"], "trace_seq": s})
		}
		items, array := params["items"].([]any)
		if event["err"] != nil || params["kind"] != "full" || !array || len(items) == 0 {
			continue
		}
		// Every later positive, including native/cancelled callbacks, must
		// have a genuine accepted-origin/emission/selected body delivery.
		delivered := false
		for _, b := range h.Batches {
			if b.Response == nil || b.Response["request_id"] != event["request_id"] || b.Response["trace_seq"] != event["trace_seq"] || !b.Attributed || b.EvidenceID == "" || b.Emission == nil || m9Object(b.Origin["raw"]) == nil {
				continue
			}
			for _, finding := range probeSlice(evidence[b.EvidenceID]["findings"]) {
				ids, first, err := m9RetainedPositiveIDs(replies, b.EvidenceID, finding)
				if err != nil {
					return nil, err
				}
				laterDeliveries = append(laterDeliveries, map[string]any{"evidence_id": b.EvidenceID,
					"finding_ids": ids, "first_full_report_index": first})
			}
			delivered = delivered || len(probeSlice(evidence[b.EvidenceID]["findings"])) > 0
		}
		if !delivered {
			return nil, fmt.Errorf("later positive callback %v lacks attributed selected ledger-body delivery", event["trace_seq"])
		}
		laterPositive = true
	}
	// Conspicuous positive classification survives any following progress.
	if laterPositive {
		classification = "historical_empty_later_positive_delivered"
	}
	var callBoundaries []any
	for _, call := range calls {
		callBoundaries = append(callBoundaries, map[string]any{"name": call.Name, "request_id": call.Reply["request_id"],
			"trace_bytes_before": call.Before, "trace_bytes_after": call.After})
	}
	return map[string]any{
		"call_boundaries": callBoundaries,
		"later_positive_deliveries": laterDeliveries,
		"final_cut_marker": finalMarker,
		"outcome":         "historical_post_fix_observation_pending", "historical_observation": true,
		"exact_attribution": true, "no_resolution": true, "analysis_completion_proven": false,
		"clean_resolution_proven": false, "old_terminal_gate_qualified": false,
		"classification": classification, "original_observation": candidate,
		"original_evidence": evidence[candidate.EvidenceID], "qualifying_evidence_ids": []string{candidate.EvidenceID},
		"identical_later_callbacks": duplicates, "superseding_trace_sequences": superseding,
		"latest_observed_payload": h.Latest, "open_progress_tokens": h.OpenTokens, "inflight_requests": h.Inflight,
		"known_work_pending": len(h.OpenTokens) != 0 || len(h.Inflight) != 0,
		"canonical_process":  h.Process, "selected_provider": fmt.Sprintf("pyright#%v", h.Client),
		"final_process_sequences": h.Sequences, "final_ledger": m9Report(final),
		"retained_finding_ids": fixed.IDs, "fixed_transaction": fixed.Transaction,
		"producer_proof_observed": false,
	}, nil
}

func (p *m9Probe) recoverHistoricalEvidence() {
	for p.historicalInspected < len(p.postFixReplies) {
		item := p.postFixReplies[p.historicalInspected]
		p.historicalInspected++
		if len(probeSlice(m9Object(item["evidence"])["ids"])) > 0 && item["data"] != nil &&
			(m9Report(item) != nil || m9Object(m9Object(item["data"])["evidence"]) == nil) {
			p.inspectEvidence(item)
		}
	}
}

func (p *m9Probe) collectPostFix() {
	p.t.Helper()
	// Predeclared allocation, not a clean/settled timer. All iterations and
	// finalization-triggered work remain in the final complete trace cut.
	for time.Now().Before(p.historicalEnd) && p.ctx.Err() == nil {
		if m9Report(p.call("diagnostics", map[string]any{"full": true})) == nil {
			p.t.Fatal("historical sampling omitted full report")
		}
		p.recoverHistoricalEvidence()
		p.event()
	}
	if p.ctx.Err() != nil {
		p.t.Fatal("interrupted historical observation (deadline never success)")
	}
	final := p.call("diagnostics", map[string]any{"full": true})
	if m9Report(final) == nil {
		p.t.Fatal("final historical full report missing")
	}
	p.recoverHistoricalEvidence()
	body, err := os.ReadFile(filepath.Join(p.dir, "lsp.jsonl"))
	if err != nil {
		p.t.Fatal(err)
	}
	events, err := m9DecodeCut(body)
	if err != nil || p.ctx.Err() != nil {
		p.t.Fatalf("incomplete historical cut: %v", err)
	}
	// Retain the original stamped-push lane, including quiet/progress and
	// legitimate resolution checks; failed available proof never falls back.
	state, err := m9PostFixTrace(events, p.postFix)
	if err != nil {
		p.t.Fatal(err)
	}
	if state.ProducerProof {
		p.collectPostFixTerminal()
		return
	}
	p.historicalCut = append([]byte(nil), body...)
	m9Write(p.t, filepath.Join(p.dir, "historical-trace-cut.jsonl"), string(body))
	cutSum := sha256.Sum256(body)
	p.postFixCoverage["final_trace_sha256"] = hex.EncodeToString(cutSum[:])
	p.postFixCoverage["final_trace_bytes"] = len(body)
	result, err := m9HistoricalResult(events, p.postFix, p.evidenceBatches, p.postFixReplies, p.historicalCalls)
	if err != nil {
		p.postFixCoverage["historical_qualification_error"] = err.Error()
		m9JSON(p.t, filepath.Join(p.dir, "historical-observation.json"), p.postFixCoverage)
		p.t.Fatal(err)
	}
	p.historicalCut = append([]byte(nil), body...)
	digest := sha256.Sum256(body)
	result["final_trace_sha256"] = hex.EncodeToString(digest[:])
	result["final_trace_bytes"] = len(body)
	result["reached_gate"] = "complete_historical_observation_cut"
	artifacts := map[string]string{}
	for n := p.historicalFirstCall; n <= p.sequence; n++ {
		matches, err := filepath.Glob(filepath.Join(p.dir, fmt.Sprintf("%03d-*.json", n)))
		if err != nil || len(matches) != 1 {
			p.t.Fatal("missing/ambiguous report artifact in final cut")
		}
		content, err := os.ReadFile(matches[0])
		if err != nil {
			p.t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		artifacts[filepath.Base(matches[0])] = hex.EncodeToString(sum[:])
	}
	result["report_cut_sha256"] = artifacts
	maps.Copy(p.postFixCoverage, result)
	m9Write(p.t, filepath.Join(p.dir, "historical-trace-cut.jsonl"), string(body))
	m9JSON(p.t, filepath.Join(p.dir, "historical-observation.json"), p.postFixCoverage)
}

func m9HistoricalFixture(t *testing.T) (*m9PostFix, []map[string]any, map[string]map[string]any, []map[string]any) {
	t.Helper()
	fixed, raw := m9PolicyFixture(t)
	events := []map[string]any{{"kind": "observer_ready", "process_id": float64(91)}}
	events = append(events, raw...)
	for n, e := range events {
		e["trace_seq"] = float64(n + 1)
	}
	batch := map[string]any{"kind": "lsp_pull", "provider_id": "pyright#7", "producer": "pyright",
		"document": fixed.Path, "document_revision": fixed.Revision, "transaction_id": fixed.Transaction,
		"expected_version": float64(4), "result_id": "opaque-5", "complete": false,
		"selected": true, "dimension": "edited_documents", "findings": []any{}}
	origin := map[string]any{"origin_id": float64(1),
		"raw":   map[string]any{"process_id": float64(91), "client_id": float64(7), "request_id": "91:7:1", "callback_seq": float64(5)},
		"watch": map[string]any{"client_id": float64(7), "document": fixed.Path, "generation": float64(1)}}
	identity := map[string]any{"batch_id": float64(2), "origin": origin}
	for _, e := range []map[string]any{
		{"kind": "diagnostic_observation", "stage": "accepted_receive", "origin": origin, "result": raw[3]["params"]},
		{"kind": "diagnostic_observation", "stage": "constructed", "identity": identity, "batch": batch},
		{"kind": "diagnostic_observation", "stage": "immediate_emission", "identity": identity, "batch": batch,
			"context": map[string]any{"request_id": "diagnostics_edit_fix"}},
		{"kind": "diagnostic_observation", "stage": "transport_emission", "identity": identity, "batch": batch,
			"provider_rpc_id": float64(41), "transport_ok": true},
	} {
		e["trace_seq"], e["process_id"], e["unix_us"], e["telemetry_errors"] =
			float64(len(events)+1), float64(91), float64(len(events)+31), float64(0)
		events = append(events, e)
	}
	events = m9HistoryAppend(events, map[string]any{"kind": "transport_end", "provider_rpc_id": float64(41),
		"observer_status": map[string]any{"installed": true, "errors": []any{}}})
	events[len(events)-1]["unix_us"] = float64(40)
	selected := maps.Clone(batch)
	selected["observed_at"] = "1970-01-01T00:00:00.000100Z"
	reply := map[string]any{"data": map[string]any{"diagnostics": map[string]any{
		"confidence": "provisional", "stale": []any{map[string]any{"id": "type-original", "status": "stale"}},
		"coverage": map[string]any{"edited_documents": map[string]any{"evidence_ids": []any{"ev_origin"}}}}}}
	return fixed, events, map[string]map[string]any{"ev_origin": selected}, []map[string]any{reply}
}

func m9HistoryAppend(events []map[string]any, event map[string]any) []map[string]any {
	event["process_id"], event["trace_seq"], event["unix_us"] = float64(91), float64(len(events)+1), float64(len(events)+101)
	if event["kind"] == "diagnostic_observation" {
		event["telemetry_errors"] = float64(0)
	}
	return append(events, event)
}

func m9AppendHistoricalPull(t *testing.T, fixed *m9PostFix, events []map[string]any, items []any, immediate bool) ([]map[string]any, map[string]any) {
	t.Helper()
	_, raw := m9PolicyFixture(t)
	id := "91:7:later"
	request, dispatch, response := raw[1], raw[2], raw[3]
	for _, e := range []map[string]any{request, dispatch, response} {
		e["request_id"] = id
	}
	result := map[string]any{"kind": "full", "items": items, "resultId": "later"}
	if immediate {
		result["resultId"] = "opaque-5"
	}
	response["params"] = result
	for _, e := range []map[string]any{request, dispatch, response} {
		events = m9HistoryAppend(events, e)
	}
	origin := map[string]any{"origin_id": float64(10),
		"raw":   map[string]any{"process_id": float64(91), "client_id": float64(7), "request_id": id, "callback_seq": response["trace_seq"]},
		"watch": map[string]any{"client_id": float64(7), "document": fixed.Path, "generation": float64(2)}}
	batch := map[string]any{"kind": "lsp_pull", "provider_id": "pyright#7", "producer": "pyright",
		"document": fixed.Path, "document_revision": fixed.Revision, "transaction_id": fixed.Transaction,
		"expected_version": float64(4), "result_id": result["resultId"], "complete": false,
		"selected": true, "dimension": "edited_documents", "findings": m9NormalizedRawFindings(items)}
	identity := map[string]any{"batch_id": float64(11), "origin": origin}
	events = m9HistoryAppend(events, map[string]any{"kind": "diagnostic_observation", "stage": "accepted_receive", "origin": origin, "result": result})
	events = m9HistoryAppend(events, map[string]any{"kind": "diagnostic_observation", "stage": "constructed", "identity": maps.Clone(identity), "batch": maps.Clone(batch)})
	stage := "immediate_emission"
	if !immediate {
		stage = "late_emission"
		identity["queue_sequence"] = float64(1)
		batch["reason"] = "late_pull"
		events = m9HistoryAppend(events, map[string]any{"kind": "diagnostic_observation", "stage": "enqueued", "identity": identity, "batch": batch, "queue_sequence": float64(1)})
		events = m9HistoryAppend(events, map[string]any{"kind": "collection_begin", "collection_id": float64(1)})
	}
	emission := map[string]any{"kind": "diagnostic_observation", "stage": stage, "identity": identity,
		"batch": batch, "context": map[string]any{"request_id": "late_evidence"}}
	if !immediate {
		emission["queue_sequence"], emission["collection_id"] = float64(1), float64(1)
	}
	events = m9HistoryAppend(events, emission)
	if !immediate {
		events = m9HistoryAppend(events, map[string]any{"kind": "collection_end", "collection_id": float64(1),
			"observer_status": map[string]any{"installed": true, "errors": []any{}}})
	}
	events = m9HistoryAppend(events, map[string]any{"kind": "diagnostic_observation", "stage": "transport_emission",
		"identity": identity, "batch": batch, "provider_rpc_id": float64(42), "transport_ok": true})
	events = m9HistoryAppend(events, map[string]any{"kind": "transport_end", "provider_rpc_id": float64(42),
		"observer_status": map[string]any{"installed": true, "errors": []any{}}})
	selected := maps.Clone(batch)
	selected["observed_at"] = "1970-01-01T00:00:00.001000Z"
	return events, selected
}

func m9TestHistoryCalls(events []map[string]any, replies []map[string]any) []m9HistoricalCall {
	for n, event := range events {
		event["cut_byte_start"], event["cut_byte_end"] = float64(n), float64(n+1)
	}
	var calls []m9HistoricalCall
	for _, reply := range replies {
		calls = append(calls, m9HistoricalCall{Name: "diagnostics", Reply: reply, Before: 0, After: len(events)})
	}
	return calls
}

func TestM9HistoricalPendingDoesNotRestoreTerminal(t *testing.T) {
	fixed, events, evidence, replies := m9HistoricalFixture(t)
	for _, kind := range []string{"begin", "end"} {
		events = m9HistoryAppend(events, map[string]any{"kind": "progress", "data": map[string]any{
			"client_id": float64(7), "params": map[string]any{"token": "later", "value": map[string]any{"kind": kind}}}})
	}
	old, err := m9PostFixTrace(events, fixed)
	if err != nil || old.Candidate != nil {
		t.Fatalf("fixture did not lose the old terminal gate: %+v %v", old, err)
	}
	result, err := m9HistoricalResult(events, fixed, evidence, replies, m9TestHistoryCalls(events, replies))
	if err != nil || result["outcome"] != "historical_post_fix_observation_pending" ||
		result["analysis_completion_proven"] != false || result["clean_resolution_proven"] != false ||
		result["old_terminal_gate_qualified"] != false || result["known_work_pending"] != false {
		t.Fatalf("historical pending confused with clean/completion/active work: %+v %v", result, err)
	}
}

func TestM9HistoricalOriginAndCompleteCut(t *testing.T) {
	for _, name := range []string{"missing-accepted-origin", "cancelled-origin", "wrong-process", "raw-origin-process", "wrong-docrev",
		"wrong-version", "wrong-hash", "missing-sequence", "missing-queue", "observer-error", "unknown-event", "ambiguous-emission", "native-identical-not-origin"} {
		t.Run(name, func(t *testing.T) {
			fixed, events, evidence, replies := m9HistoricalFixture(t)
			if _, err := m9HistoricalResult(events, fixed, evidence, replies, m9TestHistoryCalls(events, replies)); err != nil {
				t.Fatalf("negative fixture baseline is not qualified: %v", err)
			}
			want := ""
			switch name {
			case "missing-accepted-origin":
				events = append(events[:5], events[6:]...)
				for n, e := range events {
					e["trace_seq"] = float64(n + 1)
				}
			case "cancelled-origin":
				events = m9HistoryAppend(events, map[string]any{"kind": "pull_cancel", "request_id": "91:7:1", "client_id": float64(7)})
			case "wrong-process":
				// A complete second stream gets through per-process validation.
				events = append(events,
					map[string]any{"kind": "observer_ready", "process_id": float64(92), "trace_seq": float64(1), "unix_us": float64(200)},
					map[string]any{"kind": "publish", "process_id": float64(92), "trace_seq": float64(2), "unix_us": float64(201),
						"client_id": float64(7), "params": map[string]any{"uri": fixed.uri(), "diagnostics": []any{}}})
				want = "wrong-process/client canonical event"
			case "raw-origin-process":
				m9Object(m9Object(events[5]["origin"])["raw"])["process_id"] = float64(92)
				want = "accepted origin conflicts with request/callback/cancellation"
			case "wrong-docrev":
				evidence["ev_origin"]["document_revision"] = "docrev_other"
			case "wrong-version":
				m9Object(events[2]["snapshot"])["local_version"] = float64(5)
				m9Object(events[4]["snapshot"])["local_version"] = float64(5)
				if _, err := m9HistoricalTrace(events, fixed); err != nil {
					t.Fatalf("version negative missed the candidate guard: %v", err)
				}
				if err := m9CorrelatedEmpty(events[2], events[4], fixed); err != nil ||
					!m9SameEmittedBatch(m9Object(events[7]["batch"]), evidence["ev_origin"]) {
					t.Fatalf("version negative failed an earlier local/payload guard: %v", err)
				}
				want = "no exact-origin selected historical post-fix empty observation"
			case "wrong-hash":
				m9Object(events[4]["snapshot"])["sha256"] = "old"
			case "missing-sequence":
				events[7]["trace_seq"] = float64(9)
			case "missing-queue":
				var selected map[string]any
				events, selected = m9AppendHistoricalPull(t, fixed, events, []any{}, false)
				evidence["ev_later"] = selected
				var withoutQueue []map[string]any
				for _, e := range events {
					if e["stage"] != "enqueued" {
						withoutQueue = append(withoutQueue, e)
					}
				}
				events = withoutQueue
				for n, e := range events {
					e["trace_seq"] = float64(n + 1)
				}
				want = "emission lacks actual enqueue/collection relation"
			case "observer-error":
				events[7]["telemetry_errors"] = float64(1)
			case "unknown-event":
				events = m9HistoryAppend(events, map[string]any{"kind": "unhandled"})
			case "ambiguous-emission":
				events, _ = m9AppendHistoricalPull(t, fixed, events, []any{}, true)
			case "native-identical-not-origin":
				// A raw native callback is not an accepted receive. Pointing an
				// old sidecar at that duplicate must not invent the missing edge.
				m9Object(m9Object(events[5]["origin"])["raw"])["callback_seq"] = float64(99)
			}
			result, err := m9HistoricalResult(events, fixed, evidence, replies, m9TestHistoryCalls(events, replies))
			if err == nil {
				t.Fatalf("unqualified history passed: %+v", result)
			}
			if want != "" && !strings.Contains(err.Error(), want) {
				t.Fatalf("negative hit an unrelated guard: want %q, got %v", want, err)
			}
		})
	}
	for _, body := range [][]byte{nil, []byte("{}"), []byte("{}\n{")} {
		if _, err := m9DecodeCut(body); err == nil {
			t.Fatal("incomplete/truncated cut accepted")
		}
	}
}

func m9CloneHistoricalReply(t *testing.T, reply map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(body, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

// Distinct call intervals deliver a later positive, then observe an empty
// native callback and two more full reports with real completion markers.
func m9PositiveRetentionFixture(t *testing.T) (*m9PostFix, []map[string]any, map[string]map[string]any, []map[string]any, []m9HistoricalCall) {
	t.Helper()
	fixed, events, evidence, replies := m9HistoricalFixture(t)
	firstEnd := len(events)
	items := []any{map[string]any{"range": map[string]any{
		"start": map[string]any{"line": float64(1), "character": float64(0)},
		"end": map[string]any{"line": float64(1), "character": float64(1)}},
		"severity": float64(1), "code": "reportAssignmentType", "source": "Pyright", "message": "later real positive"}}
	var selected map[string]any
	events, selected = m9AppendHistoricalPull(t, fixed, events, items, false)
	evidence["ev_positive"] = selected
	secondEnd := len(events)
	positive := m9CloneHistoricalReply(t, replies[0])
	m9Report(positive)["current"] = []any{map[string]any{"id": "later-real", "status": "current",
		"finding": probeSlice(selected["findings"])[0], "evidence_ids": []any{"ev_positive"}}}
	m9Object(m9Object(m9Report(positive)["coverage"])["edited_documents"])["evidence_ids"] = []any{"ev_origin", "ev_positive"}
	replies = append(replies, positive)
	calls := []m9HistoricalCall{
		{Name: "diagnostics", Reply: replies[0], Before: 0, After: firstEnd},
		{Name: "diagnostics", Reply: positive, Before: firstEnd, After: secondEnd},
	}
	_, raw := m9PolicyFixture(t)
	for _, event := range raw[1:] {
		event["request_id"] = "91:7:after-positive"
		events = m9HistoryAppend(events, event)
	}
	m9Object(raw[1]["request_params"])["previousResultId"] = "later"
	m9Object(raw[3]["request_params"])["previousResultId"] = "later"
	m9Object(raw[3]["params"])["resultId"] = "empty-after-positive"
	before := secondEnd
	for _, rpc := range []float64{43, 44} {
		events = m9HistoryAppend(events, map[string]any{"kind": "transport_end", "provider_rpc_id": rpc,
			"observer_status": map[string]any{"installed": true, "errors": []any{}}})
		reply := m9CloneHistoricalReply(t, positive)
		replies = append(replies, reply)
		calls = append(calls, m9HistoricalCall{Name: "diagnostics", Reply: reply, Before: before, After: len(events)})
		before = len(events)
	}
	m9TestHistoryCalls(events, nil) // Populate byte offsets without overlapping the calls above.
	return fixed, events, evidence, replies, calls
}

func TestM9HistoricalLaterPositiveRequiresActualDelivery(t *testing.T) {
	fixed, events, evidence, replies := m9HistoricalFixture(t)
	items := []any{map[string]any{"range": map[string]any{"start": map[string]any{"line": float64(1), "character": float64(0)},
		"end": map[string]any{"line": float64(1), "character": float64(1)}},
		"severity": float64(1), "code": "reportAssignmentType", "source": "Pyright", "message": "later real positive"}}
	events, selected := m9AppendHistoricalPull(t, fixed, events, items, false)
	evidence["ev_positive"] = selected
	m9Object(m9Object(m9Report(replies[0])["coverage"])["edited_documents"])["evidence_ids"] = []any{"ev_origin", "ev_positive"}
	if _, err := m9HistoricalResult(events, fixed, evidence, replies, m9TestHistoryCalls(events, replies)); err == nil {
		t.Fatal("raw/emitted/selected positive passed without actual finding-body delivery")
	}
	report := m9Report(replies[0])
	report["current"] = []any{map[string]any{"id": "later-real", "status": "current",
		"finding": probeSlice(selected["findings"])[0], "evidence_ids": []any{"ev_positive"}}}
	result, err := m9HistoricalResult(events, fixed, evidence, replies, m9TestHistoryCalls(events, replies))
	if err != nil || result["classification"] != "historical_empty_later_positive_delivered" ||
		result["clean_resolution_proven"] != false {
		t.Fatalf("honest later positive not conspicuous: %+v %v", result, err)
	}

	for _, name := range []string{"final-omission-after-empty", "intermediate-omission-then-restoration", "retained-current-to-stale"} {
		t.Run(name, func(t *testing.T) {
			fixed, events, evidence, replies, calls := m9PositiveRetentionFixture(t)
			if _, err := m9HistoricalResult(events, fixed, evidence, replies, calls); err != nil {
				t.Fatalf("retention fixture baseline is not qualified: %v", err)
			}
			switch name {
			case "final-omission-after-empty":
				m9Report(replies[3])["current"] = []any{}
			case "intermediate-omission-then-restoration":
				m9Report(replies[2])["current"] = []any{}
			case "retained-current-to-stale":
				for _, reply := range replies[2:] {
					report := m9Report(reply)
					item := maps.Clone(m9Object(probeSlice(report["current"])[0]))
					item["status"] = "stale"
					delete(item, "evidence_ids") // The delivered ID must survive, not every old evidence reference.
					report["current"] = []any{}
					report["stale"] = append(probeSlice(report["stale"]), item)
				}
			}
			result, err := m9HistoricalResult(events, fixed, evidence, replies, calls)
			if name != "retained-current-to-stale" {
				if err == nil || !strings.Contains(err.Error(), "later positive finding later-real disappeared from full report") {
					t.Fatalf("silent later-positive loss missed its retention guard: %+v %v", result, err)
				}
			} else if err != nil || result["classification"] != "historical_empty_later_positive_delivered" ||
				result["clean_resolution_proven"] != false || len(probeSlice(result["later_positive_deliveries"])) != 1 {
				t.Fatalf("retained current/stale positive misclassified: %+v %v", result, err)
			}
		})
	}
}

func TestM9HistoricalEveryIntermediateAndMixedPush(t *testing.T) {
	for _, body := range []string{
		`{"data":{"diagnostic_delta":{"resolved_ids":["type-original"]}}}`,
		`{"diagnostic_updates":[{"kind":"resolved","id":"type-original"}]}`,
		`{"data":{"diagnostics":{"confidence":"authoritative","stale":[{"id":"type-original","status":"stale"}]}}}`,
		`{"data":{"diagnostics":{"confidence":"provisional","current":[],"stale":[]}}}`,
		`{"diagnostic_updates_truncated":true}`,
	} {
		fixed, events, evidence, replies := m9HistoricalFixture(t)
		var bad map[string]any
		if err := json.Unmarshal([]byte(body), &bad); err != nil {
			t.Fatal(err)
		}
		replies = append([]map[string]any{bad}, replies...)
		if _, err := m9HistoricalResult(events, fixed, evidence, replies, m9TestHistoryCalls(events, replies)); err == nil {
			t.Fatal("later correction excused earlier false authority/resolution/truncation")
		}
	}
	for _, version := range []any{nil, float64(4)} {
		fixed, events, evidence, replies := m9HistoricalFixture(t)
		params := map[string]any{"uri": fixed.uri(), "diagnostics": []any{}, "version": version}
		events = m9HistoryAppend(events, map[string]any{"kind": "publish", "client_id": float64(7), "params": params})
		if _, err := m9HistoricalResult(events, fixed, evidence, replies, m9TestHistoryCalls(events, replies)); err == nil {
			t.Fatal("failed/mixed push path laundered through historical pull")
		}
	}
}

func TestM9HistoricalTransportCallBoundaryAndMetadata(t *testing.T) {
	for _, name := range []string{"missing-transport", "failed-transport", "outside-first-call", "missing-call", "unknown-payload-field", "repeated-transport"} {
		t.Run(name, func(t *testing.T) {
			fixed, events, evidence, replies := m9HistoricalFixture(t)
			calls := m9TestHistoryCalls(events, replies)
			switch name {
			case "missing-transport":
				events = append(events[:len(events)-2], events[len(events)-1])
				for n, event := range events {
					event["trace_seq"] = float64(n + 1)
				}
			case "failed-transport":
				events[len(events)-2]["transport_ok"] = false
			case "outside-first-call":
				calls[0].After = 1
			case "missing-call":
				calls = nil
			case "unknown-payload-field":
				evidence["ev_origin"]["invented"] = true
			case "repeated-transport":
				marker := events[len(events)-1]
				events[len(events)-1] = maps.Clone(events[len(events)-2])
				events = append(events, marker)
				for n, event := range events {
					event["trace_seq"] = float64(n + 1)
				}
				calls = m9TestHistoryCalls(events, replies)
			}
			if _, err := m9HistoricalResult(events, fixed, evidence, replies, calls); err == nil {
				t.Fatal("missing/ambiguous causal transport-to-selected edge qualified")
			}
		})
	}
	fixed, events, evidence, replies := m9HistoricalFixture(t)
	selected := evidence["ev_origin"]
	selected["pre_edit_document_revision"], selected["state_seq"] = "docrev_before", float64(8)
	originalTime := selected["observed_at"]
	result, err := m9HistoricalResult(events, fixed, evidence, replies, m9TestHistoryCalls(events, replies))
	if err != nil {
		t.Fatal(err)
	}
	original := m9Object(result["original_evidence"])
	observation := result["original_observation"].(*m9HistoricalBatch)
	if original["observed_at"] != originalTime || observation.EvidenceID != "ev_origin" ||
		original["state_seq"] != float64(8) || original["pre_edit_document_revision"] != "docrev_before" ||
		result["analysis_completion_proven"] != false || result["clean_resolution_proven"] != false {
		t.Fatal("Go-owned additions changed original ID/time or acquired authority")
	}

	for _, name := range []string{"only-last-transport-error", "missing-same-rpc-marker", "wrong-marker-rpc",
		"marker-outside-first-call", "missing-final-call-marker", "other-process-final-marker"} {
		t.Run(name, func(t *testing.T) {
			fixed, events, evidence, replies := m9HistoricalFixture(t)
			calls := m9TestHistoryCalls(events, replies)
			if _, err := m9HistoricalResult(events, fixed, evidence, replies, calls); err != nil {
				t.Fatalf("transport-end fixture baseline is not qualified: %v", err)
			}
			want := ""
			marker := events[len(events)-1]
			switch name {
			case "only-last-transport-error":
				// Every emission succeeded and has telemetry_errors=0. Only
				// the status captured after the final transport reports failure.
				m9Object(marker["observer_status"])["errors"] = []any{"second batch transport snapshot failed"}
				want = "transport-end identity or observer error"
			case "missing-same-rpc-marker":
				events = events[:len(events)-1]
				want = "transport emission lacks later same-RPC transport-end marker"
			case "wrong-marker-rpc":
				marker["provider_rpc_id"] = float64(99)
				want = "transport emission lacks later same-RPC transport-end marker"
			case "marker-outside-first-call":
				calls[0].After = len(events) - 1 // Includes transport, excludes only its end marker.
				want = "same-RPC transport-end marker lies outside first citing call"
			case "missing-final-call-marker":
				final := m9CloneHistoricalReply(t, replies[0])
				replies = append(replies, final)
				calls = append(calls, m9HistoricalCall{Name: "diagnostics", Reply: final, Before: len(events), After: len(events)})
				want = "final citing full report lacks in-stream transport-end cut marker"
			case "other-process-final-marker":
				before := len(events)
				events = append(events,
					map[string]any{"kind": "observer_ready", "process_id": float64(92), "trace_seq": float64(1), "unix_us": float64(200)},
					map[string]any{"kind": "transport_end", "process_id": float64(92), "trace_seq": float64(2), "unix_us": float64(201),
						"provider_rpc_id": float64(41), "observer_status": map[string]any{"installed": true, "errors": []any{}}})
				final := m9CloneHistoricalReply(t, replies[0])
				replies = append(replies, final)
				m9TestHistoryCalls(events, nil)
				calls = append(calls, m9HistoricalCall{Name: "diagnostics", Reply: final, Before: before, After: len(events)})
				want = "final citing full report lacks in-stream transport-end cut marker"
			}
			if _, err := m9HistoricalResult(events, fixed, evidence, replies, calls); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("tail/cut negative missed its intended guard %q: %v", want, err)
			}
		})
	}
}

func TestM9HistoricalNativeDuplicateKeepsOriginalAssociation(t *testing.T) {
	fixed, events, evidence, replies := m9HistoricalFixture(t)
	_, raw := m9PolicyFixture(t)
	for _, event := range raw[1:] {
		event["request_id"] = "91:7:native"
		events = m9HistoryAppend(events, event)
	}
	m9Object(raw[1]["request_params"])["previousResultId"] = "opaque-5"
	result, err := m9HistoricalResult(events, fixed, evidence, replies, m9TestHistoryCalls(events, replies))
	if err != nil {
		t.Fatal(err)
	}
	represented := result["original_observation"].(*m9HistoricalBatch)
	if represented.Response["trace_seq"] != float64(5) || represented.Response["request_id"] != "91:7:1" ||
		represented.EvidenceID != "ev_origin" || len(probeSlice(result["identical_later_callbacks"])) != 1 {
		t.Fatal("native identical callback moved the old batch's origin")
	}
}

func TestM9HistoricalSynchronousCallbackHasNoInventedInflight(t *testing.T) {
	fixed, events, evidence, replies := m9HistoricalFixture(t)
	dispatch := events[3]
	events = append(events[:3], events[4:]...)
	events = append(events, dispatch)
	for n, event := range events {
		event["trace_seq"] = float64(n + 1)
	}
	m9Object(m9Object(events[4]["origin"])["raw"])["callback_seq"] = float64(4)
	result, err := m9HistoricalResult(events, fixed, evidence, replies, m9TestHistoryCalls(events, replies))
	if err != nil || result["known_work_pending"] != false {
		t.Fatalf("synchronous callback left invented inflight state: %+v %v", result, err)
	}
}
