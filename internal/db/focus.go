package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// Focus is the owner's ordered working set, not an authorization to act.
// Revision zero means no choice has been saved, including an empty choice.
type Focus struct {
	Items    []string
	Revision int64
}

var ErrFocusConflict = errors.New("focus changed")
var focusID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func ValidateFocus(items []string) error {
	if len(items) > 2 {
		return fmt.Errorf("最多选择两个事项")
	}
	seen := map[string]bool{}
	for _, id := range items {
		if !focusID.MatchString(id) || seen[id] {
			return fmt.Errorf("事项 ID 无效或重复")
		}
		seen[id] = true
	}
	return nil
}

func (d *DB) GetFocus() (Focus, error) {
	var f Focus
	var raw string
	err := d.QueryRow(`SELECT items,revision FROM focus WHERE id=1`).Scan(&raw, &f.Revision)
	if err == sql.ErrNoRows {
		return Focus{Items: []string{}}, nil
	}
	if err != nil {
		return f, err
	}
	if err = json.Unmarshal([]byte(raw), &f.Items); err != nil {
		return f, err
	}
	if f.Items == nil || f.Revision < 1 {
		return f, fmt.Errorf("invalid stored focus")
	}
	return f, ValidateFocus(f.Items)
}

// SaveFocus uses one conditional SQL statement. Different concurrent choices
// never overwrite each other; retrying an already saved choice is harmless.
func (d *DB) SaveFocus(items []string, base int64) (Focus, error) {
	if err := ValidateFocus(items); err != nil {
		return Focus{}, err
	}
	if base < 0 {
		return Focus{}, fmt.Errorf("invalid focus revision")
	}
	current, err := d.GetFocus()
	if err != nil {
		return current, err
	}
	if current.Revision > 0 && slices.Equal(current.Items, items) {
		return current, nil
	}
	if items == nil {
		items = []string{}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return Focus{}, err
	}
	var result sql.Result
	if base == 0 {
		result, err = d.Exec(`INSERT INTO focus(id,items,revision) VALUES(1,?,1) ON CONFLICT(id) DO NOTHING`, string(raw))
	} else {
		result, err = d.Exec(`UPDATE focus SET items=?,revision=revision+1 WHERE id=1 AND revision=? AND items<>?`, string(raw), base, string(raw))
	}
	if err != nil {
		return Focus{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Focus{}, err
	}
	if changed == 1 {
		return Focus{Items: slices.Clone(items), Revision: base + 1}, nil
	}
	current, err = d.GetFocus()
	if err != nil {
		return current, err
	}
	if current.Revision > 0 && slices.Equal(current.Items, items) {
		return current, nil
	}
	return current, ErrFocusConflict
}
