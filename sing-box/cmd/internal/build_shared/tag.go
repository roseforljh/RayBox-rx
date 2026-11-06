package build_shared

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sagernet/sing-box/common/badversion"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/shell"
)

var stableVersionTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var sourceBaseline = regexp.MustCompile(`(?m)^\| sing-box \| 官方 (v[0-9]+\.[0-9]+\.[0-9]+) 源码包，SHA-256 `)

func readBaseTag() (string, bool, error) {
	tag, err := shell.Exec("git", "describe", "--tags", "--match", "v[0-9]*", "--abbrev=0").ReadOutput()
	if err == nil {
		if !stableVersionTag.MatchString(tag) {
			return "", false, fmt.Errorf("invalid sing-box version tag: %s", tag)
		}
		exact, exactErr := shell.Exec("git", "describe", "--tags", "--exact-match", "--match", tag).ReadOutput()
		return strings.TrimPrefix(tag, "v"), exactErr == nil && exact == tag, nil
	}
	root, err := shell.Exec("git", "rev-parse", "--show-toplevel").ReadOutput()
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(filepath.Join(root, "SOURCE.md"))
	if err != nil {
		return "", false, fmt.Errorf("sing-box baseline unavailable: %w", err)
	}
	matches := sourceBaseline.FindSubmatch(data)
	if len(matches) != 2 || !stableVersionTag.Match(matches[1]) {
		return "", false, fmt.Errorf("sing-box baseline missing or invalid in SOURCE.md")
	}
	return strings.TrimPrefix(string(matches[1]), "v"), false, nil
}

func ReadTag() (string, error) {
	base, exact, err := readBaseTag()
	if err != nil || exact {
		return base, err
	}
	commit, err := shell.Exec("git", "rev-parse", "--short=12", "HEAD").ReadOutput()
	if err != nil {
		return "", err
	}
	return base + "-" + commit, nil
}

func ReadTagVersionRev() (badversion.Version, error) {
	base, _, err := readBaseTag()
	if err != nil {
		return badversion.Version{}, err
	}
	return badversion.Parse(base), nil
}

func ReadTagVersion() (badversion.Version, error) {
	base, exact, err := readBaseTag()
	if err != nil {
		return badversion.Version{}, err
	}
	version := badversion.Parse(base)
	if !exact && version.PreReleaseIdentifier == "" {
		version.Patch++
	}
	return version, nil
}

func TestFlightVersion(version badversion.Version) string {
	return F.ToString(version.Major, ".", version.Minor, ".1000")
}
