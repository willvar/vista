package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"vista/internal/appdirs"
	"vista/internal/platform"
	"vista/internal/server"
)

type portHandoffMessage struct {
	Reserved bool   `json:"reserved,omitempty"`
	Armed    bool   `json:"armed,omitempty"`
	Port     int    `json:"port,omitempty"`
	Commit   bool   `json:"commit,omitempty"`
	Error    string `json:"error,omitempty"`
}

func readPortHandoff(reader *bufio.Reader) (portHandoffMessage, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return portHandoffMessage{}, err
	}
	if len(line) > 4096 {
		return portHandoffMessage{}, errors.New("port reservation response exceeds its limit")
	}
	var message portHandoffMessage
	if err := json.Unmarshal(line, &message); err != nil {
		return message, errors.New("invalid port reservation response")
	}
	return message, nil
}
func awaitPortHandoff(reader *bufio.Reader) (portHandoffMessage, error) {
	type result struct {
		message portHandoffMessage
		err     error
	}
	done := make(chan result, 1)
	go func() { message, err := readPortHandoff(reader); done <- result{message, err} }()
	select {
	case result := <-done:
		return result.message, result.err
	case <-time.After(8 * time.Second):
		return portHandoffMessage{}, errors.New("port reservation did not respond")
	}
}

func reservePortRestart(port int) (*server.ReservedPortRestart, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	state, err := dir()
	if err != nil {
		return nil, err
	}
	output, err := os.OpenFile(filepath.Join(state, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer output.Close()
	cmd := exec.Command(exe, "serve-reserved", "--port", fmt.Sprint(port))
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	response, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	cmd.Stderr = output
	platform.Detach(cmd)
	if err := cmd.Start(); err != nil {
		input.Close()
		response.Close()
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	reader := bufio.NewReaderSize(response, 4096)
	var once sync.Once
	abort := func() { once.Do(func() { input.Close(); response.Close(); _ = cmd.Process.Kill() }) }
	fail := func(code string, err error) (*server.ReservedPortRestart, error) {
		abort()
		return nil, &server.PortReservationError{Code: code, Err: err}
	}
	message, err := awaitPortHandoff(reader)
	if err != nil {
		return fail("port_restart_start_failed", fmt.Errorf("could not prepare Vista successor: %w", err))
	}
	if !message.Reserved || message.Port != port {
		return fail("port_unavailable", fmt.Errorf("could not reserve port %d: %s", port, message.Error))
	}
	committed := false
	return &server.ReservedPortRestart{
		Abort: func() {
			if !committed {
				abort()
			}
		},
		Commit: func() error {
			if committed {
				return nil
			}
			if err := json.NewEncoder(input).Encode(portHandoffMessage{Commit: true, Port: port}); err != nil {
				return err
			}
			message, err := awaitPortHandoff(reader)
			if err != nil {
				return err
			}
			if !message.Armed || message.Port != port {
				return errors.New("reserved Vista successor did not accept startup")
			}
			committed = true
			once.Do(func() { input.Close(); response.Close() })
			// Process ownership is independent of this Server. Wait is already running
			// so an early exit does not leak a zombie; shutdown must not kill the child.
			return nil
		},
	}, nil
}

func serveReserved(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("reserved port must be from 1 to 65535")
	}
	root, err := appdirs.Config()
	if err != nil {
		return err
	}
	config, err := server.PreviewRuntimeConfig(root, port)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", config.Address())
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(portHandoffMessage{Port: port, Error: err.Error()})
		return err
	}
	defer listener.Close()
	protocol := os.Stdout
	if err := json.NewEncoder(protocol).Encode(portHandoffMessage{Reserved: true, Port: port}); err != nil {
		return err
	}
	command := make(chan error, 1)
	go func() {
		reader := bufio.NewReaderSize(os.Stdin, 4096)
		message, err := readPortHandoff(reader)
		if err == nil && (!message.Commit || message.Port != port) {
			err = errors.New("invalid reserved startup command")
		}
		command <- err
	}()
	select {
	case err := <-command:
		if err != nil {
			return err
		}
	case <-time.After(30 * time.Second):
		return errors.New("port reservation expired before commit")
	}
	state, err := dir()
	if err != nil {
		return err
	}
	output, err := os.OpenFile(filepath.Join(state, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	if err := json.NewEncoder(protocol).Encode(portHandoffMessage{Armed: true, Port: port}); err != nil {
		return err
	}
	os.Stdout = output
	_ = protocol.Close()
	return server.RunReserved(port, listener, startDesktopControls, startProcessRestart, reservePortRestart)
}
