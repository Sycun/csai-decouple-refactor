// Package update makes "bring this installation up to the newest commit of the
// repository it was cloned from" one action: fetch, move the working tree, rebuild the
// binary, swap it in.
//
// Why this exists rather than more shell: the shipped upgrade.sh downloads a Release
// tarball of one fixed upstream repository. For anyone running a fork - which is how
// this project is actually deployed here - "upgrade" therefore means "overwrite yourself
// with somebody else's code", and the honest alternative is to pull by hand and remember
// the build command. The platform has to answer "is there anything new" and "take it"
// against the remote *this directory already tracks*, with nobody typing git or go.
//
// Two properties hold the design down:
//
//   - Nothing destructive happens by default. Local source edits, or a branch that has
//     diverged from its remote, stop with the file names and commit counts in the error
//     instead of being reset to match.
//   - Content the operator owns is not ours to overwrite. roles/, skills/, tools/,
//     agents/, bundles/, knowledge_base/, data/, config.yaml and friends are put aside
//     and restored on top of the merge, so an update moves code while the roles and
//     skills created from the console last week survive it. The result names what was
//     kept rather than quietly dropping it on the floor.
package update

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Binary is the default executable name an update replaces.
const Binary = "cyberstrike-ai"

// urlRefNamespace is where a fetch from an explicit address (config.yaml update.remote_url)
// parks its remote-tracking ref. A fixed name no remote can shadow keeps the update reading
// the same shape whether the source is a remote name or a URL.
const urlRefNamespace = "update-source"

// maxIncoming caps how many commits a status page lists; the count is reported
// separately so "37 new commits, showing 50" is impossible.
const maxIncoming = 50

// namePattern guards remote and branch names before they reach git's argv. Git treats a
// leading dash as an option, and an argument like this one comes from the working tree
// rather than from a request - but the same validator is used for both, so there is one
// rule instead of one that is stricter in the case nobody attacks.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@-]*$`)

// urlSchemePattern is the allowlist of fetch schemes. git also accepts "ext::" URLs, which
// run the command written after the colons, so this is an allowlist rather than "whatever
// git accepts"; every other "::" form is rejected outright.
var urlSchemePattern = regexp.MustCompile(`^(https?|ssh|git|file)://`)

// maxRemoteURLLength caps what a config file can put on a git command line.
const maxRemoteURLLength = 512

// ValidRemoteURL reports whether s may be used as a fetch source. Local absolute paths are
// allowed on purpose: they are how this project's own trees point at each other in tests
// and on one machine.
func ValidRemoteURL(s string) bool {
	if s == "" || len(s) > maxRemoteURLLength {
		return false
	}
	if strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	if strings.Contains(s, "::") {
		return false
	}
	if strings.HasPrefix(s, "/") {
		return true
	}
	return urlSchemePattern.MatchString(s)
}

// protectedDirs are install-tree directories whose contents the operator may have
// changed through the console or by hand. An update never overwrites them.
var protectedDirs = []string{
	"roles/", "skills/", "tools/", "agents/", "bundles/", "knowledge_base/",
	"data/", "log/", "logs/", "tmp/", "venv/", ".upgrade-backup/",
}

// protectedFiles are single files with the same rule - most importantly the live
// configuration, which is never in the repository in the first place.
var protectedFiles = []string{"config.yaml", "config.yml", ".env"}

// Protected reports whether a repository-relative path belongs to the operator rather
// than to the product.
func Protected(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, f := range protectedFiles {
		if rel == f {
			return true
		}
	}
	for _, d := range protectedDirs {
		if strings.HasPrefix(rel, d) {
			return true
		}
	}
	return false
}

// Options describes which installation to act on. Root is the only required field;
// Remote, RemoteURL and Branch carry an explicit update source (config.yaml update
// section) for a tree that tracks something other than its own upstream - either a remote
// it already has, by name, or an address to fetch directly. When both are set, the
// address wins.
type Options struct {
	Root      string
	Remote    string
	RemoteURL string
	Branch    string
	// BinaryName overrides the executable swapped after a successful update. Empty
	// means "cyberstrike-ai" in Root; "none" means leave the binary alone.
	BinaryName string
}

func (o Options) binaryName() string {
	if o.BinaryName == "" {
		return Binary
	}
	return o.BinaryName
}

// installRoot resolves the configured directory to an absolute path. A relative config path
// arrives as "." from filepath.Dir, and while git answers it correctly the page would show
// an installation rooted at "." - and an operator reading the refusal cannot tell which
// directory was actually inspected.
func (o Options) installRoot() (string, error) {
	root := strings.TrimSpace(o.Root)
	if root == "" {
		return "", fmt.Errorf("update: install root is required")
	}
	if abs, err := filepath.Abs(root); err == nil {
		return abs, nil
	}
	return root, nil
}

// Change is one locally modified or deleted tracked file.
type Change struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Protected bool   `json:"protected"`
}

// Commit is one line of the incoming history.
type Commit struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
}

// Snapshot is everything a person needs to decide whether to press the button.
type Snapshot struct {
	Root      string `json:"root"`
	Installed bool   `json:"installed"`
	Branch    string `json:"branch"`
	Remote    string `json:"remote"`
	// RemoteURL is set when the update source is an explicit address (config.yaml
	// update.remote_url) rather than a remote this tree already has.
	RemoteURL   string `json:"remoteUrl,omitempty"`
	Remotes     []string
	Commit      string `json:"commit"`
	Subject     string `json:"subject"`
	CommittedAt string `json:"committedAt"`

	Behind int `json:"behind"`
	Ahead  int `json:"ahead"`
	// Diverged means this tree has commits the remote does not. Updating then is a
	// merge decision, not a download, so Apply refuses it.
	Diverged bool `json:"diverged"`

	UpdateAvailable bool     `json:"updateAvailable"`
	RemoteCommit    string   `json:"remoteCommit"`
	RemoteSubject   string   `json:"remoteSubject"`
	Incoming        []Commit `json:"incoming"`
	IncomingTotal   int      `json:"incomingTotal"`

	LocalChanges []Change `json:"localChanges"`
	// BlockingChanges are local edits to product source. Content edits are not
	// blocking: they are preserved across the update.
	BlockingChanges []Change `json:"blockingChanges"`

	GoToolchain string `json:"goToolchain"`
	CanBuild    bool   `json:"canBuild"`

	HasBinary   bool `json:"hasBinary"`
	HasRollback bool `json:"hasRollback"`
	// RollbackTo is the commit the previous update came from, when there is one.
	RollbackTo string `json:"rollbackTo"`

	// CheckError carries a fetch or parse failure to the page instead of turning the
	// whole endpoint into a 500: "offline" and "your token expired" are answers, not
	// server faults.
	CheckError string `json:"checkError,omitempty"`
}

// git runs one git command in the install tree. Everything goes through argv, never
// through a shell, so a repository name with a metacharacter in it is inert.
func gitCmd(ctx context.Context, root string, args ...string) (string, error) {
	out, err := gitRaw(ctx, root, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is the untrimmed form, and the difference is load-bearing: in a `--porcelain -z`
// record the first two bytes are the X and Y columns, and an unstaged modification is
// " M" - trimming the whole blob eats that leading space and shifts every path one byte
// to the left, so "internal/x.go" arrives as "nternal/x.go". Callers that parse the
// machine formats must use this, not gitCmd.
func gitRaw(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return string(out), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// nulList splits a `-z` machine-readable git output into its records.
func nulList(blob string) []string {
	var out []string
	for _, rec := range strings.Split(blob, "\x00") {
		if rec != "" {
			out = append(out, rec)
		}
	}
	return out
}

// ValidName reports whether s can safely reach git's argv as a remote or branch name.
func ValidName(s string) bool { return namePattern.MatchString(s) }

// Status reads the install tree without touching the network.
func Status(ctx context.Context, opts Options) (*Snapshot, error) {
	root, err := opts.installRoot()
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{
		Root:            root,
		Incoming:        []Commit{},
		LocalChanges:    []Change{},
		BlockingChanges: []Change{},
	}

	if url := strings.TrimSpace(opts.RemoteURL); url != "" {
		if !ValidRemoteURL(url) {
			snap.CheckError = "配置的更新源地址不合法（只允许 https/http/ssh/git/file:// 或本机绝对路径）"
			return snap, nil
		}
		snap.RemoteURL = url
	}

	if _, err := gitCmd(ctx, root, "rev-parse", "--is-inside-work-tree"); err != nil {
		// Not a git tree: the page still has to say something useful, so this is a
		// state rather than an error.
		snap.Installed = false
		snap.CheckError = "这个目录不是 git 工作树：可以先配置更新源再接入（配置文件 update 段），或按发布包方式更新"
		snap.GoToolchain, snap.CanBuild = toolchain(ctx, root)
		snap.HasBinary = fileExists(filepath.Join(root, opts.binaryName()))
		return snap, nil
	}
	snap.Installed = true

	for _, q := range []struct {
		args []string
		dest *string
	}{
		{[]string{"rev-parse", "--abbrev-ref", "HEAD"}, &snap.Branch},
		{[]string{"rev-parse", "--short", "HEAD"}, &snap.Commit},
		{[]string{"log", "-1", "--format=%s"}, &snap.Subject},
		{[]string{"log", "-1", "--format=%ci"}, &snap.CommittedAt},
	} {
		if v, err := gitCmd(ctx, root, q.args...); err == nil {
			*q.dest = v
		}
	}
	if opts.Branch != "" {
		snap.Branch = opts.Branch
	}

	remotes, err := gitCmd(ctx, root, "remote")
	if err == nil {
		for _, r := range strings.Fields(remotes) {
			if ValidName(r) {
				snap.Remotes = append(snap.Remotes, r)
			}
		}
	}
	snap.Remote = pickRemote(snap.Remotes, snap.Branch, opts.Remote)

	if upstream, err := gitCmd(ctx, root, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil && ValidName(upstream) {
		if i := strings.Index(upstream, "/"); i > 0 {
			if snap.Remote == "" {
				snap.Remote = upstream[:i]
			}
			if opts.Branch == "" && upstream[i+1:] != "HEAD" {
				snap.Branch = upstream[i+1:]
			}
		}
	}

	snap.LocalChanges, snap.BlockingChanges = localChanges(ctx, root)
	snap.GoToolchain, snap.CanBuild = toolchain(ctx, root)
	bin := filepath.Join(root, opts.binaryName())
	snap.HasBinary = fileExists(bin)
	if st, ok := readState(root); ok {
		snap.HasRollback = fileExists(bin+".prev") && st.UpdatedCommit != ""
		snap.RollbackTo = st.PreviousCommit
	}
	return snap, nil
}

// pickRemote chooses which remote to pull from: an explicit option, then the usual
// suspects in the order that matches how this project is actually cloned (a fork named
// for its owner tracking the owner's work, with the upstream kept for reference).
func pickRemote(remotes []string, branch, optsRemote string) string {
	if optsRemote != "" && ValidName(optsRemote) {
		return optsRemote
	}
	ranked := []string{"mine", "origin", "upstream"}
	known := map[string]bool{}
	for _, r := range remotes {
		known[r] = true
	}
	for _, r := range ranked {
		if known[r] {
			return r
		}
	}
	if len(remotes) > 0 {
		return remotes[0]
	}
	_ = branch
	return ""
}

// fetchTarget is what git is pointed at: the configured address, or the chosen remote.
func (s *Snapshot) fetchTarget() string {
	if s.RemoteURL != "" {
		return s.RemoteURL
	}
	return s.Remote
}

// fetchNamespace is where the fetched branch is parked under refs/remotes/.
func (s *Snapshot) fetchNamespace() string {
	if s.RemoteURL != "" {
		return urlRefNamespace
	}
	return s.Remote
}

// sourceRef is the remote-tracking ref a status or an update compares against.
func (s *Snapshot) sourceRef() string { return s.fetchNamespace() + "/" + s.Branch }

// sourceLabel is the human-facing name of where an update would come from.
func (s *Snapshot) sourceLabel() string {
	if s.RemoteURL != "" {
		return s.RemoteURL
	}
	return s.Remote
}

// localChanges lists modified and deleted tracked files, and splits them by who owns
// the path.
func localChanges(ctx context.Context, root string) (all, blocking []Change) {
	out, err := gitRaw(ctx, root, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=no")
	if err != nil {
		return all, blocking
	}
	for _, entry := range nulList(out) {
		if len(entry) < 4 {
			continue
		}
		status, path := entry[:2], filepath.ToSlash(entry[3:])
		if status == "??" || strings.HasPrefix(status, "!") {
			continue
		}
		c := Change{Path: path, Status: changeVerb(status), Protected: Protected(path)}
		all = append(all, c)
		if !c.Protected {
			blocking = append(blocking, c)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })
	sort.Slice(blocking, func(i, j int) bool { return blocking[i].Path < blocking[j].Path })
	return all, blocking
}

func changeVerb(status string) string {
	switch {
	case strings.Contains(status, "D"):
		return "deleted"
	case strings.Contains(status, "A"):
		return "added"
	default:
		return "modified"
	}
}

// Check fetches the chosen remote and reports what would come in. The fetch is the only
// network operation in this package, and a failure here is a readable answer rather than
// a fault: the page has to distinguish "up to date" from "could not look".
func Check(ctx context.Context, opts Options) (*Snapshot, error) {
	snap, err := Status(ctx, opts)
	if err != nil {
		return nil, err
	}
	if !snap.Installed {
		return snap, nil
	}
	root := snap.Root
	if snap.Remote == "" && snap.RemoteURL == "" {
		snap.CheckError = "该安装没有配置任何 git remote，也没有配置更新源地址，无法检查更新"
		return snap, nil
	}
	if snap.Branch == "" || snap.Branch == "HEAD" || !ValidName(snap.Branch) {
		snap.CheckError = "当前不在可自动更新的分支上（游离 HEAD 或分支名不合规范），无法确定要跟踪哪个分支"
		return snap, nil
	}
	// --recurse-submodules=no and --prune keep the fetch to this repository's own
	// history. A detached fetch of one branch means a remote with many refs cannot
	// turn the update check into a full clone.
	if _, err := gitCmd(ctx, root, "fetch", "--quiet", "--no-tags", "--prune", "--recurse-submodules=no",
		snap.fetchTarget(), "+refs/heads/"+snap.Branch+":refs/remotes/"+snap.fetchNamespace()+"/"+snap.Branch); err != nil {
		snap.CheckError = err.Error()
		return snap, nil
	}

	ref := snap.sourceRef()
	if v, err := gitCmd(ctx, root, "rev-parse", "--short", ref); err == nil {
		snap.RemoteCommit = v
	} else {
		snap.CheckError = err.Error()
		return snap, nil
	}
	snap.RemoteSubject, _ = gitCmd(ctx, root, "log", "-1", "--format=%s", ref)

	if counts, err := gitCmd(ctx, root, "rev-list", "--left-right", "--count", "HEAD..."+ref); err == nil {
		var ahead, behind int
		if _, scanErr := fmt.Sscanf(counts, "%d %d", &ahead, &behind); scanErr == nil {
			snap.Ahead, snap.Behind = ahead, behind
			snap.Diverged = ahead > 0 && behind > 0
		}
	}
	snap.UpdateAvailable = snap.Behind > 0
	snap.IncomingTotal = snap.Behind

	logs, err := gitCmd(ctx, root, "log", "--format=%h\x1f%s", "-n", fmt.Sprint(maxIncoming), "HEAD.."+ref)
	if err == nil {
		for _, line := range strings.Split(logs, "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\x1f", 2)
			c := Commit{Commit: parts[0]}
			if len(parts) > 1 {
				c.Subject = parts[1]
			}
			snap.Incoming = append(snap.Incoming, c)
		}
	}
	return snap, nil
}
