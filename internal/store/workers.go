package store

import (
	"context"
	"strconv"
)

type WorkerOwner struct {
	SessionID string
	PID       int
	StartTime uint64
}

func (s *Store) SaveWorkerOwner(ctx context.Context, owner WorkerOwner) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO worker_owners(session_id,pid,start_time) VALUES(?,?,?) ON CONFLICT(session_id) DO UPDATE SET pid=excluded.pid,start_time=excluded.start_time`, owner.SessionID, owner.PID, strconv.FormatUint(owner.StartTime, 10))
	return err
}

func (s *Store) ClearWorkerOwner(ctx context.Context, owner WorkerOwner) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM worker_owners WHERE session_id=? AND pid=? AND start_time=?`, owner.SessionID, owner.PID, strconv.FormatUint(owner.StartTime, 10))
	return err
}

func (s *Store) WorkerOwners(ctx context.Context) ([]WorkerOwner, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT session_id,pid,start_time FROM worker_owners")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := []WorkerOwner{}
	for rows.Next() {
		var owner WorkerOwner
		var start string
		if err := rows.Scan(&owner.SessionID, &owner.PID, &start); err != nil {
			return nil, err
		}
		owner.StartTime, err = strconv.ParseUint(start, 10, 64)
		if err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}
