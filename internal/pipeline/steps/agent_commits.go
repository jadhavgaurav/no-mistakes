package steps

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// errAgentCommitsNotLinear marks agent commits that are not a plain chain of
// single-parent commits on the recorded head, so their messages cannot be
// rewritten without restructuring history.
var errAgentCommitsNotLinear = errors.New("agent commits are not a linear chain from the recorded head")

// conformAgentCommits applies the repository's commit settings to commits a fix
// agent made itself. When the agent runs `git commit`, the worktree is clean and
// the pipeline would otherwise adopt the agent's subject and trailers as they
// are, bypassing commit.fix_message and commit.trailers.
//
// The rewrite is anchored to the head the run has recorded, the same anchor
// assertPipelineHeadContinuity and ciHeadAwaitsRecording use: every commit above
// sctx.Run.HeadSHA is work the pipeline has not recorded yet, including commits
// an earlier failed agent turn left behind. Only a linear, non-merge chain on
// that head is rewritten. Trees, authors and parents are kept (only the message
// changes) and HEAD moves with a compare-and-swap against the head observed. An
// unexpected shape or a git failure returns an error with the agent's commits
// untouched, so the change is never dropped and never published unshaped.
func conformAgentCommits(sctx *pipeline.StepContext, stepName types.StepName, render func() (string, error)) error {
	anchor := strings.TrimSpace(sctx.Run.HeadSHA)
	if anchor == "" {
		return nil
	}
	head, err := stepGitHeadSHA(sctx)
	if err != nil {
		return fmt.Errorf("resolve agent head: %w", err)
	}
	if head == anchor {
		return nil
	}
	// An unfinished rebase or merge makes HEAD a partial result; leave it alone.
	if rebaseInProgress(sctx.Ctx, sctx.WorkDir) || mergeInProgress(sctx.Ctx, sctx.WorkDir) {
		return nil
	}
	descends, err := headDescendsFrom(sctx, anchor, head)
	if err != nil {
		return fmt.Errorf("inspect %s agent commits: %w", stepName, err)
	}
	if !descends {
		// A rebase rewrote history instead of extending the recorded head. That
		// is not the agent adding commits; continuity checks own that case.
		return nil
	}
	chain, err := agentCommitChain(sctx, anchor, head)
	if err != nil {
		return fmt.Errorf("inspect %s agent commits: %w", stepName, err)
	}
	message, err := render()
	if err != nil {
		return err
	}

	parent, changed := anchor, false
	for _, sha := range chain {
		info, err := stepGitRun(sctx, "show", "-s", "--date=raw", "--format=%T%x1f%an%x1f%ae%x1f%ad%x1f%B", sha)
		if err != nil {
			return fmt.Errorf("read %s agent commit %s: %w", stepName, sha, err)
		}
		fields := strings.SplitN(info, "\x1f", 5)
		if len(fields) != 5 {
			return fmt.Errorf("read %s agent commit %s: unexpected git output", stepName, sha)
		}
		if !changed && strings.TrimSpace(fields[4]) == message {
			parent = sha
			continue
		}
		changed = true
		env := []string{"GIT_AUTHOR_NAME=" + fields[1], "GIT_AUTHOR_EMAIL=" + fields[2], "GIT_AUTHOR_DATE=" + fields[3]}
		parent, err = stepGitRunEnv(sctx, env, "commit-tree", fields[0], "-p", parent, "-m", message)
		if err != nil {
			return fmt.Errorf("rewrite %s agent commit %s: %w", stepName, sha, err)
		}
	}
	if !changed {
		return nil
	}
	if _, err := stepGitRun(sctx, "update-ref", "-m", "no-mistakes: apply commit settings to agent commits", "HEAD", parent, head); err != nil {
		return fmt.Errorf("move head to rewritten %s agent commits: %w", stepName, err)
	}
	sctx.Log(fmt.Sprintf("applied commit settings to %d agent commit(s)", len(chain)))
	return nil
}

// headDescendsFrom reports whether anchor is an ancestor of head. Exit status 1
// is git's answer "no"; any other failure is an error.
func headDescendsFrom(sctx *pipeline.StepContext, anchor, head string) (bool, error) {
	_, err := stepGitRun(sctx, "merge-base", "--is-ancestor", anchor, head)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// agentCommitChain lists the commits in anchor..head oldest first and
// requires them to be a single-parent chain that starts at anchor.
func agentCommitChain(sctx *pipeline.StepContext, anchor, head string) ([]string, error) {
	out, err := stepGitRun(sctx, "rev-list", "--reverse", "--parents", anchor+".."+head)
	if err != nil {
		return nil, err
	}
	var chain []string
	parent := anchor
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != parent {
			return nil, fmt.Errorf("commits %s..%s: %w; refusing to publish agent commits that bypass the commit settings", anchor, head, errAgentCommitsNotLinear)
		}
		chain = append(chain, fields[0])
		parent = fields[0]
	}
	if parent != head {
		return nil, fmt.Errorf("commits %s..%s do not end at the observed head", anchor, head)
	}
	return chain, nil
}
