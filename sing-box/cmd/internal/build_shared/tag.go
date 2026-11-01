package build_shared

import (
	"strings"

	"github.com/sagernet/sing-box/common/badversion"
	"github.com/sagernet/sing/common"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/shell"
)

func ReadTag() (string, error) {
	currentTag, err := shell.Exec("git", "describe", "--tags").ReadOutput()
	if err != nil {
		return currentTag, err
	}
	currentTagRev, _ := shell.Exec("git", "describe", "--tags", "--abbrev=0").ReadOutput()
	if currentTagRev == currentTag {
		return strings.TrimPrefix(currentTag, "v"), nil
	}
	if strings.HasPrefix(currentTagRev, "android-") {
		return currentTag, nil
	}
	shortCommit, _ := shell.Exec("git", "rev-parse", "--short", "HEAD").ReadOutput()
	version := badversion.Parse(strings.TrimPrefix(currentTagRev, "v"))
	return version.String() + "-" + shortCommit, nil
}

func ReadTagVersionRev() (badversion.Version, error) {
	currentTagRev := common.Must1(shell.Exec("git", "describe", "--tags", "--abbrev=0").ReadOutput())
	return badversion.Parse(strings.TrimPrefix(currentTagRev, "v")), nil
}

func ReadTagVersion() (badversion.Version, error) {
	currentTag := common.Must1(shell.Exec("git", "describe", "--tags").ReadOutput())
	currentTagRev := common.Must1(shell.Exec("git", "describe", "--tags", "--abbrev=0").ReadOutput())
	version := badversion.Parse(strings.TrimPrefix(currentTagRev, "v"))
	if currentTagRev != currentTag {
		if version.PreReleaseIdentifier == "" {
			version.Patch++
		}
	}
	return version, nil
}

func TestFlightVersion(version badversion.Version) string {
	return F.ToString(version.Major, ".", version.Minor, ".1000")
}
