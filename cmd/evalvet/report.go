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

func newReportCmd(c *cli) *cobra.Command {
	var (
		in          string
		formatName  string
		comment     bool
		stepSummary bool
		annotations bool
	)
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Render a results.json as table, Markdown, JUnit or JSON, or post it as a sticky PR comment",
		RunE: func(cmd *cobra.Command, _ []string) error {
			results, err := readResults(in)
			if err != nil {
				return err
			}
			for _, r := range results {
				var md bytes.Buffer
				if err := markdown.Write(&md, r); err != nil {
					return err
				}
				switch formatName {
				case "md", "markdown":
					_, err = c.streams.Out.Write(md.Bytes())
				case "table":
					err = table.Write(c.streams.Out, r)
				case "junit":
					err = junit.Write(c.streams.Out, r)
				case "json":
					err = jsonr.Write(c.streams.Out, r)
				case "none":
				default:
					err = usageError("unknown format %q", formatName)
				}
				if err != nil {
					return err
				}
				if annotations {
					if err := github.Annotations(c.streams.Out, r); err != nil {
						return err
					}
				}
				if stepSummary {
					if p := c.getenv("GITHUB_STEP_SUMMARY"); p != "" {
						if err := github.AppendStepSummary(p, md.String()); err != nil {
							return err
						}
					}
				}
				if comment {
					if err := c.postComment(cmd, r.Suite, md.String()); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&in, "in", ".evalvet/out", "results.json file or a directory containing them")
	cmd.Flags().StringVar(&formatName, "format", "md", "output format: md, table, junit, json or none")
	cmd.Flags().BoolVar(&comment, "github-comment", false, "create or update the sticky PR comment (needs GITHUB_TOKEN)")
	cmd.Flags().BoolVar(&stepSummary, "step-summary", false, "append the Markdown report to GITHUB_STEP_SUMMARY")
	cmd.Flags().BoolVar(&annotations, "annotations", false, "print ::error annotations for regressed cases")
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
