package platform

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/nxyexiong/OuternetAWG-client/windows/service/internal/control"
	"golang.org/x/sys/windows"
)

const PipeName = `\\.\pipe\OuternetAWG.Control.v1`

func Listen(owner string) (net.Listener, error) {
	sid, err := windows.StringToSid(owner)
	if err != nil {
		return nil, errors.New("invalid authorized user SID")
	}
	return winio.ListenPipe(PipeName, &winio.PipeConfig{
		SecurityDescriptor: "D:P(D;;GA;;;NU)(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;" + sid.String() + ")",
		InputBufferSize:    65536, OutputBufferSize: 65536,
	})
}

func Serve(ctx context.Context, listener net.Listener, c *control.Controller) error {
	var workers sync.WaitGroup
	defer workers.Wait()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			listener.Close()
		case <-closed:
		}
	}()
	slots := make(chan struct{}, 8)
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			connection.Close()
			slog.Warn("control connection limit reached")
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			defer connection.Close()
			if err := connection.SetDeadline(time.Now().Add(45 * time.Second)); err != nil {
				slog.Error("set control deadline", "error", err)
				return
			}
			request, err := control.ReadRequest(connection)
			if err == nil {
				switch request.Command {
				case "status":
				case "import":
					err = c.Import(request.Name, request.Config)
				case "connect":
					err = c.Connect(ctx)
				case "disconnect":
					err = c.Disconnect()
				case "save-routing":
					if request.Routing == nil {
						err = errors.New("routing settings are required")
					} else {
						err = c.SaveRouting(*request.Routing)
					}
				default:
					err = errors.New("unknown control command")
				}
			}
			response := control.Response{Version: 1, OK: err == nil, Status: c.Status()}
			if err != nil {
				response.Error = err.Error()
			}
			if err := control.WriteResponse(connection, response); err != nil {
				slog.Warn("send control response", "error", err)
			}
		}()
	}
}
