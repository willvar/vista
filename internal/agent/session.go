package agent

import (
	"context"
	"errors"
	"sync"

	"vista/internal/tools"
	"vista/internal/workerproto"
)

// Session owns detached work across foreground runs. Closing the browser or
// finishing/canceling a foreground run does not close this supervisor.
type Session struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	runs   map[string]*runScope
	wg     sync.WaitGroup
	tasks  *delegationRegistry
}

func NewSession(ctx context.Context) *Session {
	ctx, cancel := context.WithCancel(ctx)
	tasks := newDelegationRegistry()
	tasks.sessionCtx = ctx
	return &Session{ctx: ctx, cancel: cancel, runs: make(map[string]*runScope), tasks: tasks}
}

func (s *Session) Run(ctx context.Context, command workerproto.Command, emit func(workerproto.Result) error, nextInput func(*workerproto.CompletedReply) (*workerproto.Command, error), jobs *tools.BackgroundManager) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	scope, err := newRunScope(s.tasks.forRun(command.RunID))
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if err := s.ctx.Err(); err != nil {
		s.mu.Unlock()
		return "", err
	}
	if s.runs[command.RunID] != nil {
		s.mu.Unlock()
		return "", errors.New("execution already registered")
	}
	s.runs[command.RunID] = scope
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() {
		go func() {
			scope.delegations.wg.Wait()
			s.mu.Lock()
			delete(s.runs, command.RunID)
			s.mu.Unlock()
			s.wg.Done()
		}()
	}()
	return runWithScope(ctx, command, emit, nil, nextInput, jobs, scope)
}

// TaskContext keeps persistence ACKs tied to the task rather than its former
// foreground parent. Terminal records use the session context so cancellation
// cannot prevent their acknowledgment while the Worker remains available.
func (s *Session) TaskContext(runID, taskID string, fallback context.Context) context.Context {
	s.mu.Lock()
	scope := s.runs[runID]
	s.mu.Unlock()
	if scope != nil {
		scope.delegations.mu.Lock()
		defer scope.delegations.mu.Unlock()
		if task := scope.delegations.tasks[taskID]; task != nil && task.runID == scope.delegations.runID {
			return task.ctx
		}
	}
	return fallback
}

func (s *Session) CancelTask(runID, taskID string) {
	s.mu.Lock()
	scope := s.runs[runID]
	s.mu.Unlock()
	if scope != nil {
		scope.delegations.mu.Lock()
		task := scope.delegations.tasks[taskID]
		owned := task != nil && task.runID == scope.delegations.runID
		scope.delegations.mu.Unlock()
		if owned {
			scope.delegations.cancelTree(taskID)
		}
	}
}

func (s *Session) Close() {
	s.mu.Lock()
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}
