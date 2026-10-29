package server

import (
	"context"
	"errors"
	"time"

	"vista/internal/workerproto"
)

func (c *workerResultConsumer) mcpHostEndpoint(result workerproto.Result) error {
	if result.AckID == "" || len(result.AckID) > 64 {
		return errors.New("invalid MCP host endpoint request")
	}
	async := c.async
	if async == nil {
		async = func(fn func()) { go fn() }
	}
	async(func() {
		ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
		defer cancel()
		if c.done != nil {
			go func() {
				select {
				case <-c.done:
					cancel()
				case <-ctx.Done():
				}
			}()
		}
		ack := workerproto.Command{Action: "ack", AckID: result.AckID}
		var err error
		if c.mcpEndpoint == nil {
			err = errors.New("MCP host is unavailable")
		} else {
			ack.MCPHost, err = c.mcpEndpoint(ctx)
		}
		if err != nil {
			ack.Error = err.Error()
		}
		_ = c.send(ack)
	})
	return nil
}
