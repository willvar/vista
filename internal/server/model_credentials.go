package server

import (
	"context"
	"errors"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func (a *App) resolveForRun(ctx context.Context, key string) (modelcfg.Model, string, error) {
	source, selected, err := a.modelsSnapshot().Resolve(key)
	if err != nil {
		return source, selected, err
	}
	return a.credentialCache.Resolve(ctx, source, selected)
}

func (a *App) refreshModelCredentials(ctx context.Context, request modelcfg.Model) (modelcfg.Model, error) {
	source, key, err := a.modelsSnapshot().Resolve(request.Key)
	if err != nil {
		return modelcfg.Model{}, errors.New("model connection is no longer available")
	}
	source.Key = key
	if key != request.Key || request.CredentialRef != provider.CredentialReference(source) {
		return modelcfg.Model{}, errors.New("model connection changed; start a new request")
	}
	model, _, err := a.credentialCache.Resolve(ctx, source, key)
	if err != nil {
		return modelcfg.Model{}, err
	}
	current, _, err := a.modelsSnapshot().Resolve(key)
	current.Key = key
	if err != nil || provider.CredentialReference(current) != request.CredentialRef {
		return modelcfg.Model{}, errors.New("model connection changed; start a new request")
	}
	return model, nil
}

func (c *workerResultConsumer) credentials(result workerproto.Result) error {
	request := result.Credential
	if result.AckID == "" || len(result.AckID) > 64 || request == nil || request.Key == "" || len(request.Key) > 64 || len(request.Ref) != 64 || request.Deadline < 0 {
		return errors.New("invalid model credential request")
	}
	// Exchange outside the result reader: an expired credential must not stall
	// progress or completion events from other tasks in this Worker.
	async := c.async
	if async == nil {
		async = func(fn func()) { go fn() }
	}
	async(func() {
		ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
		defer cancel()
		if request.Deadline > 0 {
			var deadlineCancel context.CancelFunc
			ctx, deadlineCancel = context.WithDeadline(ctx, time.UnixMilli(request.Deadline))
			defer deadlineCancel()
		}
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
		if c.refreshCredentials == nil {
			err = errors.New("credential resolver unavailable")
		} else {
			ack.Model, err = c.refreshCredentials(ctx, modelcfg.Model{Key: request.Key, CredentialRef: request.Ref})
		}
		if err != nil {
			ack.Error = err.Error()
		}
		_ = c.send(ack) // A closed Worker has no remaining credential consumer.
	})
	return nil
}
