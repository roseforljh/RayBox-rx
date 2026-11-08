package build_shared

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestReadTagUsesOnlyRayBoxVersion(t *testing.T) {
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
	git("tag", "v0.0.1")
	git("tag", "android-"+git("rev-parse", "HEAD"))
	if _, err := ReadTag(); err == nil {
		t.Fatal("upstream tags must not replace the RayBox VERSION file")
	}
	for _, value := range []string{"", "v1.0.0", "1.0", "1.0.0\n2.0.0"} {
		if err := os.WriteFile("VERSION", []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadTag(); err == nil {
			t.Fatalf("invalid VERSION accepted: %q", value)
		}
	}
	if err := os.WriteFile("VERSION", []byte("0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "0.0.1"
	for _, tag := range []string{"server-" + git("rev-parse", "HEAD")} {
		git("tag", tag)
		if actual, err := ReadTag(); err != nil || actual != want {
			t.Fatalf("product version: %q %v, want %q", actual, err, want)
		}
	}
	if version, err := ReadTagVersion(); err != nil || version.String() != "0.0.1" {
		t.Fatalf("numeric product version: %v %v", version, err)
	}
}
