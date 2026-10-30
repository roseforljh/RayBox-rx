package libbox

import "github.com/xtls/xray-core/core"

// XrayVersion also lets the Android build reject an unpatched libbox AAR.
func XrayVersion() string { return core.Version() }
