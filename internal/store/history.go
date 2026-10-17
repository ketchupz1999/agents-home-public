package store

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type HistoryQuery struct {
	SHA           string
	Since, Until  time.Time
	Offset, Limit int
}
type Commit struct {
	SHA, Author, Email, Subject string
	At                          time.Time
}
type HistoryPage struct {
	SHA        string
	Commits    []Commit
	NextOffset int
	HasMore    bool
}

// History 按固定提交与日期窗口分页查询 Git 署名历史。
func (s *Store) History(q HistoryQuery) (HistoryPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := HistoryPage{Commits: []Commit{}}
	var err error
	if q.SHA == "" {
		q.SHA, err = s.publishedSHALocked()
		if err != nil {
			return p, err
		}
	}
	if !commitSHA.MatchString(q.SHA) || q.Offset < 0 || q.Limit < 0 || (!q.Since.IsZero() && !q.Until.IsZero() && q.Since.After(q.Until)) {
		return p, fmt.Errorf("invalid history query")
	}
	published, err := s.publishedSHALocked()
	if err != nil {
		return p, err
	}
	if _, err = s.git("merge-base", "--is-ancestor", q.SHA, published); err != nil {
		return p, fmt.Errorf("history commit is not published")
	}
	p.SHA = q.SHA
	if q.Limit == 0 || q.Limit > 100 {
		q.Limit = 100
	}
	args := []string{"log", q.SHA, "--date-order", "--format=%H%x00%an%x00%ae%x00%cI%x00%s"}
	// 先按固定 SHA 全量取元数据再过滤，避免 Git since 的提前截断漏掉乱序日期。
	raw, err := s.git(args...)
	if err != nil {
		return p, err
	}
	commits := []Commit{}
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		f := strings.SplitN(line, "\x00", 5)
		if len(f) != 5 {
			continue
		}
		at, e := time.Parse(time.RFC3339, f[3])
		if e != nil {
			return p, e
		}
		if (!q.Since.IsZero() && at.Before(q.Since)) || (!q.Until.IsZero() && !at.Before(q.Until)) {
			continue
		}
		commits = append(commits, Commit{f[0], f[1], f[2], f[4], at})
	}
	// 提交时间可能不遵循祖先顺序；先在固定 SHA 的集合上排序，再分页。
	sort.Slice(commits, func(i, j int) bool {
		if !commits[i].At.Equal(commits[j].At) {
			return commits[i].At.After(commits[j].At)
		}
		return commits[i].SHA > commits[j].SHA
	})
	if q.Offset < len(commits) {
		end := q.Offset + q.Limit
		if end < len(commits) {
			p.HasMore = true
		} else {
			end = len(commits)
		}
		p.Commits = commits[q.Offset:end]
	}
	p.NextOffset = q.Offset + len(p.Commits)
	return p, nil
}
