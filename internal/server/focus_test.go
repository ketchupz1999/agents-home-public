package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ketchupz1999/agents-home-public/internal/db"
	"github.com/ketchupz1999/agents-home-public/internal/store"
	"os"
	"path/filepath"
)

func TestNowFocusPermissionAndFailures(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"hidden", "visible"} {
		p := filepath.Join(dir, "items", id)
		os.MkdirAll(p, 0700)
		os.WriteFile(filepath.Join(p, "CONTEXT.md"), []byte("# "+id+"\n"), 0600)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := &Server{DB: d, Store: &store.Store{Dir: dir}}
	if _, err = d.SaveFocus([]string{"hidden", "visible"}, 0); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		read []string
		want string
		has  bool
	}{
		{[]string{"items/"}, "", false},
		{[]string{"profile/", "items/visible/"}, "visible", true},
		{[]string{"profile/"}, "暂无可读的关注事项", true},
	} {
		rw := httptest.NewRecorder()
		s.getNow(rw, httptest.NewRequest("GET", "/v1/now", nil), &Key{Read: tc.read})
		if rw.Code != 200 {
			t.Fatal(rw.Code)
		}
		_, section, ok := strings.Cut(rw.Body.String(), "## 当前关注")
		if ok != tc.has || (ok && (!strings.Contains(section, tc.want) || strings.Contains(section, "hidden") || strings.Contains(section, "2."))) {
			t.Fatal(section)
		}
	}
	d.Close()
	rw := httptest.NewRecorder()
	s.getNow(rw, httptest.NewRequest("GET", "/v1/now", nil), &Key{Owner: true})
	if rw.Code != http.StatusInternalServerError || strings.Contains(rw.Body.String(), "## 当前关注") {
		t.Fatal("partial success on DB failure", rw.Code)
	}
}
