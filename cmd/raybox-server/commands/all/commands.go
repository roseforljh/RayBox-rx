package all

import (
	"github.com/roseforljh/RayBox-rx/cmd/raybox-server/commands/all/api"
	"github.com/roseforljh/RayBox-rx/cmd/raybox-server/commands/all/base64"
	"github.com/roseforljh/RayBox-rx/cmd/raybox-server/commands/all/convert"
	// TODO: Restore this import and lua.CmdLua when Lua privileged signature authorization is re-enabled.
	// "github.com/roseforljh/RayBox-rx/cmd/raybox-server/commands/all/lua"
	"github.com/roseforljh/RayBox-rx/cmd/raybox-server/commands/all/tls"
	"github.com/roseforljh/RayBox-rx/cmd/raybox-server/commands/base"
)

func init() {
	base.RootCommand.Commands = append(
		base.RootCommand.Commands,
		api.CmdAPI,
		base64.CmdBase64,
		convert.CmdConvert,
		// lua.CmdLua,
		tls.CmdTLS,
		cmdUUID,
		cmdX25519,
		// cmdEd25519,
		cmdWG,
		cmdMLDSA65,
		cmdMLKEM768,
		cmdVLESSEnc,
	)
}
