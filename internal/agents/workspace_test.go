package agents

import "testing"

func TestWorkspaceRejectsUnsafeLinksAndKeepsLegacyPath(t *testing.T) {
	for _, u := range []string{"javascript:alert(1)", "//example.com/repo", "https://user:secret@example.com/repo", "https://example.com/repo?token=secret", "https://example.com/repo#secret"} {
		if validateWorkspace(Role{Repository: Repository{Status: "git", URL: u}}) == nil {
			t.Fatalf("accepted unsafe link %q", u)
		}
	}
	legacy := Role{Workspace: "~/legacy"}
	if err := validateWorkspace(legacy); err != nil {
		t.Fatal(err)
	}
	if got := legacy.WorkLocations(); len(got) != 1 || got[0].Path != "~/legacy" || got[0].DeviceID != "" {
		t.Fatal(got)
	}
	legacy.Locations = []Location{{DeviceID: "home", Path: "~/different"}}
	if got := legacy.WorkLocations(); len(got) != 1 || got[0].Path != "~/different" {
		t.Fatal("legacy path overrode new registration", got)
	}
	if err := validateWorkspace(Role{Repository: Repository{Status: "none", URL: "https://example.com/repo"}}); err == nil {
		t.Fatal("none repository accepted URL")
	}
}
