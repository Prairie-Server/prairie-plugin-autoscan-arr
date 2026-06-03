package arr

import (
	"testing"

	"github.com/Silo-Server/silo-plugin-autoscan-arr/internal/config"
)

func TestApplyRewrites(t *testing.T) {
	rw := []config.Rewrite{{From: "/data/media", To: "/mnt/media"}}
	cases := []struct{ in, want string }{
		{"/data/media/Movies/Dune/Dune.mkv", "/mnt/media/Movies/Dune/Dune.mkv"},
		{"/other/path/file.mkv", "/other/path/file.mkv"},
	}
	for _, tc := range cases {
		if got := ApplyRewrites(tc.in, rw); got != tc.want {
			t.Fatalf("ApplyRewrites(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	multi := []config.Rewrite{{From: "/data", To: "/A"}, {From: "/data/media", To: "/B"}}
	if got := ApplyRewrites("/data/media/x", multi); got != "/A/media/x" {
		t.Fatalf("first-match: got %q", got)
	}
	if got := ApplyRewrites("/data/media/x", nil); got != "/data/media/x" {
		t.Fatalf("nil rewrites: got %q", got)
	}

	// Segment-boundary matching: a sibling dir sharing the prefix must NOT match.
	boundary := []config.Rewrite{{From: "/data/media", To: "/mnt"}}
	if got := ApplyRewrites("/data/media2/x", boundary); got != "/data/media2/x" {
		t.Fatalf("boundary: /data/media2/x must not rewrite, got %q", got)
	}
	if got := ApplyRewrites("/data/media/x", boundary); got != "/mnt/x" {
		t.Fatalf("boundary: /data/media/x -> /mnt/x, got %q", got)
	}
	if got := ApplyRewrites("/data/media", boundary); got != "/mnt" {
		t.Fatalf("boundary: exact /data/media -> /mnt, got %q", got)
	}
}

func TestNormalizeSeparators(t *testing.T) {
	if got := NormalizeSeparators(`C:\Media\Movies\Dune\Dune.mkv`); got != "C:/Media/Movies/Dune/Dune.mkv" {
		t.Fatalf("NormalizeSeparators(windows) = %q", got)
	}
	// POSIX paths are unchanged.
	if got := NormalizeSeparators("/mnt/media/x.mkv"); got != "/mnt/media/x.mkv" {
		t.Fatalf("NormalizeSeparators(posix) = %q", got)
	}
	// A normalized Windows path then rewrites on the Linux host.
	rw := []config.Rewrite{{From: "C:/Media", To: "/mnt/media"}}
	if got := ApplyRewrites(NormalizeSeparators(`C:\Media\ShowA\S01\E01.mkv`), rw); got != "/mnt/media/ShowA/S01/E01.mkv" {
		t.Fatalf("windows rewrite = %q", got)
	}
}
