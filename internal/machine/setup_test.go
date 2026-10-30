package machine

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"connectrpc.com/connect"
)

func TestListFolders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	write := func(rel, content string) {
		p := filepath.Join(home, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A repository on feat/x with a branch in a folder and one packed.
	write("code/app/.git/HEAD", "ref: refs/heads/feat/x\n")
	write("code/app/.git/refs/heads/main", "0\n")
	write("code/app/.git/refs/heads/feat/x", "0\n")
	write("code/app/.git/packed-refs", "# pack-refs with: peeled\n0 refs/heads/old\n0 refs/tags/v1\n0 refs/remotes/origin/main\n")
	write("code/app/main.go", "package main\n")
	write("code/Notes/a.md", "")
	write("code/.cache/x", "")
	write("code/readme.txt", "")
	os.Symlink(filepath.Join(home, "code", "Notes"), filepath.Join(home, "code", "linked"))
	os.Symlink(filepath.Join(home, "code", "readme.txt"), filepath.Join(home, "code", "file-link"))
	write("Documents/thesis/.git/HEAD", "ref: refs/heads/main\n")

	res, err := ListFolders("~/code", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "~/code" || res.Repo || res.Files != 2 {
		t.Fatalf("path %q repo %v files %d", res.Path, res.Repo, res.Files)
	}
	var names []string
	for _, f := range res.Folders {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"app", "linked", "Notes"}) {
		t.Fatalf("folders %v: hidden ones left out, a link to a folder kept, case aside", names)
	}
	if app := res.Folders[0]; app.Path != "~/code/app" || !app.Repo || app.Branch != "feat/x" {
		t.Fatalf("app %+v", app)
	}
	if res.Folders[2].Repo {
		t.Fatal("Notes is no repository")
	}

	withHidden, err := ListFolders("~/code", true)
	if err != nil || len(withHidden.Folders) != 4 || withHidden.Folders[0].Name != ".cache" {
		t.Fatalf("hidden: %v %v", withHidden, err)
	}

	// Inside the repository: its branches, local ones only.
	repo, err := ListFolders(filepath.Join(home, "code", "app"), false)
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(filepath.Join(home, "code", "app"))
	if repo.Path != "~/code/app" || repo.FullPath != real || !repo.Repo || repo.Branch != "feat/x" || !slices.Equal(repo.Branches, []string{"feat/x", "main", "old"}) || repo.Files != 1 {
		t.Fatalf("repo %+v", repo)
	}

	top, err := ListFolders("", false)
	if err != nil || top.Path != "~" {
		t.Fatalf("home: %v %v", top, err)
	}
	for _, f := range top.Folders {
		if f.Name == "Documents" {
			// Not looked into on a Mac, where that asks the person.
			if mac := runtime.GOOS == "darwin"; f.Protected != mac || f.Repo {
				t.Fatalf("Documents %+v", f)
			}
		}
	}

	if _, err := ListFolders("~/nowhere", false); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing folder: %v", err)
	}
	if _, err := ListFolders("code", false); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("relative path: %v", err)
	}
}
