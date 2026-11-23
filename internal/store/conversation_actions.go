package store

import (
	"context"
	"errors"
	"slices"

	"gorm.io/gorm"
)

var ErrConversationChanged = errors.New("conversation membership changed")

// ConversationMembers returns the whole conversation, including closed branches,
// in lock order. Resolve membership from the supplied session, not UI state.
func (s *Store) ConversationMembers(ctx context.Context, id string) ([]string, error) {
	var ids []string
	err := s.orm.WithContext(ctx).Raw(`WITH target AS (
 SELECT COALESCE(b.root_id,s.id) AS id FROM sessions s
 LEFT JOIN conversation_branches b ON b.branch_id=s.id WHERE s.id=?
 ) SELECT id FROM target UNION SELECT b.branch_id FROM conversation_branches b
 JOIN target ON target.id=b.root_id ORDER BY id`, id).Scan(&ids).Error
	if err == nil && len(ids) == 0 {
		err = ErrNotFound
	}
	return ids, err
}

// Recheck the exact set locked by the caller on the same transaction as the
// change. A fork created before those locks were acquired must not be missed.
func (s *Store) changeConversation(ctx context.Context, id string, locked []string, apply func(*Store) error) error {
	return s.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		view := &Store{orm: tx, library: s.library}
		ids, err := view.ConversationMembers(ctx, id)
		if err != nil {
			return err
		}
		if !slices.Equal(ids, locked) {
			return ErrConversationChanged
		}
		return apply(view)
	})
}

func (s *Store) CloseConversation(ctx context.Context, id string, locked []string) error {
	return s.changeConversation(ctx, id, locked, func(view *Store) error {
		for _, member := range locked {
			if err := setSessionOpen(view.orm, member, false); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteConversation removes shared history only together with every dependent
// branch. Output paths are returned after commit for normal ownership cleanup.
func (s *Store) DeleteConversation(ctx context.Context, id string, locked []string) (map[string][]string, error) {
	outputs := make(map[string][]string, len(locked))
	err := s.changeConversation(ctx, id, locked, func(view *Store) error {
		// Gather every owner's paths before cascading any session records.
		for _, member := range locked {
			paths, err := view.ArtifactPaths(ctx, member)
			if err != nil {
				return err
			}
			outputs[member] = paths
		}
		for _, member := range locked {
			if err := sessionRowsAffected(view.orm.Where("id=?", member).Delete(&sessionRow{})); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return outputs, nil
}
