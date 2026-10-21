package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"vista/internal/workerproto"
)

type delegatedTask struct {
	id        string
	runID     string
	parentID  string
	join      string
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	result    delegationResult
	completed uint64
	collected bool
}

type delegationResult struct {
	TaskID           string                 `json:"taskId,omitempty"`
	Status           string                 `json:"status"`
	Join             string                 `json:"join,omitempty"`
	Answer           string                 `json:"answer,omitempty"`
	Report           *workerproto.OutputRef `json:"report,omitempty"`
	Error            string                 `json:"error,omitempty"`
	AlreadyCollected bool                   `json:"alreadyCollected,omitempty"`
	PendingTaskIDs   []string               `json:"pendingTaskIds,omitempty"`
}

// Task identities and reports live for the Session. Each run gets a view with
// its own join/cleanup boundary and wait group, including its descendants.
type delegationTasks struct {
	mu       sync.Mutex
	tasks    map[string]*delegatedTask
	changed  chan struct{}
	sequence uint64
}

type delegationRegistry struct {
	*delegationTasks
	runID      string
	sessionCtx context.Context
	wg         sync.WaitGroup
}

func newDelegationRegistry() *delegationRegistry {
	return &delegationRegistry{delegationTasks: &delegationTasks{tasks: make(map[string]*delegatedTask), changed: make(chan struct{})}}
}

func (r *delegationRegistry) forRun(runID string) *delegationRegistry {
	return &delegationRegistry{delegationTasks: r.delegationTasks, runID: runID, sessionCtx: r.sessionCtx}
}

func (r *delegationRegistry) start(ctx context.Context, parentID, callID string, work func(context.Context, string) (string, *workerproto.OutputRef, error)) (delegationResult, error) {
	return r.startWithJoin(ctx, parentID, callID, "required", work)
}

func (r *delegationRegistry) startWithJoin(ctx context.Context, parentID, callID, join string, work func(context.Context, string) (string, *workerproto.OutputRef, error)) (delegationResult, error) {
	return r.startPrepared(ctx, parentID, callID, join, nil, work)
}

func (r *delegationRegistry) startPrepared(ctx context.Context, parentID, callID, join string, prepare func(string) error, work func(context.Context, string) (string, *workerproto.OutputRef, error)) (delegationResult, error) {
	if err := ctx.Err(); err != nil {
		return delegationResult{}, err
	}
	if join == "" {
		join = "required"
	}
	if join != "required" && join != "best_effort" && join != "detached" {
		return delegationResult{}, fmt.Errorf("unsupported join policy %q", join)
	}
	r.mu.Lock()
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return delegationResult{}, err
	}
	id := parentID + "/" + callID
	for suffix := 2; r.tasks[id] != nil; suffix++ {
		id = fmt.Sprintf("%s/%s-%d", parentID, callID, suffix)
	}
	if len(id) > 1024 {
		r.mu.Unlock()
		return delegationResult{}, errors.New("delegated task identity exceeds 1024 bytes")
	}
	parentCtx := ctx
	if join == "detached" && r.sessionCtx != nil {
		parentCtx = r.sessionCtx
	}
	childCtx, cancel := context.WithCancel(parentCtx)
	task := &delegatedTask{id: id, runID: r.runID, parentID: parentID, join: join, ctx: childCtx, cancel: cancel, done: make(chan struct{}), result: delegationResult{TaskID: id, Status: "running", Join: join}}
	r.tasks[id] = task
	r.wg.Add(1)
	r.mu.Unlock()
	finish := func(answer string, report *workerproto.OutputRef, err error) {
		defer r.wg.Done()
		defer cancel()
		result := delegationResult{TaskID: id, Status: "succeeded", Join: join, Answer: answer, Report: report}
		if err != nil {
			result.Status, result.Error = "failed", err.Error()
			if childCtx.Err() != nil {
				result.Status = "canceled"
			}
		}
		r.mu.Lock()
		r.sequence++
		task.result, task.completed = result, r.sequence
		close(task.done)
		close(r.changed)
		r.changed = make(chan struct{})
		r.mu.Unlock()
	}
	if prepare != nil {
		if err := prepare(id); err != nil {
			finish("", nil, err)
			return delegationResult{}, err
		}
	}
	go func() {
		answer, report, err := work(childCtx, id)
		finish(answer, report, err)
	}()
	return delegationResult{TaskID: id, Status: "running", Join: join}, nil
}

func (r *delegationRegistry) ownedTasks(parentID string, ids []string) ([]*delegatedTask, error) {
	if len(ids) == 0 {
		return nil, errors.New("await_agent requires at least one taskId")
	}
	tasks := make([]*delegatedTask, 0, len(ids))
	seen := make(map[string]bool)
	for _, id := range ids {
		task := r.tasks[id]
		if task == nil || task.parentID != parentID || task.runID != r.runID && task.join != "detached" {
			return nil, fmt.Errorf("task %q is not a child of this agent", id)
		}
		if !seen[id] {
			tasks = append(tasks, task)
			seen[id] = true
		}
	}
	return tasks, nil
}

// Await returns the first uncollected completion among the requested tasks.
// Collected tasks are skipped while others remain pending; requesting only
// collected tasks replays the report without starting another model request.
func (r *delegationRegistry) await(ctx context.Context, parentID string, ids []string, timeout *int64) (delegationResult, error) {
	var expired <-chan time.Time
	if timeout != nil {
		if *timeout < 0 || *timeout > int64((1<<63-1)/time.Second) {
			return delegationResult{}, errors.New("timeoutSeconds must be a nonnegative duration")
		}
		timer := time.NewTimer(time.Duration(*timeout) * time.Second)
		defer timer.Stop()
		expired = timer.C
	}
	for {
		if err := ctx.Err(); err != nil {
			return delegationResult{}, err
		}
		r.mu.Lock()
		tasks, err := r.ownedTasks(parentID, ids)
		if err != nil {
			r.mu.Unlock()
			return delegationResult{}, err
		}
		var ready, replay *delegatedTask
		var pending []string
		for _, task := range tasks {
			if task.completed == 0 {
				pending = append(pending, task.id)
			} else if !task.collected && (ready == nil || task.completed < ready.completed) {
				ready = task
			} else if task.collected && (replay == nil || task.completed < replay.completed) {
				replay = task
			}
		}
		if ready == nil && len(pending) == 0 {
			ready = replay
		}
		if ready != nil {
			result := ready.result
			result.AlreadyCollected, ready.collected = ready.collected, true
			r.mu.Unlock()
			return result, nil
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return delegationResult{}, ctx.Err()
		case <-expired:
			return delegationResult{Status: "running", PendingTaskIDs: pending}, nil
		case <-changed:
		}
	}
}

// joinRequired waits for every direct required child to reach a terminal
// state, then marks its report collected and returns a stable snapshot for the
// parent model. It is a runtime join: model calls to await_agent remain an
// optional way to inspect a report, but they do not decide whether the parent
// run may finalize.
func (r *delegationRegistry) joinRequired(ctx context.Context, parentID string) ([]delegationResult, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.mu.Lock()
		var pending bool
		var ready []*delegatedTask
		for _, task := range r.tasks {
			if task.runID != r.runID || task.parentID != parentID || task.join != "required" {
				continue
			}
			if task.completed == 0 {
				pending = true
				continue
			}
			if !task.collected {
				ready = append(ready, task)
			}
		}
		if !pending {
			// The completion sequence is assigned under the same lock, so this
			// gives the model deterministic report order without relying on map
			// iteration order.
			sortDelegatedTasks(ready)
			results := make([]delegationResult, 0, len(ready))
			for _, task := range ready {
				result := task.result
				result.AlreadyCollected = false
				task.collected = true
				results = append(results, result)
			}
			r.mu.Unlock()
			return results, nil
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (r *delegationRegistry) hasUncollectedRequired(parentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, task := range r.tasks {
		if task.runID == r.runID && task.parentID == parentID && task.join == "required" && !task.collected {
			return true
		}
	}
	return false
}

func sortDelegatedTasks(tasks []*delegatedTask) {
	for i := 1; i < len(tasks); i++ {
		for j := i; j > 0 && tasks[j].completed < tasks[j-1].completed; j-- {
			tasks[j], tasks[j-1] = tasks[j-1], tasks[j]
		}
	}
}

func (r *delegationRegistry) cancel(ctx context.Context, parentID, id string) (delegationResult, error) {
	r.mu.Lock()
	_, err := r.ownedTasks(parentID, []string{id})
	r.mu.Unlock()
	if err != nil {
		return delegationResult{}, err
	}
	for _, task := range r.cancelTree(id) {
		select {
		case <-task.done:
		case <-ctx.Done():
			return delegationResult{}, ctx.Err()
		}
	}
	return r.await(ctx, parentID, []string{id}, nil)
}

func (r *delegationRegistry) closeChildren(parentID string) {
	r.mu.Lock()
	var children []*delegatedTask
	for _, task := range r.tasks {
		if task.runID == r.runID && task.parentID == parentID && task.join != "detached" {
			children = append(children, task)
		}
	}
	r.mu.Unlock()
	for _, child := range children {
		child.cancel()
	}
	for _, child := range children {
		<-child.done
	}
}

// Explicit cancellation includes detached descendants. Ordinary parent
// completion uses closeChildren instead, preserving their independent lifetime.
func (r *delegationRegistry) cancelTree(id string) []*delegatedTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	var tasks []*delegatedTask
	for _, task := range r.tasks {
		for ancestor := task; ancestor != nil; ancestor = r.tasks[ancestor.parentID] {
			if ancestor.id == id {
				task.cancel()
				tasks = append(tasks, task)
				break
			}
		}
	}
	return tasks
}

func (r *delegationRegistry) close() {
	r.mu.Lock()
	tasks := make([]*delegatedTask, 0, len(r.tasks))
	for _, task := range r.tasks {
		task.cancel()
		tasks = append(tasks, task)
	}
	r.mu.Unlock()
	for _, task := range tasks {
		<-task.done
	}
	r.wg.Wait()
}
