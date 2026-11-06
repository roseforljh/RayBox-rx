package build_shared

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestReadTagInGitRepository(t *testing.T) {
	t.Chdir(t.TempDir())
	git := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--quiet")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "fixture")
	git("tag", "android-"+git("rev-parse", "HEAD"))
	if _, err := ReadTag(); err == nil {
		t.Fatal("android release tag must not be used as the sing-box version")
	}
	if err := os.WriteFile("SOURCE.md", []byte("| sing-box | 官方 v1.14.3 源码包，SHA-256 `abc` | source |\n"), 0600); err != nil {
		t.Fatal(err)
	}
	want := "1.14.3-" + git("rev-parse", "--short=12", "HEAD")
	if actual, err := ReadTag(); err != nil || actual != want {
		t.Fatalf("source baseline: %q %v, want %q", actual, err, want)
	}
	git("tag", "v1.14.3")
	if actual, err := ReadTag(); err != nil || actual != "1.14.3" {
		t.Fatalf("exact tag: %q %v", actual, err)
	}
	if version, err := ReadTagVersion(); err != nil || version.String() != "1.14.3" {
		t.Fatalf("exact version: %v %v", version, err)
	}
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "next")
	git("tag", "server-"+git("rev-parse", "HEAD"))
	want = "1.14.3-" + git("rev-parse", "--short=12", "HEAD")
	if actual, err := ReadTag(); err != nil || actual != want {
		t.Fatalf("later release tag: %q %v, want %q", actual, err, want)
	}
	if version, err := ReadTagVersionRev(); err != nil || version.String() != "1.14.3" {
		t.Fatalf("base version: %v %v", version, err)
	}
	if version, err := ReadTagVersion(); err != nil || version.String() != "1.14.4" {
		t.Fatalf("next version: %v %v", version, err)
	}
}
