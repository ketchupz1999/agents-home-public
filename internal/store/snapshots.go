package store

import (
	"fmt"
	"regexp"
	"strings"
)

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// PublishedSHA 固定最近已知的已发布提交，不读取临时工作树。
func (s *Store) PublishedSHA() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishedSHALocked()
}
func (s *Store) publishedSHALocked() (string, error) {
	ref := "refs/remotes/origin/main"
	if !s.Remote {
		ref = "refs/heads/main"
	}
	raw, err := s.git("rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(raw)
	if !commitSHA.MatchString(sha) {
		return "", fmt.Errorf("invalid published commit")
	}
	return sha, nil
}

// ReadSnapshot 读取固定提交中的文件；提交不可变，因此后续读取不会混入审批变更。
func (s *Store) ReadSnapshot(sha, rel string) ([]byte, error) {
	if !commitSHA.MatchString(sha) {
		return nil, fmt.Errorf("invalid snapshot")
	}
	clean, err := Clean(rel)
	if err != nil || clean != rel {
		return nil, fmt.Errorf("invalid snapshot path")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.git("show", sha+":"+rel)
	if err != nil {
		return nil, err
	}
	return []byte(raw), nil
}

// ReadWithSHA 在同一锁内读取实际返回字节并计算指纹。
func (s *Store) ReadWithSHA(rel string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.Read(rel)
	if err != nil {
		return nil, "", err
	}
	if b == nil {
		b = []byte{}
	}
	return b, BlobSHA(b), nil
}
