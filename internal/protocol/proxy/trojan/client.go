package trojan

import (
	"context"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/buf"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/errors"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/protocol"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/retry"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/session"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/signal"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/task"
	core "github.com/roseforljh/RayBox-rx/internal/protocol/core"
	"github.com/roseforljh/RayBox-rx/internal/protocol/features/policy"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/stat"
)

// Client is a inbound handler for trojan protocol
type Client struct {
	server        *protocol.ServerSpec
	policyManager policy.Manager
}

// NewClient create a new trojan client.
func NewClient(ctx context.Context, config *ClientConfig) (*Client, error) {
	if config.Server == nil {
		return nil, errors.New(`no target server found`)
	}
	server, err := protocol.NewServerSpecFromPB(config.Server)
	if err != nil {
		return nil, errors.New("failed to get server spec").Base(err)
	}

	client := &Client{
		server:        server,
		policyManager: core.PolicyManager(ctx),
	}
	return client, nil
}

// Process implements OutboundHandler.Process().
func (c *Client) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	outbounds := session.OutboundsFromContext(ctx)
	ob := outbounds[len(outbounds)-1]
	if !ob.Target.IsValid() {
		return errors.New("target not specified")
	}
	ob.Name = "trojan"
	ob.CanSpliceCopy = 3
	destination := ob.Target
	network := destination.Network

	server := c.server
	var conn stat.Connection

	err := retry.ExponentialBackoff(5, 100).On(func() error {
		rawConn, err := dialer.Dial(ctx, server.Destination)
		if err != nil {
			return err
		}

		conn = rawConn
		return nil
	})
	if err != nil {
		return errors.New("failed to find an available destination").Base(err)
	}
	errors.LogInfo(ctx, "tunneling request to ", destination, " via ", server.Destination.NetAddr())

	defer conn.Close()

	user := server.User
	account, ok := user.Account.(*MemoryAccount)
	if !ok {
		return errors.New("user account is not valid")
	}

	var newCtx context.Context
	var newCancel context.CancelFunc
	if session.TimeoutOnlyFromContext(ctx) {
		newCtx, newCancel = context.WithCancel(context.Background())
	}

	sessionPolicy := c.policyManager.ForLevel(user.Level)
	ctx, cancel := context.WithCancel(ctx)
	timer := signal.CancelAfterInactivity(ctx, func() {
		cancel()
		if newCancel != nil {
			newCancel()
		}
	}, sessionPolicy.Timeouts.ConnectionIdle)

	postRequest := func() error {
		defer timer.SetTimeout(sessionPolicy.Timeouts.DownlinkOnly)

		bufferWriter := buf.NewBufferedWriter(buf.NewWriter(conn))

		connWriter := &ConnWriter{
			Writer:  bufferWriter,
			Target:  destination,
			Account: account,
		}

		var bodyWriter buf.Writer
		if destination.Network == net.Network_UDP {
			bodyWriter = &PacketWriter{Writer: connWriter, Target: destination}
		} else {
			bodyWriter = connWriter
		}

		// write some request payload to buffer
		if err = buf.CopyOnceTimeout(link.Reader, bodyWriter, time.Millisecond*100); err != nil && err != buf.ErrNotTimeoutReader && err != buf.ErrReadTimeout {
			return errors.New("failed to write A request payload").Base(err)
		}

		// Flush; bufferWriter.WriteMultiBuffer now is bufferWriter.writer.WriteMultiBuffer
		if err = bufferWriter.SetBuffered(false); err != nil {
			return errors.New("failed to flush payload").Base(err)
		}

		// Send header if not sent yet
		if _, err = connWriter.Write([]byte{}); err != nil {
			return err
		}

		if err = buf.Copy(link.Reader, bodyWriter, buf.UpdateActivity(timer)); err != nil {
			return errors.New("failed to transfer request payload").Base(err)
		}

		return nil
	}

	getResponse := func() error {
		defer timer.SetTimeout(sessionPolicy.Timeouts.UplinkOnly)

		var reader buf.Reader
		if network == net.Network_UDP {
			reader = &PacketReader{
				Reader: conn,
			}
		} else {
			reader = buf.NewReader(conn)
		}
		return buf.Copy(reader, link.Writer, buf.UpdateActivity(timer))
	}

	if newCtx != nil {
		ctx = newCtx
	}

	responseDoneAndCloseWriter := task.OnSuccess(getResponse, task.Close(link.Writer))
	if err := task.Run(ctx, postRequest, responseDoneAndCloseWriter); err != nil {
		return errors.New("connection ends").Base(err)
	}

	return nil
}

func init() {
	common.Must(common.RegisterConfig((*ClientConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return NewClient(ctx, config.(*ClientConfig))
	}))
}
