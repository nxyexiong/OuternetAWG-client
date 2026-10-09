package gateway

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Request struct {
	Version int    `json:"version"`
	Action  string `json:"action"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
}

type Response struct {
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
}

func readFrame(r io.Reader, value any) error {
	var size uint32
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return err
	}
	if size == 0 || size > MaxMessageSize {
		return errors.New("control message exceeds the size limit")
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	if err := decodeJSON(data, value); err != nil {
		// Do not return JSON diagnostics containing imported private profile text.
		return errors.New("invalid control message")
	}
	return nil
}

func writeFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > MaxMessageSize {
		return errors.New("control response exceeds the size limit")
	}
	frame := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

func dispatch(ctx context.Context, m *Manager, request Request) Response {
	var err error
	switch {
	case request.Version != 1:
		err = errors.New("unsupported control protocol version")
	case request.Action != "import" && (request.Name != "" || request.Content != ""):
		err = errors.New("profile fields are only valid with import")
	default:
		switch request.Action {
		case "status":
		case "import":
			err = m.Import(request.Name, request.Content)
		case "connect":
			err = m.Connect(ctx, time.Now())
		case "disconnect":
			err = m.Disconnect(ctx)
		default:
			err = errors.New("unknown control action")
		}
	}
	response := Response{Status: m.Status()}
	if err != nil {
		response.Error = err.Error()
	}
	return response
}

func ServeControl(ctx context.Context, path string, m *Manager) error {
	if err := privateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("refusing to replace a non-socket control path")
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			listener.Close()
		case <-stop:
		}
	}()
	var handlers sync.WaitGroup
	defer handlers.Wait()
	slots := make(chan struct{}, 8)
	for {
		client, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			client.Close()
			continue
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() { <-slots }()
			defer client.Close()
			if err := peerRoot(client); err != nil {
				slog.Warn("rejected control connection", "error", err)
				return
			}
			if err := client.SetDeadline(time.Now().Add(45 * time.Second)); err != nil {
				slog.Error("set control deadline", "error", err)
				return
			}
			stop := context.AfterFunc(ctx, func() { client.Close() })
			defer stop()
			var request Request
			if err := readFrame(client, &request); err != nil {
				slog.Warn("invalid control request", "error", err)
				return
			}
			call, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := writeFrame(client, dispatch(call, m, request)); err != nil {
				slog.Warn("send control response", "error", err)
			}
		}()
	}
}

func Call(ctx context.Context, path string, request Request) (Response, error) {
	if err := checkPrivate(filepath.Dir(path), true); err != nil {
		return Response{}, err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, fmt.Errorf("contact gateway service (start it with systemctl): %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	client, ok := conn.(*net.UnixConn)
	if !ok {
		return Response{}, errors.New("unexpected control transport")
	}
	if err = peerRoot(client); err != nil {
		return Response{}, err
	}
	if err = conn.SetDeadline(time.Now().Add(45 * time.Second)); err != nil {
		return Response{}, err
	}
	request.Version = 1
	if err = writeFrame(conn, request); err != nil {
		return Response{}, err
	}
	var response Response
	if err = readFrame(conn, &response); err != nil {
		return Response{}, err
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}
