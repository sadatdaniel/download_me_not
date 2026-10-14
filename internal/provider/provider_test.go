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
	if st["video"].Status != MissingTool || st["gdrive"].Status != Disabled { // yt-dlp missing; drives are opt-in
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

func TestSettings(t *testing.T) {
	r := NewRegistry(map[string]Settings{"gdrive": {Settings: map[string]any{"clientSecret": "enc:v1:xxx"}}}, t.TempDir())
	v, err := r.View("gdrive")
	if err != nil || v["clientSecret"] != true || v["token"] != false || v["folder"] != "getaload" {
		t.Fatalf("view %v %v", v, err)
	}
	for _, c := range []struct {
		key string
		v   any
		ok  bool
	}{
		{"clientSecret", "s", true}, {"folder", 3.0, false}, {"token", "x", false}, {"nope", "x", false},
	} {
		if _, err := r.Check("gdrive", c.key, c.v); (err == nil) != c.ok {
			t.Errorf("%s=%v: %v", c.key, c.v, err)
		}
	}
	if _, err := r.Check("torrent", "encryption", "maybe"); err == nil {
		t.Error("enum not enforced")
	}
	if _, err := r.Check("torrent", "maxPeers", 900.0); err == nil {
		t.Error("maximum not enforced")
	}
	if !r.Restart("torrent", "port") || r.Restart("gdrive", "folder") {
		t.Error("restart flags")
	}
}
