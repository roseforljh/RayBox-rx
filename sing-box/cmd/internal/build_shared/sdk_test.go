package build_shared

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindNDKPreference(t *testing.T) {
	previousSDK, previousNDK := androidSDKPath, androidNDKPath
	t.Cleanup(func() { androidSDKPath, androidNDKPath = previousSDK, previousNDK })
	androidSDKPath = t.TempDir()
	fixed := filepath.Join(androidSDKPath, "ndk", "29.0.14206865")
	explicit := filepath.Join(t.TempDir(), "custom-ndk")
	for _, dir := range []string{fixed, explicit} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "source.properties"), []byte("Pkg.Revision = 29.0.14206865\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ANDROID_NDK_HOME", explicit)
	if !findNDK() || androidNDKPath != explicit {
		t.Fatalf("explicit NDK ignored: %q", androidNDKPath)
	}
	t.Setenv("ANDROID_NDK_HOME", "")
	if !findNDK() || androidNDKPath != fixed {
		t.Fatalf("fixed NDK 29 not selected: %q", androidNDKPath)
	}
}
