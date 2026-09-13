package workspace

// The analysis snapshot: one immutable set of facts about one exact revision,
// assembled from every contributor that has something to say about it.
//
// Before this, impact was a single regular-expression pass over imports, and
// its answer was the whole model: no way to say who claimed a relation, how
// sure they were, or what a language server would have said instead. A
// snapshot keeps each contributor's claim with its producer, confidence and
// coverage, merges duplicates without losing either provenance, and retains
// disagreements rather than picking a winner. Anything built on top - impact
// analysis, invariants, execution graphs - reads facts that name their own
// evidence.
//
// Identity is the whole input: a snapshot is keyed by workspace, epoch,
// revision, analysis profile, project configuration and the version of every
// producer. Nothing is invalidated, because nothing can be stale: a changed
// input is a different key and therefore a different snapshot. A canonical
// and a prepared revision can never share an entry for the same reason.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// maxAnalysisSnapshots bounds the cache. Snapshots are keyed by revision, so
// an editing session produces a new one every few edits and the old ones are
// never asked for again.
const maxAnalysisSnapshots = 16

// AnalysisNodeKind is what a node stands for.
type AnalysisNodeKind string

const (
	NodeFile          AnalysisNodeKind = "file"
	NodeSymbol        AnalysisNodeKind = "symbol"
	NodeInterface     AnalysisNodeKind = "interface"
	NodeSchema        AnalysisNodeKind = "schema"
	NodeConfiguration AnalysisNodeKind = "configuration"
	NodeGenerated     AnalysisNodeKind = "generated"
	NodeTest          AnalysisNodeKind = "test"
)

// AnalysisEdgeKind is what one node does to another.
type AnalysisEdgeKind string

const (
	EdgeCalls      AnalysisEdgeKind = "calls"
	EdgeImports    AnalysisEdgeKind = "imports"
	EdgeImplements AnalysisEdgeKind = "implements"
	EdgeReferences AnalysisEdgeKind = "references"
	EdgeGenerates  AnalysisEdgeKind = "generates"
	EdgeConfigures AnalysisEdgeKind = "configures"
	EdgeCovers     AnalysisEdgeKind = "covers"
	EdgeExports    AnalysisEdgeKind = "exports"
)

// ProducerVersion names one contributor and the version of whatever produced
// its facts. It is part of the key: a language server upgrade is a different
// snapshot, because it may have a different opinion.
type ProducerVersion struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// AnalysisKey is everything a snapshot's identity depends on.
type AnalysisKey struct {
	WorkspaceID ID                `json:"workspace_id"`
	Epoch       uint64            `json:"epoch"`
	Revision    string            `json:"revision"`
	Profile     string            `json:"profile"`
	ConfigHash  string            `json:"config_hash"`
	Producers   []ProducerVersion `json:"producers"`
}

// ID is the deterministic identity of the key: the same inputs in any order
// give the same snapshot id, and any difference gives another.
func (k AnalysisKey) ID() string {
	producers := append([]ProducerVersion(nil), k.Producers...)
	sort.Slice(producers, func(i, j int) bool {
		if producers[i].Name == producers[j].Name {
			return producers[i].Version < producers[j].Version
		}
		return producers[i].Name < producers[j].Name
	})
	k.Producers = producers
	encoded, err := json.Marshal(k)
	if err != nil {
		return "snap_unencodable"
	}
	return "snap_" + hashBytes(encoded)[:32]
}

// AnalysisNode is one thing the snapshot knows about. ID is stable across
// snapshots so facts from different contributors meet on the same node.
type AnalysisNode struct {
	ID       string           `json:"id"`
	Kind     AnalysisNodeKind `json:"kind"`
	Path     string           `json:"path"`
	NamePath string           `json:"name_path,omitempty"`
	Language string           `json:"language,omitempty"`
}

// NodeID is the identity of a node: what it is and where it lives, but not
// what kind of file it turned out to be. A file is one thing whether or not
// it is a test, and a contributor that records a fact about it without
// knowing that must still meet the contributor that did.
func NodeID(kind AnalysisNodeKind, path, namePath string) string {
	if kind == NodeSymbol || namePath != "" {
		return fmt.Sprintf("symbol:%s#%s", path, namePath)
	}
	return "file:" + path
}

// FactProducer is one contributor's claim about one fact: who said it, how
// sure they were, and what they could see when they said it.
type FactProducer struct {
	Name        string   `json:"name"`
	Confidence  string   `json:"confidence"`
	Coverage    Coverage `json:"coverage"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Detail      string   `json:"detail,omitempty"`
}

// AnalysisFact is one typed edge and everyone who claimed it.
type AnalysisFact struct {
	From      string           `json:"from"`
	To        string           `json:"to"`
	Kind      AnalysisEdgeKind `json:"kind"`
	Producers []FactProducer   `json:"producers"`
}

// AnalysisConflict records that contributors disagree about what relates two
// nodes. Both facts stay in the snapshot: a disagreement between a regular
// expression and a language server is information, and resolving it silently
// would throw away the only signal that one of them is wrong.
type AnalysisConflict struct {
	From  string             `json:"from"`
	To    string             `json:"to"`
	Kinds []AnalysisEdgeKind `json:"kinds"`
}

// AnalysisSnapshot is the immutable result.
type AnalysisSnapshot struct {
	ID        string             `json:"id"`
	Key       AnalysisKey        `json:"key"`
	Nodes     []AnalysisNode     `json:"nodes"`
	Facts     []AnalysisFact     `json:"facts"`
	Conflicts []AnalysisConflict `json:"conflicts,omitempty"`
	Coverage  Coverage           `json:"coverage"`
	BuiltAt   time.Time          `json:"built_at"`
}

// Producers is every contributor that was consulted, which is not the same as
// every contributor that found something: a reader that looked and saw no
// relation is evidence, and dropping its name would make "nobody asked" and
// "asked and found nothing" look alike.
func (s AnalysisSnapshot) Producers() []string {
	names := make([]string, 0, len(s.Key.Producers))
	for _, producer := range s.Key.Producers {
		names = append(names, producer.Name)
	}
	sort.Strings(names)
	return names
}

// AnalysisCaps bound one build. A snapshot that hit a cap says so in its
// coverage rather than pretending to be the whole picture.
type AnalysisCaps struct {
	MaxNodes int
	MaxFacts int
	Timeout  time.Duration
}

func (c AnalysisCaps) withDefaults() AnalysisCaps {
	if c.MaxNodes <= 0 {
		c.MaxNodes = 4000
	}
	if c.MaxFacts <= 0 {
		c.MaxFacts = 20000
	}
	if c.Timeout <= 0 {
		c.Timeout = 20 * time.Second
	}
	return c
}

// AnalysisRequest is what to analyse.
type AnalysisRequest struct {
	Key     AnalysisKey
	Root    string
	Changed []string
	Caps    AnalysisCaps
}

// AnalysisContributor is one source of facts. The native import reader is
// one; a language server is another; neither is the model.
type AnalysisContributor interface {
	Name() string
	Version() string
	Contribute(ctx context.Context, request AnalysisRequest, builder *AnalysisBuilder) error
}
