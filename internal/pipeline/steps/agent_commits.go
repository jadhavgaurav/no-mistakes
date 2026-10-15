package steps

import (
	"errors"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// errAgentCommitsNotLinear marks agent commits that are not a plain chain of
// single-parent commits on the head before the agent turn, so their messages cannot be
// rewritten without restructuring history.
var errAgentCommitsNotLinear = errors.New("agent commits are not a linear chain from the head before the agent turn")

// markAgentTurnStart records the worktree HEAD just before a fix agent turn so
// conformAgentCommits can tell the agent's own commits from everything before
// it. An unreadable HEAD leaves the anchor empty, which rewrites nothing.
func markAgentTurnStart(sctx *pipeline.StepContext) {
	head, err := stepGitHeadSHA(sctx)
	if err != nil {
		sctx.Log(fmt.Sprintf("warning: could not read head before the agent turn; its own commits keep their messages: %v", err))
		head = ""
	}
	sctx.AgentTurnStartHead = head
}

// conformAgentCommits applies the repository's commit settings to commits a fix
// agent made itself. When the agent runs `git commit`, the worktree is clean and
// the pipeline would otherwise adopt the agent's subject and trailers as they
// are, bypassing commit.fix_message and commit.trailers.
//
// The rewrite is anchored to the head observed before the agent turn: it only
// touches the linear, non-merge chain between sctx.AgentTurnStartHead and the
// worktree HEAD, keeps every tree, author and parent relationship (only the
// message changes), and moves HEAD with a compare-and-swap against the head it
// observed. An unexpected shape or a git failure returns an error with the
// agent's commits untouched, so the change is never dropped and never
// published unshaped. The anchor is consumed either way.
func conformAgentCommits(sctx *pipeline.StepContext, stepName types.StepName, render func() (string, error)) error {
	anchor := strings.TrimSpace(sctx.AgentTurnStartHead)
	sctx.AgentTurnStartHead = ""
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
