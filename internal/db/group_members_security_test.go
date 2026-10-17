package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/protocol"
)

func TestRemovedCreatorCannotRestoreMembershipFromStaleRequest(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	g := protocol.Group{ID: "g", Item: "test", CreatedBy: "creator", Status: "active", Members: []string{"owner"}, CreatedAt: now()}
	if err := d.CreateGroup(g); err != nil {
		t.Fatal(err)
	}
	// The caller was a creator/member when the HTTP handler began; the owner
	// has now committed removal before that old request reaches its transaction.
	if _, err := d.RemoveGroupMember("g", "creator"); err != nil {
		t.Fatal(err)
	}
	makeNote := func([]string, []string, int) protocol.Message {
		return protocol.Message{ID: "note", GroupID: "g", From: "creator", CreatedAt: time.Now(), Body: "restore"}
	}
	_, _, _, err = d.ChangeGroupMembers("g", "creator", false, []string{"creator"}, nil, makeNote)
	if err == nil {
		t.Error("removed creator restored membership")
	}
	if d.IsGroupMember("g", "creator") || d.CountGroupMessages("g") != 0 {
		t.Error("rejected stale request changed data")
	}
	// Owner retains the explicit ability to add that identity back.
	_, _, _, err = d.ChangeGroupMembers("g", "owner", true, []string{"creator"}, nil, makeNote)
	if err != nil {
		t.Fatalf("owner change rejected: %v", err)
	}
}
