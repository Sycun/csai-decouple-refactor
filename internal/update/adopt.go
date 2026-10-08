package update

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Adopt connects a plain directory - a tarball or zip installation, or a copy carried in by
// hand - to the configured update source, so it becomes a git work tree the one-click
// update applies to from then on.
//
// The rules are the ones an update already keeps: operator content (roles/skills/tools/
// agents/knowledge_base/data/config.yaml) is put aside and put back, and nothing else is
// replaced without having been named first - the preview lists every local file whose
// content differs from the target, and each replaced file is kept under
// <backup>/overwritten/ so "the update ate my edit" always has an address.

// maxAdoptList caps the file lists a plan carries over the wire; totals are separate so a
// truncated list can never be read as the whole truth.
const maxAdoptList = 50

// AdoptPlan is the preview of connecting a directory to a source. Producing it must not
// change the directory.
type AdoptPlan struct {
	Root    string `json:"root"`
	Source  string `json:"source"`
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Subject string `json:"subject"`

	Incoming int `json:"incoming"` // files the target branch tracks

	OverwrittenTotal int      `json:"overwrittenTotal"`
	Overwritten      []string `json:"overwritten"` // differing local files the target's version will replace (capped)

	ProtectedTotal int      `json:"protectedTotal"`
	Protected      []string `json:"protected"` // operator content put aside and put back (capped)
}

// adoptionScan is the full (uncapped) comparison an adoption is based on.
type adoptionScan struct {
	target  map[string]string // tracked path -> blob sha
	paths   []string          // every tracked path, sorted
	changed []string          // local file differs from the target and is not protected
	kept    []string          // local file differs from the target and is protected
}

// Preview builds an adoption plan without touching the directory: the fetch happens in a
// throwaway bare repository outside it, so a failure cannot leave a half-adopted tree.
func Preview(ctx context.Context, opts Options) (*AdoptPlan, error) {
	root, source, branch, err := adoptPreflight(ctx, opts)
	if err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp("", "csai-adopt-preview-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if _, err := gitCmd(ctx, tmp, "init", "--quiet", "--bare"); err != nil {
		return nil, err
	}
	branch, commit, subject, err := fetchSource(ctx, tmp, source, branch)
	if err != nil {
		return nil, err
	}
	scan, err := scanAdoption(ctx, root, tmp, "origin/"+branch)
	if err != nil {
		return nil, err
	}
	return scan.plan(root, source, branch, commit, subject), nil
}

// Adopt carries the directory onto the source. It runs its own fetch inside the directory
// (the preview's throwaway fetch cannot be reused), and on any failure removes only the
// .git it created itself, so a retry starts from the same pristine state.
func Adopt(ctx context.Context, opts Options, onStep func(Step)) (*Result, error) {
	if onStep == nil {
		onStep = func(Step) {}
	}
	step := func(phase, msg string) { onStep(Step{Phase: phase, Message: msg, At: time.Now().Format(time.RFC3339)}) }
	start := time.Now()

	root, source, branch, err := adoptPreflight(ctx, opts)
	if err != nil {
		return nil, err
	}
	step("preflight", fmt.Sprintf("把目录 %s 接入更新源 %s（分支 %s）", root, source, branch))

	if _, err := gitCmd(ctx, root, "init", "--quiet"); err != nil {
		return nil, &Error{Reason: "init_failed", Message: "git init 失败：" + err.Error()}
	}
	adopted := false
	defer func() {
		if !adopted {
			// Only the repository git just created; the directory's own files are untouched.
			_ = os.RemoveAll(filepath.Join(root, ".git"))
		}
	}()
	if _, err := gitCmd(ctx, root, "remote", "add", "origin", source); err != nil {
		return nil, &Error{Reason: "init_failed", Message: "添加远端失败：" + err.Error()}
	}

	branch, commit, subject, err := fetchSource(ctx, root, source, branch)
	if err != nil {
		return nil, err
	}
	ref := "origin/" + branch
	scan, err := scanAdoption(ctx, root, root, ref)
	if err != nil {
		return nil, err
	}
	step("fetch", fmt.Sprintf("%s/%s（%s %s）共 %d 个文件：%d 个本机版本会被目标版本替换，%d 个运维者文件先暂存再放回",
		source, branch, commit, subject, len(scan.paths), len(scan.changed), len(scan.kept)))

	backupDir := filepath.Join(root, ".update-backup", time.Now().Format("20060102_150405"))
	backupAll := append(append([]string{}, scan.changed...), scan.kept...)
	for _, p := range backupAll {
		abs := filepath.Join(root, filepath.FromSlash(p))
		info, statErr := os.Stat(abs)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		dst := filepath.Join(backupDir, "overwritten", filepath.FromSlash(p))
		if Protected(p) {
			dst = filepath.Join(backupDir, filepath.FromSlash(p))
		}
		if err := copyFile(abs, dst, info.Mode()); err != nil {
			return nil, &Error{Reason: "backup_failed", Message: fmt.Sprintf("备份 %s 失败：%v", p, err)}
		}
	}
	if len(backupAll) > 0 {
		step("protect", fmt.Sprintf("%d 个文件已留底（被替换的在 %s/overwritten/，运维者的会原样放回）", len(backupAll), backupDir))
	}

	if _, err := gitCmd(ctx, root, "checkout", "--force", "-B", branch, ref); err != nil {
		return nil, &Error{Reason: "checkout_failed", Message: "落地目标内容失败：" + err.Error()}
	}
	if err := restoreProtected(backupDir, scan.kept); err != nil {
		return nil, err
	}
	if _, err := gitCmd(ctx, root, "branch", "--set-upstream-to="+ref); err != nil {
		return nil, &Error{Reason: "upstream_failed", Message: "设置分支跟踪失败：" + err.Error()}
	}
	// From here the directory IS a work tree tracking the source: a later build failure
	// must leave it that way (the source has landed; only the binary has not), so the
	// cleanup above must not remove the repository any more.
	adopted = true

	res := &Result{
		ToCommit:     commit,
		FilesTouched: len(scan.paths),
		KeptContent:  scan.kept,
		Overwritten:  scan.changed,
		BackupDir:    backupDir,
		Adopted:      true,
		NeedsRestart: true,
	}

	if opts.binaryName() == "none" {
		step("done", fmt.Sprintf("目录已接入 %s/%s（%s）；按请求跳过重新编译", source, branch, commit))
		res.Duration = time.Since(start).Round(time.Millisecond).String()
		return res, nil
	}

	bin := filepath.Join(root, opts.binaryName())
	res.BinaryPath = bin
	if _, canBuild := toolchain(ctx, root); !canBuild {
		step("done", fmt.Sprintf("目录已接入 %s/%s（%s）；本机没有 Go 工具链，二进制未重编", source, branch, commit))
		res.Duration = time.Since(start).Round(time.Millisecond).String()
		return res, &Error{
			Reason:  "no_toolchain",
			Message: "目录已接入更新源，但本机没有 Go 工具链，无法重新编译。装好 go 后再点一次更新即可补上二进制。",
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
		return res, &Error{Reason: "build_failed", Message: "编译失败，目录已接入但二进制保持原版本：\n" + err.Error()}
	}
	prev, err := installBinary(newBin, bin)
	if err != nil {
		return res, &Error{Reason: "swap_failed", Message: err.Error()}
	}
	res.BinaryBuilt = true
	res.PrevBinary = prev
	step("done", fmt.Sprintf("目录已接入 %s/%s（%s）并换好二进制", source, branch, commit))
	res.Duration = time.Since(start).Round(time.Millisecond).String()
	return res, nil
}

// adoptPreflight validates the operation's preconditions: a plain directory, a usable
// source address, and (when given) a sane branch name. It does not touch the directory.
func adoptPreflight(ctx context.Context, opts Options) (root, source, branch string, err error) {
	root, err = opts.installRoot()
	if err != nil {
		return "", "", "", err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", "", "", &Error{Reason: "no_git", Message: "本机没有 git，无法把目录接入更新源"}
	}
	if _, err := gitCmd(ctx, root, "rev-parse", "--is-inside-work-tree"); err == nil {
		return "", "", "", &Error{Reason: "already_a_repo", Message: "这个目录已经是 git 工作树：用「一键更新」，不需要接入"}
	}
	if fi, statErr := os.Stat(root); statErr != nil || !fi.IsDir() {
		return "", "", "", &Error{Reason: "bad_root", Message: "安装目录不存在：" + root}
	}
	source = strings.TrimSpace(opts.RemoteURL)
	if source == "" {
		return "", "", "", &Error{Reason: "no_source", Message: "还没有配置更新源地址：先填入仓库地址（配置文件的 update.remote_url）再接入"}
	}
	if !ValidRemoteURL(source) {
		return "", "", "", &Error{Reason: "bad_source", Message: "更新源地址不合法（只允许 https/http/ssh/git/file:// 或本机绝对路径）"}
	}
	branch = strings.TrimSpace(opts.Branch)
	if branch != "" && !ValidName(branch) {
		return "", "", "", &Error{Reason: "bad_branch", Message: fmt.Sprintf("分支名不合法：%q", branch)}
	}
	return root, source, branch, nil
}

// fetchSource fetches one branch from source into repo and reports which branch that
// ended up being (an empty request resolves the repository's default branch), its short
// commit and its subject. Both callers fetch into a repository of their own.
func fetchSource(ctx context.Context, repo, source, branch string) (string, string, string, error) {
	if branch == "" {
		out, err := gitCmd(ctx, repo, "ls-remote", "--symref", source, "HEAD")
		if err != nil {
			return "", "", "", &Error{Reason: "fetch_failed", Message: "读取远端默认分支失败：" + err.Error()}
		}
		branch = parseDefaultBranch(out)
		if branch == "" {
			return "", "", "", &Error{Reason: "no_branch", Message: "无法确定远端默认分支，请在配置里写明分支（update.branch）"}
		}
	}
	if _, err := gitCmd(ctx, repo, "fetch", "--quiet", "--no-tags", "--recurse-submodules=no",
		source, "+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
		return "", "", "", &Error{Reason: "fetch_failed", Message: "拉取失败：" + err.Error()}
	}
	ref := "origin/" + branch
	commit, err := gitCmd(ctx, repo, "rev-parse", "--short", ref)
	if err != nil {
		return "", "", "", &Error{Reason: "no_branch", Message: fmt.Sprintf("远端没有分支 %s", branch)}
	}
	subject, _ := gitCmd(ctx, repo, "log", "-1", "--format=%s", ref)
	return branch, commit, subject, nil
}

// parseDefaultBranch reads "ref: refs/heads/<name>\tHEAD" out of ls-remote --symref output.
func parseDefaultBranch(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ref: refs/heads/") {
			continue
		}
		rest := strings.TrimPrefix(line, "ref: refs/heads/")
		if i := strings.IndexAny(rest, "\t "); i >= 0 {
			rest = rest[:i]
		}
		if ValidName(rest) {
			return rest
		}
	}
	return ""
}

// scanAdoption compares the directory against the tracked files of ref in repo.
func scanAdoption(ctx context.Context, root, repo, ref string) (*adoptionScan, error) {
	out, err := gitRaw(ctx, repo, "ls-tree", "-r", "-z", "--full-tree", ref)
	if err != nil {
		return nil, &Error{Reason: "diff_failed", Message: err.Error()}
	}
	scan := &adoptionScan{target: map[string]string{}}
	for _, rec := range nulList(out) {
		tab := strings.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(rec[:tab])
		if len(fields) < 3 || fields[1] != "blob" {
			continue // submodules and trees are not files this comparison replaces
		}
		p := rec[tab+1:]
		scan.target[p] = fields[2]
		scan.paths = append(scan.paths, p)
	}
	sort.Strings(scan.paths)
	for _, p := range scan.paths {
		abs := filepath.Join(root, filepath.FromSlash(p))
		info, err := os.Lstat(abs)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		hash, err := gitBlobHash(abs)
		if err != nil || hash == scan.target[p] {
			continue
		}
		if Protected(p) {
			scan.kept = append(scan.kept, p)
		} else {
			scan.changed = append(scan.changed, p)
		}
	}
	return scan, nil
}

// plan renders the capped, wire-facing view of a scan.
func (s *adoptionScan) plan(root, source, branch, commit, subject string) *AdoptPlan {
	p := &AdoptPlan{
		Root: root, Source: source, Branch: branch, Commit: commit, Subject: subject,
		Incoming:         len(s.paths),
		OverwrittenTotal: len(s.changed),
		ProtectedTotal:   len(s.kept),
		Overwritten:      append([]string{}, s.changed...),
		Protected:        append([]string{}, s.kept...),
	}
	if len(p.Overwritten) > maxAdoptList {
		p.Overwritten = p.Overwritten[:maxAdoptList]
	}
	if len(p.Protected) > maxAdoptList {
		p.Protected = p.Protected[:maxAdoptList]
	}
	if p.Overwritten == nil {
		p.Overwritten = []string{}
	}
	if p.Protected == nil {
		p.Protected = []string{}
	}
	return p
}

// gitBlobHash computes the object id git would give a file's content. It hashes the bytes
// as they are (no filters), because the comparison is against raw blobs in the tree. sha1 is
// not a security choice here - it is the blob-id algorithm of the repository format.
func gitBlobHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}
