package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/Guilherme-Ciano/evalvet/internal/adapters/github"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/report/jsonr"
	"github.com/Guilherme-Ciano/evalvet/internal/report/junit"
	"github.com/Guilherme-Ciano/evalvet/internal/report/markdown"
	"github.com/Guilherme-Ciano/evalvet/internal/report/table"
)

func readResults(in string) ([]domain.RunResult, error) {
	st, err := os.Stat(in)
	if err != nil {
		return nil, usageError("read results %s: %w", in, err)
	}
	var files []string
	if st.IsDir() {
		err := filepath.WalkDir(in, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() == "results.json" {
				files = append(files, p)
			}
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", in, err)
		}
		sort.Strings(files)
	} else {
		files = []string{in}
	}
	if len(files) == 0 {
		return nil, usageError("no results.json under %s", in)
	}
	out := make([]domain.RunResult, 0, len(files))
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", p, err)
		}
		r, err := jsonr.Read(f)
		cErr := f.Close()
		if err := errors.Join(err, cErr); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, r)
	}
	return out, nil
}

type reportFlags struct {
	in          string
	format      string
	comment     bool
	stepSummary bool
	annotations bool
}

func (c *cli) writeFormat(name string, md []byte, r domain.RunResult) error {
	var err error
	switch name {
	case "md", "markdown":
		_, err = c.streams.Out.Write(md)
	case formatTable:
		err = table.Write(c.streams.Out, r)
	case "junit":
		err = junit.Write(c.streams.Out, r)
	case formatJSON:
		err = jsonr.Write(c.streams.Out, r)
	case "none":
	default:
		return usageError("unknown format %q", name)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func (c *cli) renderReport(cmd *cobra.Command, r domain.RunResult, f *reportFlags) error {
	var md bytes.Buffer
	if err := markdown.Write(&md, r); err != nil {
		return err
	}
	if err := c.writeFormat(f.format, md.Bytes(), r); err != nil {
		return err
	}
	if f.annotations {
		if err := github.Annotations(c.streams.Out, r); err != nil {
			return err
		}
	}
	if p := c.getenv("GITHUB_STEP_SUMMARY"); f.stepSummary && p != "" {
		if err := github.AppendStepSummary(p, md.String()); err != nil {
			return err
		}
	}
	if f.comment {
		return c.postComment(cmd, r.Suite, md.String())
	}
	return nil
}

func newReportCmd(c *cli) *cobra.Command {
	f := &reportFlags{}
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Render a results.json as table, Markdown, JUnit or JSON, or post it as a sticky PR comment",
		RunE: func(cmd *cobra.Command, _ []string) error {
			results, err := readResults(f.in)
			if err != nil {
				return err
			}
			for i := range results {
				if err := c.renderReport(cmd, results[i], f); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&f.in, "in", ".evalvet/out", "results.json file or a directory containing them")
	cmd.Flags().StringVar(&f.format, "format", "md", "output format: md, table, junit, json or none")
	cmd.Flags().
		BoolVar(&f.comment, "github-comment", false, "create or update the sticky PR comment (needs GITHUB_TOKEN)")
	cmd.Flags().BoolVar(&f.stepSummary, "step-summary", false, "append the Markdown report to GITHUB_STEP_SUMMARY")
	cmd.Flags().BoolVar(&f.annotations, "annotations", false, "print ::error annotations for regressed cases")
	return cmd
}

func (c *cli) postComment(cmd *cobra.Command, suite, body string) error {
	env := github.EnvFrom(c.getenv)
	pr, err := env.PullNumber()
	if errors.Is(err, github.ErrNotPullRequest) {
		fmt.Fprintln(c.streams.Err, "evalvet: not a pull request event, skipping the PR comment")
		return nil
	}
	if err != nil {
		return err
	}
	client := github.NewClient(&http.Client{Timeout: 30 * time.Second}, env)
	res, err := client.UpsertComment(cmd.Context(), pr, markdown.Marker(suite), body)
	if err != nil {
		return &ExitError{Code: ExitInfra, Err: err}
	}
	action := "updated"
	if res.Created {
		action = "created"
	}
	fmt.Fprintf(c.streams.Err, "evalvet: %s PR comment %d on #%d\n", action, res.CommentID, pr)
	return nil
}
