package update

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The explicit update source (config.yaml update.remote_url) is what lets an installation
// follow a repository that is not one of its own remotes - the official one, a fork, or a
// second-development repository somebody was told about. These tests keep that path honest:
// the address must win over remote names, must not invent a remote, and must not let git's
// ext:: transport (which executes commands) through.

func TestValidRemoteURL(t *testing.T) {
	yes := []string{
		"https://github.com/Sycun/CyberStrikeAI.git",
		"http://example.com/x.git",
		"ssh://git@example.com/x.git",
		"git://example.com/x.git",
		"file:///srv/repos/x.git",
		"/Volumes/code/CyberStrikeAI/开发版-CyberStrikeAI",
	}
	no := []string{
		"",
		"-u./evil",
		"ext::sh -c whoami",
		"ext::touch /tmp/x",
		"a\nb",
		"javascript:alert(1)",
		"ftp://example.com/x",
		" https://github.com/x/y.git",
		strings.Repeat("a", 600),
	}
	for _, s := range yes {
		if !ValidRemoteURL(s) {
			t.Errorf("%q must be accepted as a fetch source", s)
		}
	}
	for _, s := range no {
		if ValidRemoteURL(s) {
			t.Errorf("%q must be rejected before it reaches git", s)
		}
	}
}

func TestCheckAndApplyFromAnExplicitURL(t *testing.T) {
	requireGit(t)
	tr := newTree(t)
	// No remote at all: the source is purely the configured address.
	mustGit(t, tr.install, "remote", "remove", "origin")

	urlOpts := Options{Root: tr.install, RemoteURL: tr.upstream, BinaryName: "none"}

	snap, err := Status(context.Background(), urlOpts)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Installed || snap.Remote != "" || snap.RemoteURL != tr.upstream {
		t.Fatalf("source must resolve to the configured address: installed=%v remote=%q url=%q",
			snap.Installed, snap.Remote, snap.RemoteURL)
	}
	// With no upstream to name a branch, the current branch is the default.
	if snap.Branch != "main" {
		t.Fatalf("branch = %q, want main", snap.Branch)
	}

	tr.upstreamCommit(t, "second: bump service", map[string]string{
		"internal_service.go": "package service\n\nconst Version = \"2\"\n",
	})
	snap, err = Check(context.Background(), urlOpts)
	if err != nil {
		t.Fatal(err)
	}
	if snap.CheckError != "" {
		t.Fatalf("check failed: %s", snap.CheckError)
	}
	if !snap.UpdateAvailable || snap.Behind != 1 {
		t.Fatalf("behind=%d available=%v, want 1/true", snap.Behind, snap.UpdateAvailable)
	}

	res, err := Apply(context.Background(), urlOpts, nil)
	if err != nil {
		t.Fatalf("apply failed: %v\nresult: %+v", err, res)
	}
	if res.Commits != 1 {
		t.Fatalf("res = %+v, want one commit moved", res)
	}
	if got := readFile(t, filepath.Join(tr.install, "internal_service.go")); !strings.Contains(got, "Version = \"2\"") {
		t.Fatalf("code was not updated: %q", got)
	}
	// A URL source parks its ref in a namespace of its own; it must not fabricate a remote
	// the operator never configured.
	if remotes := strings.TrimSpace(mustGit(t, tr.install, "remote")); remotes != "" {
		t.Fatalf("a URL source must not create a remote, got %q", remotes)
	}
}

func TestRemoteURLWinsOverARemoteName(t *testing.T) {
	requireGit(t)
	tr := newTree(t)
	// A second repository with its own newer commit: the address must beat the origin remote.
	other := filepath.Join(filepath.Dir(tr.upstream), "other")
	mustGit(t, filepath.Dir(tr.upstream), "clone", "-q", tr.upstream, other)
	writeFile(t, filepath.Join(other, "internal_service.go"), "package service\n\nconst Version = \"9\"\n")
	mustGit(t, other, "add", ".")
	mustGit(t, other, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "-m", "other moves")

	snap, err := Check(context.Background(), Options{
		Root: tr.install, Remote: "origin", RemoteURL: other, BinaryName: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.CheckError != "" {
		t.Fatalf("check failed: %s", snap.CheckError)
	}
	if snap.RemoteURL != other || snap.Behind != 1 || len(snap.Incoming) != 1 || snap.Incoming[0].Subject != "other moves" {
		t.Fatalf("the address must win over the remote name: %+v", snap)
	}
}

func TestInvalidRemoteURLIsAReadableStateNotAnError(t *testing.T) {
	requireGit(t)
	tr := newTree(t)
	snap, err := Status(context.Background(), Options{Root: tr.install, RemoteURL: "ext::sh -c true"})
	if err != nil {
		t.Fatalf("a bad configured source is a state the page can show, not a fault: %v", err)
	}
	if !strings.Contains(snap.CheckError, "不合法") {
		t.Fatalf("checkError = %q, want the illegality named", snap.CheckError)
	}
}
