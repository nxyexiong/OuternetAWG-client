package control

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const MaxFrame = 1024 * 1024

type Request struct {
	Version int    `json:"version"`
	Command string `json:"command"`
	Name    string `json:"name,omitempty"`
	Config  string `json:"config,omitempty"`
}

type Response struct {
	Version int    `json:"version"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Status  Status `json:"status"`
}

func ReadRequest(r io.Reader) (Request, error) {
	var size uint32
	var request Request
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return request, err
	}
	if size == 0 || size > MaxFrame {
		return request, errors.New("invalid IPC frame length")
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return request, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return Request{}, errors.New("invalid IPC request")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Request{}, errors.New("trailing IPC data")
	}
	if request.Version != 1 {
		return Request{}, errors.New("unsupported IPC version; reinstall both UI and service together")
	}
	return request, nil
}

func WriteResponse(w io.Writer, response Response) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(body) > MaxFrame {
		return errors.New("IPC response too large")
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(body))); err != nil {
		return err
	}
	_, err = io.Copy(w, bytes.NewReader(body))
	return err
}
