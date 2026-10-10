package provider

import "testing"

func TestRegistry(t *testing.T) {
	off := false
	r := NewRegistry(map[string]Settings{"web": {Enabled: &off}, "local": {Enabled: &off}}, t.TempDir())
	t.Setenv("PATH", "") // no helper tools
	st := map[string]Info{}
	for _, in := range r.List() {
		st[in.Name] = in
	}
	if st["web"].Status != Disabled || st["local"].Status != Ready || st["torrent"].Status != Ready {
		t.Fatalf("statuses: %+v", st)
	}
	if st["video"].Status != MissingTool || st["gdrive"].Status != Disabled { // drives are opt-in
		t.Fatalf("statuses: %v %v", st["video"].Status, st["gdrive"].Status)
	}
	if err := r.SetEnabled("local", false); err == nil {
		t.Fatal("disabled an always-on provider")
	}
	if err := r.SetEnabled("web", true); err != nil || !r.Enabled("web") {
		t.Fatal("enable without restart failed")
	}
	if r.SetEnabled("nope", true) == nil {
		t.Fatal("unknown provider accepted")
	}
}
