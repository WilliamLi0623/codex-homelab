package publication

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var ErrUnknown = errors.New("publication outcome unknown")

type State string

const (
	StateLocal   State = "LOCAL"
	StatePushed  State = "PUSHED"
	StatePRReady State = "PR_READY"
	StateUnknown State = "UNKNOWN"
)

type Request struct {
	Repository string
	Branch     string
	CommitSHA  string
	BaseBranch string
	Title      string
	Body       string
}

type PullRequest struct {
	Number  int
	URL     string
	HeadRef string
	BaseRef string
}

// Remote owns the side effects of branch publication and PR management. The
// controller uses this boundary so a transport timeout never becomes an
// implicit retry or a fabricated success.
type Remote interface {
	Push(ctx context.Context, repository, branch, commitSHA string) error
	FindOpenPullRequest(ctx context.Context, repository, headBranch, baseBranch string) (PullRequest, bool, error)
	CreatePullRequest(ctx context.Context, repository, headBranch, baseBranch, title, body string) (PullRequest, error)
}

type Result struct {
	State State
	PR    PullRequest
}

type Publisher struct{ Remote Remote }

func (p Publisher) Publish(ctx context.Context, req Request) (Result, error) {
	if err := validate(req); err != nil {
		return Result{}, err
	}
	if p.Remote == nil {
		return Result{}, errors.New("publication remote is not configured")
	}
	if err := p.Remote.Push(ctx, req.Repository, req.Branch, req.CommitSHA); err != nil {
		if ctx.Err() != nil {
			return Result{State: StateUnknown}, fmt.Errorf("push: %w: %v", ErrUnknown, ctx.Err())
		}
		return Result{}, fmt.Errorf("push: %w", err)
	}
	return p.ensurePR(ctx, req)
}

// Reconcile resolves a prior UNKNOWN result only by observing the branch and
// PR. It never calls Push, so retrying reconciliation cannot duplicate a
// remote write.
func (p Publisher) Reconcile(ctx context.Context, req Request) (Result, error) {
	if err := validate(req); err != nil {
		return Result{}, err
	}
	if p.Remote == nil {
		return Result{}, errors.New("publication remote is not configured")
	}
	return p.ensurePR(ctx, req)
}

type branchObserver interface {
	ObserveBranch(ctx context.Context, repository, branch string) (string, bool, error)
}

func (p Publisher) ensurePR(ctx context.Context, req Request) (Result, error) {
	pr, found, err := p.Remote.FindOpenPullRequest(ctx, req.Repository, req.Branch, req.BaseBranch)
	if err != nil {
		if ctx.Err() != nil {
			return Result{State: StateUnknown}, fmt.Errorf("find PR: %w: %v", ErrUnknown, ctx.Err())
		}
		return Result{}, fmt.Errorf("find PR: %w", err)
	}
	if found {
		return Result{State: StatePRReady, PR: pr}, nil
	}
	pr, err = p.Remote.CreatePullRequest(ctx, req.Repository, req.Branch, req.BaseBranch, req.Title, req.Body)
	if err != nil {
		if ctx.Err() != nil {
			return Result{State: StateUnknown}, fmt.Errorf("create PR: %w: %v", ErrUnknown, ctx.Err())
		}
		return Result{}, fmt.Errorf("create PR: %w", err)
	}
	return Result{State: StatePRReady, PR: pr}, nil
}

var sha40 = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func validate(req Request) error {
	if !validRepository(req.Repository) {
		return fmt.Errorf("invalid repository %q", req.Repository)
	}
	if !validBranch(req.Branch) {
		return fmt.Errorf("invalid branch %q", req.Branch)
	}
	if !sha40.MatchString(req.CommitSHA) {
		return errors.New("commit SHA must be exactly 40 hexadecimal characters")
	}
	if !validBranch(req.BaseBranch) {
		return fmt.Errorf("invalid base branch %q", req.BaseBranch)
	}
	if strings.TrimSpace(req.Title) == "" {
		return errors.New("pull request title must not be empty")
	}
	return nil
}

func validRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && repositoryName.MatchString(parts[0]) && repositoryName.MatchString(parts[1])
}

func validBranch(branch string) bool {
	return branchName.MatchString(branch) && !strings.Contains(branch, "..") && !strings.Contains(branch, "//") && !strings.HasSuffix(branch, ".") && !strings.HasSuffix(branch, "/")
}
