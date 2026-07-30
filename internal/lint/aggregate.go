package lint

import (
	"encoding/json"
	"time"
)

// Aggregate is the shape of `varve lint --aggregate`: the distribution of a
// corpus's health, carrying no content from it.
//
// Why it exists. ADR-0005 falsifier 4 pre-commits to dropping the score if it
// fails to discriminate across ≥10 real corpora, and Amendment 2 gates L6's
// return to the scored tier on ≥3. Both need data from other people's stores.
// The artifact that already existed — `lint --format json` — is the wrong thing
// to ask for: it carries row ids, decision titles, findings, and (since
// findings learned to name their origin) paths inside the user's repository.
// Asking a pilot to send that is asking for a partial dump of a private
// decision store, from a product whose README promises local-first with no
// account.
//
// Falsifier 4 asks one question — does the score discriminate across corpora? —
// and a distribution answers it. Titles do not. So this type is defined by what
// it refuses to carry: no ids, no titles, no details, no source refs, no globs,
// no paths, no topic keys. Every field below is a number, a fixed label, or a
// disclosure string this codebase wrote.
//
// It is derived from a rendered Report rather than recomputed, so the summary
// cannot drift from the report the user read. A second computation that agreed
// today would be a second computation that disagreed later.
//
// Nothing here is sent anywhere. There is no endpoint, no upload, no telemetry
// — this writes a file the user can read in full and then chooses to send, or
// not. That distinction is the whole reason the feature is acceptable.
type Aggregate struct {
	GeneratedAt time.Time `json:"generated_at"`
	// Varve and Schema let corpora be compared across releases — a score
	// distribution is only meaningful against the checks that produced it.
	Varve  string `json:"varve_version"`
	Schema int    `json:"schema_version"`

	Entries int `json:"entries"`

	Score      int    `json:"score"`
	Band       string `json:"band"`
	Suppressed bool   `json:"suppressed"`
	Reason     string `json:"reason,omitempty"`

	// Categories is the per-category arithmetic: the rates are what falsifier 4
	// actually studies, and they are ratios of counts.
	Categories []Category `json:"categories"`

	// Methods is the method-line disclosure, verbatim. §D4 makes it a property
	// of the score itself — "a score whose method is hidden is a score an
	// evaluator rightly rejects" — so a summary that dropped it would be
	// exactly the number-without-its-method the ADR forbids. It survives here
	// in both forms the user saw.
	Methods    map[string]string `json:"methods"`
	MethodLine string            `json:"method_line"`

	Adoption Adoption `json:"adoption"`
	GatedOut []string `json:"gated_out"`

	// Unscored counts L6's review candidates without describing them. The
	// glob a group shares is a path in someone's repository, and the members
	// are ids, so the tier reduces to two integers — which is all the
	// discrimination evidence Amendment 2's re-entry condition needs.
	Unscored UnscoredCounts `json:"unscored"`

	// Corrupt is a count, not the rows: topicKeyCorruption's findings name the
	// colliding topic_key and the ids holding it.
	Corrupt int `json:"corrupt"`
}

// UnscoredCounts is L6's unscored tier reduced to its shape.
type UnscoredCounts struct {
	Hubs           int `json:"hubs"`
	CandidatePairs int `json:"candidate_pairs"`
}

// NewAggregate summarizes a report. It reads the report rather than the store:
// every number here is one the user has already been shown.
func NewAggregate(r *Report, varveVersion string, schemaVersion int) *Aggregate {
	a := &Aggregate{
		GeneratedAt: r.GeneratedAt.UTC(),
		Varve:       varveVersion,
		Schema:      schemaVersion,
		Adoption:    r.Adoption,
		Methods:     map[string]string{},
		MethodLine:  r.methodLine(),
	}
	if r.Lint == nil {
		return a
	}
	a.Entries = r.Lint.Entries
	a.Corrupt = len(r.Lint.Corrupt)
	// GatedOut is check ids — "L8", "L9" — which name this codebase's checks,
	// not the user's rows.
	a.GatedOut = append([]string{}, r.Lint.GatedOut...)
	for k, v := range r.Lint.Modes {
		a.Methods[k] = v
	}
	if s := r.Lint.Score; s != nil {
		a.Score, a.Band = s.Value, s.Band
		a.Suppressed, a.Reason = s.Suppressed, s.Reason
		// Category carries only arithmetic and fixed labels, so it crosses
		// intact. If a content-bearing field is ever added to it, test 1 fails
		// — which is the point of asserting on sentinels rather than on a
		// field list that has to be kept in step by hand.
		a.Categories = append([]Category{}, s.Categories...)
	}
	if c := r.Lint.Check("L6"); c != nil {
		a.Unscored = UnscoredCounts{Hubs: len(c.Hubs), CandidatePairs: len(c.Candidates)}
	}
	return a
}

// JSON renders the aggregate. Indented, because a user who is being asked to
// send a file is entitled to read it first without tooling.
func (a *Aggregate) JSON() (string, error) {
	out, err := json.MarshalIndent(a, "", "  ")
	return string(out), err
}
