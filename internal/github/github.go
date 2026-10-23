// Package github is a tiny unauthenticated GitHub REST client. It sends
// If-None-Match on every repeat request (a 304 is free against the 60/hour
// limit) and caches immutable objects (trees, comparisons) for good.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is a 404, which is also what a private repo looks like.
var ErrNotFound = errors.New("github: not found (or private)")

// ErrRateLimited means the hourly budget is spent until the reset time.
var ErrRateLimited = errors.New("github: rate limited")

// Client talks to the GitHub API.
type Client struct {
	Base      string
	UserAgent string
	HTTP      *http.Client

	mu        sync.Mutex
	etags     map[string]etagEntry
	trees     map[string]Tree
	compares  map[string]Comparison
	commits   map[string]Commit
	limitedTo time.Time
	Remaining int
}

type etagEntry struct {
	etag string
	body []byte
}

// New returns a client for base (normally https://api.github.com).
func New(base, ua string) *Client {
	return &Client{
		Base: strings.TrimRight(base, "/"), UserAgent: ua,
		HTTP:  &http.Client{Timeout: 20 * time.Second},
		etags: map[string]etagEntry{}, trees: map[string]Tree{}, compares: map[string]Comparison{}, commits: map[string]Commit{},
		Remaining: -1,
	}
}

func (c *Client) get(ctx context.Context, path string, v any) error {
	c.mu.Lock()
	if time.Now().Before(c.limitedTo) {
		until := c.limitedTo
		c.mu.Unlock()
		return fmt.Errorf("%w until %s", ErrRateLimited, until.Format(time.RFC3339))
	}
	cached, haveCached := c.etags[path]
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return fmt.Errorf("github: request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.UserAgent)
	if haveCached {
		req.Header.Set("If-None-Match", cached.etag)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("github: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	c.noteLimits(resp)

	var body []byte
	switch {
	case resp.StatusCode == http.StatusNotModified && haveCached:
		body = cached.body
	case resp.StatusCode == http.StatusOK:
		body, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return fmt.Errorf("github: read %s: %w", path, err)
		}
		if et := resp.Header.Get("ETag"); et != "" {
			c.mu.Lock()
			c.etags[path] = etagEntry{etag: et, body: body}
			c.mu.Unlock()
		}
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", ErrRateLimited, resp.Status)
	default:
		return fmt.Errorf("github: GET %s: %s", path, resp.Status)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("github: decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) noteLimits(resp *http.Response) {
	rem, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Remaining = rem
	if rem == 0 {
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			c.limitedTo = time.Unix(reset, 0)
		}
	}
}

// Commit is the part of a commit the dashboard shows.
type Commit struct {
	SHA     string
	Subject string
	Time    time.Time
	Tree    string
}

type apiCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message   string `json:"message"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	} `json:"commit"`
}

func (a apiCommit) toCommit() Commit {
	subject, _, _ := strings.Cut(a.Commit.Message, "\n")
	return Commit{SHA: a.SHA, Subject: subject, Time: a.Commit.Committer.Date, Tree: a.Commit.Tree.SHA}
}

// Commits lists the newest n commits on a branch, newest first.
func (c *Client) Commits(ctx context.Context, repo, branch string, n int) ([]Commit, error) {
	var raw []apiCommit
	path := fmt.Sprintf("/repos/%s/commits?sha=%s&per_page=%d", repo, url.QueryEscape(branch), n)
	if err := c.get(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := make([]Commit, len(raw))
	for i, r := range raw {
		out[i] = r.toCommit()
		c.mu.Lock()
		c.commits[repo+"@"+r.SHA] = out[i]
		c.mu.Unlock()
	}
	return out, nil
}

// Commit fetches one commit by SHA; commits never change, so it is cached.
func (c *Client) Commit(ctx context.Context, repo, sha string) (Commit, error) {
	c.mu.Lock()
	if cm, ok := c.commits[repo+"@"+sha]; ok {
		c.mu.Unlock()
		return cm, nil
	}
	c.mu.Unlock()
	var raw apiCommit
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/commits/%s", repo, sha), &raw); err != nil {
		return Commit{}, err
	}
	cm := raw.toCommit()
	c.mu.Lock()
	c.commits[repo+"@"+sha] = cm
	c.mu.Unlock()
	return cm, nil
}

// Entry is one file or folder in a tree.
type Entry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// Tree is a commit's full file list.
type Tree struct {
	SHA       string  `json:"sha"`
	Entries   []Entry `json:"tree"`
	Truncated bool    `json:"truncated"`
}

const maxTrees = 16

// Tree fetches a recursive tree; trees are immutable, so a few are kept.
func (c *Client) Tree(ctx context.Context, repo, sha string) (Tree, error) {
	key := repo + "@" + sha
	c.mu.Lock()
	if t, ok := c.trees[key]; ok {
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()
	var t Tree
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/git/trees/%s?recursive=1", repo, sha), &t); err != nil {
		return Tree{}, err
	}
	c.mu.Lock()
	if len(c.trees) >= maxTrees {
		c.trees = map[string]Tree{} // crude, but trees are refetched rarely
	}
	c.trees[key] = t
	delete(c.etags, fmt.Sprintf("/repos/%s/git/trees/%s?recursive=1", repo, sha))
	c.mu.Unlock()
	return t, nil
}

// Comparison says how far head is ahead of base.
type Comparison struct {
	Status   string `json:"status"`
	AheadBy  int    `json:"ahead_by"`
	BehindBy int    `json:"behind_by"`
}

// Compare compares two commits; both are fixed SHAs, so the answer is cached.
func (c *Client) Compare(ctx context.Context, repo, base, head string) (Comparison, error) {
	key := repo + "@" + base + "..." + head
	c.mu.Lock()
	if cmp, ok := c.compares[key]; ok {
		c.mu.Unlock()
		return cmp, nil
	}
	c.mu.Unlock()
	var cmp Comparison
	path := fmt.Sprintf("/repos/%s/compare/%s...%s?per_page=1", repo, base, head)
	if err := c.get(ctx, path, &cmp); err != nil {
		return Comparison{}, err
	}
	c.mu.Lock()
	c.compares[key] = cmp
	delete(c.etags, path)
	c.mu.Unlock()
	return cmp, nil
}
