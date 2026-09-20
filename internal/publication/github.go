package publication

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// GitHubClient implements PR lookup/create only. Branch publication remains a
// separate git operation so a GitHub API outage cannot be confused with push
// success.
type GitHubClient struct {
	BaseURL string
	Token   string
	HTTP    HTTPDoer
}

func (c GitHubClient) FindOpenPullRequest(ctx context.Context, repository, headBranch, baseBranch string) (PullRequest, bool, error) {
	if !validRepository(repository) || !validBranch(headBranch) || !validBranch(baseBranch) {
		return PullRequest{}, false, fmt.Errorf("invalid github repository or branch")
	}
	query := url.Values{}
	query.Set("state", "open")
	query.Set("head", repositoryOwner(repository)+":"+headBranch)
	query.Set("base", baseBranch)
	var prs []githubPullRequest
	if err := c.request(ctx, http.MethodGet, "/repos/"+repository+"/pulls?"+query.Encode(), nil, &prs); err != nil {
		return PullRequest{}, false, err
	}
	for _, pr := range prs {
		if pr.Head.Ref == headBranch && pr.Base.Ref == baseBranch {
			return pr.toPullRequest(), true, nil
		}
	}
	return PullRequest{}, false, nil
}

func (c GitHubClient) CreatePullRequest(ctx context.Context, repository, headBranch, baseBranch, title, body string) (PullRequest, error) {
	if !validRepository(repository) || !validBranch(headBranch) || !validBranch(baseBranch) || strings.TrimSpace(title) == "" {
		return PullRequest{}, fmt.Errorf("invalid github repository, branch, or title")
	}
	payload := map[string]string{"head": headBranch, "base": baseBranch, "title": title, "body": body}
	var pr githubPullRequest
	if err := c.request(ctx, http.MethodPost, "/repos/"+repository+"/pulls", payload, &pr); err != nil {
		return PullRequest{}, err
	}
	return pr.toPullRequest(), nil
}

type githubPullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	Head   struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p githubPullRequest) toPullRequest() PullRequest {
	return PullRequest{Number: p.Number, URL: p.URL, HeadRef: p.Head.Ref, BaseRef: p.Base.Ref}
}

func (c GitHubClient) request(ctx context.Context, method, path string, body any, out any) error {
	if c.HTTP == nil {
		return fmt.Errorf("github HTTP client is not configured")
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode github request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create github request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("github request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("github request returned HTTP %s", res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}
	return nil
}

func repositoryOwner(repository string) string {
	if i := strings.IndexByte(repository, '/'); i >= 0 {
		return repository[:i]
	}
	return repository
}
