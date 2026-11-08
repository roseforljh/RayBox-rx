package build_shared

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/roseforljh/RayBox-rx/common/badversion"
	"github.com/sagernet/sing/common/shell"
)

var productVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func readBaseTag() (string, bool, error) {
	root, err := shell.Exec("git", "rev-parse", "--show-toplevel").ReadOutput()
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return "", false, fmt.Errorf("RayBox VERSION unavailable: %w", err)
	}
	version := strings.TrimSpace(string(data))
	if !productVersion.MatchString(version) {
		return "", false, fmt.Errorf("invalid RayBox VERSION: %q", version)
	}
	tag, tagErr := shell.Exec("git", "describe", "--tags", "--exact-match", "--match", "v"+version).ReadOutput()
	return version, tagErr == nil && tag == "v"+version, nil
}

func ReadTag() (string, error) {
	base, _, err := readBaseTag()
	if err != nil {
		return "", err
	}
	commit, err := shell.Exec("git", "rev-parse", "--short=12", "HEAD").ReadOutput()
	if err != nil {
		return "", err
	}
	return base + "-" + commit, nil
}

func ReadTagVersion() (badversion.Version, error) {
	base, _, err := readBaseTag()
	if err != nil {
		return badversion.Version{}, err
	}
	return badversion.Parse(base), nil
}
