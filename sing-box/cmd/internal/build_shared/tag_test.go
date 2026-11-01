package build_shared

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestReadTagInGitRepository(t *testing.T) {
	for _, name := range []string{"version", "unprefixed-version", "android"} {
		t.Run(name, func(t *testing.T) {
			// Tag fixtures stay isolated from the source repository.
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
			if err := os.WriteFile("source", []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", "source")
			git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "fixture")
			tag := "v1.14.3"
			expected := "1.14.3"
			if name == "unprefixed-version" {
				tag = expected
			}
			if name == "android" {
				tag = "android-" + git("rev-parse", "HEAD")
				expected = tag
			}
			git("tag", tag)
			actual, err := ReadTag()
			if err != nil || actual != expected {
				t.Fatalf("exact tag: got %q, %v; want %q", actual, err, expected)
			}
			if name != "android" {
				version, err := ReadTagVersionRev()
				if err != nil || version.String() != "1.14.3" {
					t.Fatalf("release version: %v, %v", version, err)
				}
				version, err = ReadTagVersion()
				if err != nil || version.String() != "1.14.3" {
					t.Fatalf("exact version: %v, %v", version, err)
				}
			}
			git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "next")
			expected += "-" + git("rev-parse", "--short", "HEAD")
			if name == "android" {
				expected = git("describe", "--tags")
			}
			actual, err = ReadTag()
			if err != nil || actual != expected {
				t.Fatalf("after tag: got %q, %v; want %q", actual, err, expected)
			}
			if name != "android" {
				version, err := ReadTagVersionRev()
				if err != nil || version.String() != "1.14.3" {
					t.Fatalf("base version: %v, %v", version, err)
				}
				version, err = ReadTagVersion()
				if err != nil || version.String() != "1.14.4" {
					t.Fatalf("next version: %v, %v", version, err)
				}
			}
		})
	}
}
