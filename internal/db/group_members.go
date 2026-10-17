package db

import (
	"errors"
	"sort"

	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

var ErrGroupNoChange = errors.New("no membership change")

// ChangeGroupMembers commits membership, task cancellation and the room note
// together. makeNote only formats the already determined change, with no I/O.
func (d *DB) ChangeGroupMembers(groupID, caller string, owner bool, add, remove []string, makeNote func([]string, []string, int) protocol.Message) ([]string, []string, int, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, nil, 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE groups SET status=status WHERE id=? AND status='active' AND (? OR (created_by=? AND EXISTS(SELECT 1 FROM group_members WHERE group_id=groups.id AND agent=?)))`, groupID, owner, caller, caller)
	if err != nil {
		return nil, nil, 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, nil, 0, err
	}
	if n != 1 {
		return nil, nil, 0, ErrGroupChanged
	}
	added, removed := []string{}, []string{}
	cancelled := 0
	removedSet := map[string]bool{}
	for _, agent := range remove {
		removedSet[agent] = true
		if owner && agent == caller {
			return nil, nil, 0, ErrGroupChanged
		}
		res, err := tx.Exec(`DELETE FROM group_members WHERE group_id=? AND agent=?`, groupID, agent)
		if err != nil {
			return nil, nil, 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, nil, 0, err
		}
		if n == 0 {
			continue
		}
		removed = append(removed, agent)
		res, err = tx.Exec(`UPDATE wakeups SET status='cancelled' WHERE group_id=? AND recipient=? AND status IN ('pending','processing')`, groupID, agent)
		if err != nil {
			return nil, nil, 0, err
		}
		n, err = res.RowsAffected()
		if err != nil {
			return nil, nil, 0, err
		}
		cancelled += int(n)
	}
	for _, agent := range add {
		if removedSet[agent] {
			continue
		}
		res, err := tx.Exec(`INSERT OR IGNORE INTO group_members(group_id,agent) VALUES(?,?)`, groupID, agent)
		if err != nil {
			return nil, nil, 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, nil, 0, err
		}
		if n > 0 {
			added = append(added, agent)
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return nil, nil, 0, ErrGroupNoChange
	}
	sort.Strings(added)
	sort.Strings(removed)
	if err := putGroupMessage(tx, makeNote(added, removed, cancelled)); err != nil {
		return nil, nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, 0, err
	}
	return added, removed, cancelled, nil
}
