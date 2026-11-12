package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

var ErrNotPullRequest = errors.New("not a pull_request event")

type Env struct {
	Token      string
	APIURL     string
	Repository string
	EventPath  string
	EventName  string
}

func EnvFrom(lookup func(string) string) Env {
	api := lookup("GITHUB_API_URL")
	if api == "" {
		api = "https://api.github.com"
	}
	return Env{
		Token:      lookup("GITHUB_TOKEN"),
		APIURL:     strings.TrimRight(api, "/"),
		Repository: lookup("GITHUB_REPOSITORY"),
		EventPath:  lookup("GITHUB_EVENT_PATH"),
		EventName:  lookup("GITHUB_EVENT_NAME"),
	}
}

func (e Env) PullNumber() (int, error) {
	if e.EventPath == "" {
		return 0, ErrNotPullRequest
	}
	raw, err := os.ReadFile(e.EventPath)
	if err != nil {
		return 0, fmt.Errorf("read event: %w", err)
	}
	var ev struct {
		PullRequest struct {
			Number int `json:"number"`
		} `json:"pull_request"`
		Number int `json:"number"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return 0, fmt.Errorf("decode event: %w", err)
	}
	if ev.PullRequest.Number == 0 {
		return 0, ErrNotPullRequest
	}
	return ev.PullRequest.Number, nil
}

type Client struct {
	http *http.Client
	env  Env
}

func NewClient(c *http.Client, env Env) *Client {
	return &Client{http: c, env: env}
}

type comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

func (c *Client) do(ctx context.Context, method, url string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s body: %w", method, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.env.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status %d: %s", method, url, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func (c *Client) findComment(ctx context.Context, pr int, marker string) (int64, error) {
	for page := 1; page <= 50; page++ {
		url := fmt.Sprintf(
			"%s/repos/%s/issues/%d/comments?per_page=100&page=%d",
			c.env.APIURL,
			c.env.Repository,
			pr,
			page,
		)
		var list []comment
		if err := c.do(ctx, http.MethodGet, url, nil, &list); err != nil {
			return 0, err
		}
		for _, cm := range list {
			if strings.Contains(cm.Body, marker) {
				return cm.ID, nil
			}
		}
		if len(list) < 100 {
			return 0, nil
		}
	}
	return 0, nil
}

type Upsert struct {
	CommentID int64
	Created   bool
}

func (c *Client) UpsertComment(ctx context.Context, pr int, marker, body string) (Upsert, error) {
	if c.env.Token == "" || c.env.Repository == "" {
		return Upsert{}, fmt.Errorf("GITHUB_TOKEN and GITHUB_REPOSITORY are required: %w", domain.ErrConfig)
	}
	id, err := c.findComment(ctx, pr, marker)
	if err != nil {
		return Upsert{}, err
	}
	payload := map[string]string{"body": body}
	var out comment
	if id != 0 {
		url := fmt.Sprintf("%s/repos/%s/issues/comments/%d", c.env.APIURL, c.env.Repository, id)
		if err := c.do(ctx, http.MethodPatch, url, payload, &out); err != nil {
			return Upsert{}, err
		}
		return Upsert{CommentID: out.ID}, nil
	}
	url := fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.env.APIURL, c.env.Repository, pr)
	if err := c.do(ctx, http.MethodPost, url, payload, &out); err != nil {
		return Upsert{}, err
	}
	return Upsert{CommentID: out.ID, Created: true}, nil
}

func escapeData(s string) string {
	r := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
	return r.Replace(s)
}

func escapeProperty(s string) string {
	r := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C")
	return r.Replace(s)
}

func Annotations(w io.Writer, r domain.RunResult) error {
	for _, c := range r.Comparisons {
		if !c.Verdict.Failing() {
			continue
		}
		for _, cd := range c.Cases {
			if cd.PAdjusted >= r.Method.Alpha && len(c.Cases) > 0 && cd.CaseID != c.Cases[0].CaseID {
				continue
			}
			msg := fmt.Sprintf("%s regressed on %s: %d/%d → %d/%d (Holm p=%.4f). %s",
				c.Head, cd.CaseID, cd.BasePasses, cd.BaseN, cd.HeadPasses, cd.HeadN, cd.PAdjusted, cd.FirstFailure)
			line := ""
			if cd.Line > 0 {
				line = fmt.Sprintf(",line=%d", cd.Line)
			}
			if _, err := fmt.Fprintf(
				w,
				"::error file=%s%s,title=%s::%s\n",
				escapeProperty(r.SuiteFile),
				line,
				escapeProperty("evalvet "+c.Name),
				escapeData(msg),
			); err != nil {
				return fmt.Errorf("write annotation: %w", err)
			}
		}
	}
	for _, g := range r.Gates {
		if _, err := fmt.Fprintf(
			w,
			"::error file=%s,title=%s::%s\n",
			escapeProperty(r.SuiteFile),
			escapeProperty("evalvet gate "+g.Gate),
			escapeData(g.Target+": "+g.Message),
		); err != nil {
			return fmt.Errorf("write annotation: %w", err)
		}
	}
	return nil
}

func AppendStepSummary(path, body string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open step summary: %w", err)
	}
	_, wErr := f.WriteString(body + "\n")
	cErr := f.Close()
	if err := errors.Join(wErr, cErr); err != nil {
		return fmt.Errorf("write step summary: %w", err)
	}
	return nil
}
