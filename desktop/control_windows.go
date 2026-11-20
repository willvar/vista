package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"vista/internal/instance"
)

func lifecycle(base *url.URL, dir, expectedHash, action string) error {
	if action != "restart" && action != "stop" {
		return errors.New("invalid Vista lifecycle operation")
	}
	status, err := readStatus(base)
	if err != nil {
		return err
	}
	if status.BinaryHash != expectedHash || expectedHash == "" || !registered(dir, base, status.PID) {
		return errors.New("the connected Vista service does not match the desktop's local instance")
	}
	// Use the existing service API so the running companion retains its listener
	// and environment. Never replay a lifecycle POST after an uncertain reply.
	req, err := http.NewRequest(http.MethodPost, base.JoinPath("api", action).String(), strings.NewReader(`{"confirm":"`+action+`"}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := serviceHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("could not confirm Vista %s: %w", action, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("Vista rejected %s: %s", action, response.Status)
	}
	if action == "restart" {
		var accepted struct {
			Accepted   bool   `json:"accepted"`
			InstanceID string `json:"instanceId"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&accepted) != nil || !accepted.Accepted || status.InstanceID != accepted.InstanceID {
			return errors.New("could not confirm that the connected Vista accepted the restart")
		}
	}
	response.Body.Close()
	// Bounded readiness only during a deliberate lifecycle action, not background polling.
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if action == "stop" {
			if !instance.IsAlive(status.PID) {
				return nil
			}
		} else if next, err := readStatus(base); err == nil && next.InstanceID != status.InstanceID && next.BinaryHash == expectedHash && registered(dir, base, next.PID) {
			return nil
		}
	}
	return fmt.Errorf("could not confirm that Vista finished %s", action)
}
