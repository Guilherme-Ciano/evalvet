package junit

import (
	"encoding/xml"
	"fmt"
	"io"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

type failure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

type testcase struct {
	Name      string   `xml:"name,attr"`
	Classname string   `xml:"classname,attr"`
	Time      string   `xml:"time,attr"`
	Failure   *failure `xml:"failure,omitempty"`
	Error     *failure `xml:"error,omitempty"`
}

type testsuite struct {
	Name     string     `xml:"name,attr"`
	Tests    int        `xml:"tests,attr"`
	Failures int        `xml:"failures,attr"`
	Errors   int        `xml:"errors,attr"`
	Cases    []testcase `xml:"testcase"`
}

type testsuites struct {
	XMLName  xml.Name    `xml:"testsuites"`
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Errors   int         `xml:"errors,attr"`
	Suites   []testsuite `xml:"testsuite"`
}

func flagged(r domain.RunResult, target string) map[string]domain.CaseDiff {
	out := map[string]domain.CaseDiff{}
	for _, c := range r.Comparisons {
		if c.Head != target || !c.Verdict.Failing() {
			continue
		}
		for _, cd := range c.Cases {
			if cd.PAdjusted < r.Method.Alpha {
				out[cd.CaseID] = cd
			}
		}
	}
	return out
}

func Write(w io.Writer, r domain.RunResult) error {
	root := testsuites{Name: "evalvet." + r.Suite}
	for _, t := range r.Targets {
		ts := testsuite{Name: r.Suite + "." + t.Target}
		regressed := flagged(r, t.Target)
		for _, cs := range t.Cases {
			tc := testcase{Name: cs.CaseID, Classname: r.Suite + "." + t.Target, Time: "0"}
			switch {
			case cs.N == 0 && cs.Errors > 0:
				tc.Error = &failure{Message: fmt.Sprintf("%d trial(s) errored", cs.Errors), Type: "error", Body: cs.FirstFailure}
				ts.Errors++
			case regressed[cs.CaseID].CaseID != "":
				cd := regressed[cs.CaseID]
				tc.Failure = &failure{
					Message: fmt.Sprintf("regressed %d/%d → %d/%d (Holm p=%.4f)", cd.BasePasses, cd.BaseN, cd.HeadPasses, cd.HeadN, cd.PAdjusted),
					Type:    "regression", Body: cs.FirstFailure,
				}
				ts.Failures++
			case cs.N > 0 && cs.Passes*2 < cs.N:
				tc.Failure = &failure{Message: fmt.Sprintf("%d/%d trials passed", cs.Passes, cs.N), Type: "assertion", Body: cs.FirstFailure}
				ts.Failures++
			}
			ts.Cases = append(ts.Cases, tc)
		}
		ts.Tests = len(ts.Cases)
		root.Tests += ts.Tests
		root.Failures += ts.Failures
		root.Errors += ts.Errors
		root.Suites = append(root.Suites, ts)
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return fmt.Errorf("write junit: %w", err)
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("write junit: %w", err)
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return fmt.Errorf("write junit: %w", err)
	}
	return nil
}
