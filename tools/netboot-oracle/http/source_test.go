package http

import "testing"

// TestGithubRawPath pins the cdn.githubraw.com path shape: /<owner>/<repo>/<sha>/<path>.
// This is the authority the Z80 fw_build_path is byte-compared against
// (z80/fw_source_test.go).
func TestGithubRawPath(t *testing.T) {
	got := GithubRawPath("raspberrypi", "firmware",
		"a43df3a002f60c4c2243a416d045eb5937585e8b", "boot/start4.elf")
	want := "/raspberrypi/firmware/a43df3a002f60c4c2243a416d045eb5937585e8b/boot/start4.elf"
	if got != want {
		t.Errorf("GithubRawPath = %q, want %q", got, want)
	}
}

func TestGithubRawHost(t *testing.T) {
	if GithubRawHost != "cdn.githubraw.com" {
		t.Errorf("GithubRawHost = %q, want cdn.githubraw.com", GithubRawHost)
	}
}
