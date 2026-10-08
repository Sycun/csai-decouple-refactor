package update

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// stateFile records what the last update moved, so a rollback has a target that is not
// guessed at. It lives in the install tree and is deliberately not in git: it describes
// this machine's installation.
const stateFile = ".update-state.json"

// State is the rollback contract of one installation.
type State struct {
	PreviousCommit string `json:"previous_commit"`
	UpdatedCommit  string `json:"updated_commit"`
	BackupDir      string `json:"backup_dir"`
	UpdatedAt      string `json:"updated_at"`
}

func statePath(root string) string { return filepath.Join(root, stateFile) }

func readState(root string) (State, bool) {
	var st State
	data, err := os.ReadFile(statePath(root))
	if err != nil {
		return st, false
	}
	if err := json.Unmarshal(data, &st); err != nil || st.PreviousCommit == "" || st.UpdatedCommit == "" {
		return st, false
	}
	return st, true
}

func writeState(root string, st State) error {
	st.UpdatedAt = time.Now().Format(time.RFC3339)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := statePath(root) + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(root))
}

// Step is one progress line. A build takes minutes, so Apply reports where it is rather
// than appearing to hang; the console renders these directly.
type Step struct {
	Phase   string `json:"phase"`
	Message string `json:"message"`
	At      string `json:"at"`
}

// Result is what one update attempt produced. Everything the operator would otherwise
// have to go and inspect by hand is in here.
type Result struct {
	FromCommit   string `json:"fromCommit"`
	ToCommit     string `json:"toCommit"`
	Commits      int    `json:"commits"`
	FilesTouched int    `json:"filesTouched"`
	// KeptContent names the operator-owned files that were restored over the update.
	// Upstream may well have changed them; saying which ones is the difference between
	// "my roles disappeared" and "my roles are still mine, and these upstream files did
	// not land".
	KeptContent []string `json:"keptContent"`
	// Overwritten names local files whose content the target repository's version replaced
	// during an adoption; copies are kept under <BackupDir>/overwritten/ so nothing is lost
	// without a way back.
	Overwritten []string `json:"overwritten,omitempty"`
	// Adopted marks a result that comes from connecting a plain directory to a source
	// rather than from moving an existing work tree.
	Adopted      bool   `json:"adopted,omitempty"`
	BinaryPath   string `json:"binaryPath"`
	BinaryBuilt  bool   `json:"binaryBuilt"`
	PrevBinary   string `json:"prevBinary"`
	BackupDir    string `json:"backupDir"`
	NeedsRestart bool   `json:"needsRestart"`
	Duration     string `json:"duration"`
}

// Error is a refusal with the information needed to act on it. Callers render the
// Message; Details is for the page to list file names or commits.
type Error struct {
	Reason  string   `json:"reason"`
	Message string   `json:"message"`
	Items   []string `json:"items,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Apply carries an installation to the tip of the branch it tracks, then rebuilds and
// swaps the binary. It is all-or-nothing up to the point of the working-tree move: a
// failed build leaves the tree updated (source and previous binary are both recoverable),
// while a refused precondition leaves nothing touched at all.
func Apply(ctx context.Context, opts Options, onStep func(Step)) (*Result, error) {
	if onStep == nil {
		onStep = func(Step) {}
	}
	step := func(phase, msg string) { onStep(Step{Phase: phase, Message: msg, At: time.Now().Format(time.RFC3339)}) }

	snap, err := Check(ctx, opts)
	if err != nil {
		return nil, err
	}
	start := time.Now()

	// A refusal is not the end of the timeline: the page renders these lines, so the first
	// one says what was looked at even when the answer is "nothing to do".
	step("preflight", fmt.Sprintf("检查安装目录 %s（分支 %s / 远端 %s / 提交 %s）", snap.Root, snap.Branch, snap.sourceLabel(), snap.Commit))

	if !snap.Installed {
		return nil, &Error{Reason: "not_a_repo", Message: "这个目录不是 git 工作树，无法自动更新"}
	}
	if snap.CheckError != "" {
		return nil, &Error{Reason: "check_failed", Message: "检查更新失败：" + snap.CheckError}
	}
	if len(snap.BlockingChanges) > 0 {
		items := make([]string, 0, len(snap.BlockingChanges))
		for _, c := range snap.BlockingChanges {
			items = append(items, fmt.Sprintf("%s (%s)", c.Path, c.Status))
		}
		return nil, &Error{
			Reason:  "local_source_edits",
			Message: fmt.Sprintf("有 %d 个源码文件被本地改过，自动更新不会覆盖它们。先提交或还原这些文件再更新。", len(items)),
			Items:   items,
		}
	}
	if snap.Diverged {
		return nil, &Error{
			Reason:  "diverged",
			Message: fmt.Sprintf("本地领先 %d 个提交、落后 %d 个提交：这是合并而不是下载，请手工处理后重试。", snap.Ahead, snap.Behind),
		}
	}
	if !snap.UpdateAvailable {
		return &Result{
			FromCommit: snap.Commit, ToCommit: snap.Commit,
			BinaryPath: filepath.Join(snap.Root, opts.binaryName()),
			Duration:   time.Since(start).Round(time.Millisecond).String(),
		}, nil
	}

	step("fetch", fmt.Sprintf("%s/%s 有 %d 个新提交：%s → %s", snap.sourceLabel(), snap.Branch, snap.Behind, snap.Commit, snap.RemoteCommit))

	// Put operator-owned content aside, and clear the paths the incoming change would
	// write to, so the fast-forward cannot be refused by "your local changes would be
	// overwritten". Nothing here deletes a file the operator made: every path is copied
	// out first and copied back after.
	root := snap.Root
	ref := snap.sourceRef()
	backupDir, kept, err := stashProtected(ctx, root, ref)
	if err != nil {
		return nil, err
	}
	step("protect", fmt.Sprintf("已暂存 %d 个本地内容文件（roles/skills/tools/数据/配置），更新完成后放回", len(kept)))

	if out, err := gitCmd(ctx, root, "merge", "--ff-only", "--no-verify", ref); err != nil {
		restoreProtected(backupDir, kept)
		return nil, &Error{Reason: "merge_failed", Message: fmt.Sprintf("快进合并失败：%v\n%s", err, strings.TrimSpace(out))}
	}
	// The merge wrote upstream's version of the content paths; the operator's copies win
	// them back, which is what "update the code, keep my work" means.
	if err := restoreProtected(backupDir, kept); err != nil {
		return nil, err
	}

	files, _ := gitRaw(ctx, root, "diff", "--name-only", "-z", snap.Commit+".."+snap.RemoteCommit)
	res := &Result{
		FromCommit:   snap.Commit,
		ToCommit:     snap.RemoteCommit,
		Commits:      snap.Behind,
		KeptContent:  kept,
		BackupDir:    backupDir,
		FilesTouched: len(nulList(files)),
	}

	if err := writeState(root, State{PreviousCommit: snap.Commit, UpdatedCommit: snap.RemoteCommit, BackupDir: backupDir}); err != nil {
		return nil, &Error{Reason: "state_unwritable", Message: "无法写入更新状态文件，回滚将不可用：" + err.Error()}
	}

	bin := filepath.Join(root, opts.binaryName())
	res.BinaryPath = bin
	if opts.binaryName() == "none" {
		step("build", "按请求跳过重新编译；源码已更新，重启后由外部构建流程产出二进制")
		res.NeedsRestart = true
		res.Duration = time.Since(start).Round(time.Millisecond).String()
		return res, nil
	}
	if !snap.CanBuild {
		res.NeedsRestart = true
		res.Duration = time.Since(start).Round(time.Millisecond).String()
		return res, &Error{
			Reason:  "no_toolchain",
			Message: "源码已更新，但本机没有 Go 工具链，无法自动编译。装好 go 后再点一次更新即可补上二进制。",
		}
	}

	step("build", "开始编译二进制（首次会下载依赖，可能需要几分钟）")
	staging := filepath.Join(root, ".update-staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return res, &Error{Reason: "staging_failed", Message: err.Error()}
	}
	defer os.RemoveAll(staging)

	newBin := filepath.Join(staging, opts.binaryName())
	if err := build(ctx, root, newBin); err != nil {
		return res, &Error{Reason: "build_failed", Message: "编译失败，二进制保持原版本：\n" + err.Error()}
	}
	prev, err := installBinary(newBin, bin)
	if err != nil {
		return res, &Error{Reason: "swap_failed", Message: err.Error()}
	}
	res.BinaryBuilt = true
	res.PrevBinary = prev
	res.NeedsRestart = true
	step("done", fmt.Sprintf("已更新 %d 个提交并换好二进制：%s → %s", res.Commits, res.FromCommit, res.ToCommit))
	res.Duration = time.Since(start).Round(time.Millisecond).String()
	return res, nil
}

// stashProtected copies every operator-owned file that stands in the way of the
// fast-forward into backupDir and removes it from the tree. Only paths the update
// actually touches are removed - a user's extra skill directory is never disturbed.
func stashProtected(ctx context.Context, root, ref string) (backupDir string, kept []string, err error) {
	ts := time.Now().Format("20060102_150405")
	backupDir = filepath.Join(root, ".update-backup", ts)

	tracked, _ := localChanges(ctx, root)
	incoming, err := gitRaw(ctx, root, "diff", "--name-only", "-z", "HEAD.."+ref)
	if err != nil {
		return "", nil, &Error{Reason: "diff_failed", Message: err.Error()}
	}
	willWrite := map[string]bool{}
	for _, f := range nulList(incoming) {
		willWrite[filepath.ToSlash(f)] = true
	}

	// A locally modified content file would refuse the merge; an untracked content file
	// whose path upstream now adds would also refuse it. Both are put aside.
	targets := map[string]bool{}
	for _, c := range tracked {
		if c.Protected && willWrite[c.Path] {
			targets[c.Path] = true
		}
	}
	for _, p := range untrackedUnder(ctx, root, willWrite) {
		targets[p] = true
	}

	written := false
	for p := range targets {
		abs := filepath.Join(root, p)
		info, statErr := os.Stat(abs)
		if statErr != nil || info.IsDir() {
			continue
		}
		if err := copyFile(abs, filepath.Join(backupDir, p), info.Mode()); err != nil {
			return backupDir, kept, &Error{Reason: "backup_failed", Message: fmt.Sprintf("备份 %s 失败：%v", p, err)}
		}
		kept = append(kept, p)
		if err := os.Remove(abs); err != nil {
			return backupDir, kept, &Error{Reason: "backup_failed", Message: fmt.Sprintf("移开 %s 失败：%v", p, err)}
		}
		written = true
	}
	if written || len(kept) > 0 {
		sort.Strings(kept)
		return backupDir, kept, nil
	}
	// Nothing to put aside: don't leave an empty backup directory behind.
	return "", nil, nil
}

// restoreProtected puts the named operator-owned copies back over whatever the merge
// wrote. The list is the one stashProtected returned: an adoption parks replaced
// non-protected files in the same backup directory (under overwritten/), and those must
// NOT be restored over the target repository's version.
func restoreProtected(backupDir string, kept []string) error {
	if backupDir == "" {
		return nil
	}
	root := filepath.Join(filepath.Dir(backupDir), "..")
	for _, rel := range kept {
		abs := filepath.Join(backupDir, filepath.FromSlash(rel))
		info, err := os.Stat(abs)
		if err != nil {
			return err
		}
		if err := copyFile(abs, filepath.Join(root, filepath.FromSlash(rel)), info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

// untrackedUnder lists untracked files under the given repository paths - the operator's
// own additions, which must be backed up but never deleted by an update.
func untrackedUnder(ctx context.Context, root string, willWrite map[string]bool) []string {
	out, err := gitRaw(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return nil
	}
	var list []string
	for _, entry := range nulList(out) {
		if len(entry) < 4 || entry[:2] != "??" {
			continue
		}
		p := filepath.ToSlash(entry[3:])
		if willWrite[p] && Protected(p) {
			list = append(list, p)
		}
	}
	return list
}

// Rollback returns an installation to the commit it came from, using the binary kept
// before the swap. It refuses unless HEAD is still exactly what the update wrote, so it
// can never discard work done after that update.
func Rollback(ctx context.Context, opts Options) (*Result, error) {
	root, err := opts.installRoot()
	if err != nil {
		return nil, err
	}
	st, ok := readState(root)
	if !ok {
		return nil, &Error{Reason: "no_state", Message: "没有可回滚的更新记录"}
	}
	bin := filepath.Join(root, opts.binaryName())
	if !fileExists(bin + ".prev") {
		return nil, &Error{Reason: "no_binary", Message: "上一次更新没有留下旧二进制，无法回滚二进制（源码仍可用 git 处理）"}
	}
	head, err := gitCmd(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	updated, err := gitCmd(ctx, root, "rev-parse", st.UpdatedCommit)
	if err != nil {
		return nil, &Error{Reason: "bad_state", Message: "更新记录里的提交在本仓库不存在：" + st.UpdatedCommit}
	}
	if head != updated {
		return nil, &Error{
			Reason:  "moved_since_update",
			Message: fmt.Sprintf("自那次更新之后 HEAD 已经变了（现在是 %s），回滚只会撤到更新前的提交，因此拒绝执行。", shortHash(head)),
		}
	}

	start := time.Now()
	if _, blocking := localChanges(ctx, root); len(blocking) > 0 {
		items := make([]string, 0, len(blocking))
		for _, c := range blocking {
			items = append(items, c.Path)
		}
		return nil, &Error{Reason: "local_source_edits", Message: "有本地源码改动，回滚会覆盖它们", Items: items}
	}

	if _, err := gitCmd(ctx, root, "reset", "--hard", "--quiet", st.PreviousCommit); err != nil {
		return nil, &Error{Reason: "reset_failed", Message: err.Error()}
	}
	if err := restorePrevBinary(bin); err != nil {
		return nil, &Error{Reason: "swap_failed", Message: err.Error()}
	}
	if err := os.Remove(statePath(root)); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return &Result{
		FromCommit:   shortHash(head),
		ToCommit:     shortHash(st.PreviousCommit),
		BinaryPath:   bin,
		BinaryBuilt:  true,
		BackupDir:    st.BackupDir,
		NeedsRestart: true,
		Duration:     time.Since(start).Round(time.Millisecond).String(),
	}, nil
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
