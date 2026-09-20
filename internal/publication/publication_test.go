package publication

import (
	"context"
	"errors"
	"testing"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

type fakeRemote struct {
	pushes, finds, creates      int
	pushErr, findErr, createErr error
	pr                          PullRequest
	found                       bool
}

func (f *fakeRemote) Push(ctx context.Context, _ string, _ string, _ string) error {
	f.pushes++
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.pushErr
}
func (f *fakeRemote) FindOpenPullRequest(context.Context, string, string, string) (PullRequest, bool, error) {
	f.finds++
	return f.pr, f.found, f.findErr
}
func (f *fakeRemote) CreatePullRequest(context.Context, string, string, string, string, string) (PullRequest, error) {
	f.creates++
	return f.pr, f.createErr
}

func request() Request {
	return Request{Repository: "owner/repo", Branch: "codex/task-1", BaseBranch: "main", CommitSHA: testSHA, Title: "task-1", Body: "body"}
}

func TestPublishPushesAndReusesExistingPR(t *testing.T) {
	f := &fakeRemote{found: true, pr: PullRequest{Number: 7, URL: "https://example/pr/7"}}
	r, err := (Publisher{Remote: f}).Publish(context.Background(), request())
	if err != nil || r.State != StatePRReady || r.PR.Number != 7 {
		t.Fatalf("Publish() = %+v, %v", r, err)
	}
	if f.pushes != 1 || f.finds != 1 || f.creates != 0 {
		t.Fatalf("calls = push %d find %d create %d", f.pushes, f.finds, f.creates)
	}
}

func TestPublishCreatesPRWhenNoneExists(t *testing.T) {
	f := &fakeRemote{pr: PullRequest{Number: 8}}
	r, err := (Publisher{Remote: f}).Publish(context.Background(), request())
	if err != nil || r.State != StatePRReady || f.creates != 1 {
		t.Fatalf("Publish() = %+v, %v; creates=%d", r, err, f.creates)
	}
}

func TestTimeoutIsUnknownAndReconcileNeverPushes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeRemote{}
	r, err := (Publisher{Remote: f}).Publish(ctx, request())
	if !errors.Is(err, ErrUnknown) || r.State != StateUnknown {
		t.Fatalf("Publish() = %+v, %v; want UNKNOWN", r, err)
	}
	if f.pushes != 1 {
		t.Fatalf("pushes=%d, want one attempted push", f.pushes)
	}
	found := &fakeRemote{found: true, pr: PullRequest{Number: 9}}
	r, err = (Publisher{Remote: found}).Reconcile(context.Background(), request())
	if err != nil || r.State != StatePRReady || found.pushes != 0 || found.finds != 1 {
		t.Fatalf("Reconcile() = %+v, %v; calls push=%d find=%d", r, err, found.pushes, found.finds)
	}
}

func TestInvalidRequestFailsBeforeRemote(t *testing.T) {
	f := &fakeRemote{}
	req := request()
	req.CommitSHA = "bad"
	if _, err := (Publisher{Remote: f}).Publish(context.Background(), req); err == nil {
		t.Fatal("Publish() succeeded for invalid SHA")
	}
	if f.pushes != 0 {
		t.Fatalf("pushes=%d, want zero", f.pushes)
	}
}
