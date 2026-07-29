package lint

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func buildReport(t *testing.T) *Report {
	t.Helper()
	db, root := fixture(t)
	opts := fixtureOptions(root)
	res, err := Run(db, opts)
	if err != nil {
		t.Fatal(err)
	}
	backlog, err := QueryBacklog(db, opts)
	if err != nil {
		t.Fatal(err)
	}
	adoption, err := QueryAdoption(db, opts)
	if err != nil {
		t.Fatal(err)
	}
	return &Report{GeneratedAt: time.Now().UTC(), Repo: "fixture",
		Lint: res, Backlog: backlog, Adoption: adoption}
}

func TestReport_HonestyControls(t *testing.T) {
	r := buildReport(t)
	text := r.Text()
	lower := strings.ToLower(text + r.Markdown())
	for _, banned := range BannedVocabulary {
		if strings.Contains(lower, banned) {
			t.Errorf("report used banned vocabulary %q:\n%s", banned, text)
		}
	}
	// No aggregate without its sample size (§D4 suppression rule 2).
	if !strings.Contains(text, "[n=14 entries") {
		t.Errorf("score line has no sample size:\n%s", text)
	}
	if !strings.Contains(text, "method: exact-match only") {
		t.Errorf("score line hides its method:\n%s", text)
	}
	// The band label, never the bare number.
	if !strings.Contains(text, "corpus health: 66 — significant rot") {
		t.Errorf("score is not band-labelled:\n%s", text)
	}
	// Every category line prints x of n.
	collapsed := strings.Join(strings.Fields(text), " ")
	for _, want := range []string{"dead references 2 of 3", "duplicates 4 of 14",
		"contradictions 2 of 9", "staleness 2 of 14", "hygiene 2 of 7"} {
		if !strings.Contains(collapsed, want) {
			t.Errorf("missing category line %q:\n%s", want, text)
		}
	}
	// The limitation footer is on the report, not in the docs.
	if !strings.Contains(text, "semantic contradictions and paraphrase duplicates are NOT detected") {
		t.Errorf("missing limitation footer:\n%s", text)
	}
	// Adoption is present and unscored.
	if !strings.Contains(text, "adoption (not scored") {
		t.Errorf("missing adoption section:\n%s", text)
	}
}

func TestReport_JSONCarriesRowIDsForEveryFinding(t *testing.T) {
	r := buildReport(t)
	raw, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var parsed Report
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, c := range parsed.Lint.Checks {
		for _, f := range c.Findings {
			// ADR-0004 §D6.2: a number that cannot be traced to rows may not be
			// rendered.
			if f.ID == "" {
				t.Fatalf("%s has a finding with no row id: %+v", c.ID, f)
			}
		}
	}
	if !strings.Contains(raw, `"value": 66`) {
		t.Errorf("json score does not match the text score")
	}
}

func TestReport_SuppressedScoreStillPrintsFindings(t *testing.T) {
	r := buildReport(t)
	r.Lint.Score.Suppressed = true
	r.Lint.Score.Reason = "corpus too small to score"
	text := r.Text()
	if !strings.Contains(text, "not scored — corpus too small to score") {
		t.Errorf("suppressed score not explained:\n%s", text)
	}
	if strings.Contains(text, "corpus health: 66") {
		t.Errorf("suppressed report printed a number:\n%s", text)
	}
	if !strings.Contains(r.Markdown(), "L5 — duplicates") {
		t.Errorf("markdown dropped the findings when the score was suppressed")
	}
}
