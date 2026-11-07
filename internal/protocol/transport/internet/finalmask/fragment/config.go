package fragment

import (
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/finalmask"
)

func (c *Config) WrapConnClient(conn net.Conn, dest *net.Destination, dialer *finalmask.Dialer) (net.Conn, error) {
	return NewConnClient(c, conn, false)
}

func (c *Config) WrapConnServer(conn net.Conn) (net.Conn, error) {
	return NewConnServer(c, conn, true)
}
