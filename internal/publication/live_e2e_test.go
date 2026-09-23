//go:build p14live

package publication

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
)

type liveGitHubRemote struct {
	client GitHubClient
}

func (r liveGitHubRemote) Push(ctx context.Context, _, _, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("live probe refuses an untracked push")
}

func (r liveGitHubRemote) FindOpenPullRequest(ctx context.Context, repository, headBranch, baseBranch string) (PullRequest, bool, error) {
	return r.client.FindOpenPullRequest(ctx, repository, headBranch, baseBranch)
}

func (r liveGitHubRemote) CreatePullRequest(ctx context.Context, repository, headBranch, baseBranch, title, body string) (PullRequest, error) {
	return r.client.CreatePullRequest(ctx, repository, headBranch, baseBranch, title, body)
}

func TestP14LivePublication(t *testing.T) {
	repository := os.Getenv("P14_LIVE_REPOSITORY")
	token := os.Getenv("P14_LIVE_TOKEN")
	branch := os.Getenv("P14_LIVE_BRANCH")
	commitSHA := os.Getenv("P14_LIVE_COMMIT")
	if repository == "" || token == "" || branch == "" || commitSHA == "" {
		t.Fatal("P14_LIVE_REPOSITORY, P14_LIVE_TOKEN, P14_LIVE_BRANCH, and P14_LIVE_COMMIT are required")
	}
	baseBranch := os.Getenv("P14_LIVE_BASE")
	if baseBranch == "" {
		baseBranch = "main"
	}
	client := GitHubClient{Token: token, HTTP: http.DefaultClient}
	remote := liveGitHubRemote{client: client}
	publisher := Publisher{Remote: remote}
	req := Request{
		Repository: repository,
		Branch:     branch,
		CommitSHA:  commitSHA,
		BaseBranch: baseBranch,
		Title:      "P14 publication E2E",
		Body:       "Disposable P14 publication evidence.",
	}

	timeoutCtx, cancel := context.WithCancel(context.Background())
	cancel()
	unknown, err := publisher.Publish(timeoutCtx, req)
	if !errors.Is(err, ErrUnknown) || unknown.State != StateUnknown {
		t.Fatalf("timed-out publish = %+v, %v; want UNKNOWN", unknown, err)
	}

	first, err := publisher.Reconcile(context.Background(), req)
	if err != nil || first.State != StatePRReady || first.PR.Number == 0 {
		t.Fatalf("first reconcile = %+v, %v", first, err)
	}
	second, err := publisher.Reconcile(context.Background(), req)
	if err != nil || second.State != StatePRReady || second.PR.Number != first.PR.Number {
		t.Fatalf("second reconcile = %+v, %v; want PR reuse", second, err)
	}
}
