package store

import (
	"context"
	"strconv"

	"gorm.io/gorm/clause"
)

type WorkerOwner struct {
	SessionID string
	PID       int
	StartTime uint64
}

type workerOwnerRow struct {
	SessionID string `gorm:"primaryKey"`
	PID       int    `gorm:"column:pid"`
	StartTime string
}

func (workerOwnerRow) TableName() string { return "worker_owners" }

func (s *Store) SaveWorkerOwner(ctx context.Context, owner WorkerOwner) error {
	return s.orm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "session_id"}}, DoUpdates: clause.AssignmentColumns([]string{"pid", "start_time"}),
	}).Create(&workerOwnerRow{SessionID: owner.SessionID, PID: owner.PID, StartTime: strconv.FormatUint(owner.StartTime, 10)}).Error
}

func (s *Store) ClearWorkerOwner(ctx context.Context, owner WorkerOwner) error {
	return s.orm.WithContext(ctx).Where("session_id=? AND pid=? AND start_time=?", owner.SessionID, owner.PID, strconv.FormatUint(owner.StartTime, 10)).Delete(&workerOwnerRow{}).Error
}

func (s *Store) WorkerOwners(ctx context.Context) ([]WorkerOwner, error) {
	rows := []workerOwnerRow{}
	if err := s.orm.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	owners := []WorkerOwner{}
	for _, row := range rows {
		start, err := strconv.ParseUint(row.StartTime, 10, 64)
		if err != nil {
			return nil, err
		}
		owners = append(owners, WorkerOwner{SessionID: row.SessionID, PID: row.PID, StartTime: start})
	}
	return owners, nil
}
