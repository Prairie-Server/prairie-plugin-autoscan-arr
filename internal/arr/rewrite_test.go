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
	// Most-specific match wins regardless of slice order.
	multi := []config.Rewrite{{From: "/data", To: "/A"}, {From: "/data/media", To: "/B"}}
	if got := ApplyRewrites("/data/media/x", multi); got != "/B/x" {
		t.Fatalf("most-specific: got %q, want %q", got, "/B/x")
	}
	// Broad rule still applies when it is the only/longest match.
	if got := ApplyRewrites("/data/other/f.mkv", multi); got != "/A/other/f.mkv" {
		t.Fatalf("broad-fallback: got %q, want %q", got, "/A/other/f.mkv")
	}
	if got := ApplyRewrites("/data/media/x", nil); got != "/data/media/x" {
		t.Fatalf("nil rewrites: got %q", got)
	}

	// Broad-first ordering: specific nested rewrite wins even when listed after
	// the broad one. This is the shadowing bug that was fixed.
	shadowRewrites := []config.Rewrite{
		{From: "/data", To: "/x"},
		{From: "/data/media/tv", To: "/library/tv"},
	}
	if got := ApplyRewrites("/data/media/tv/Show/S01E01.mkv", shadowRewrites); got != "/library/tv/Show/S01E01.mkv" {
		t.Fatalf("shadow specific: got %q, want %q", got, "/library/tv/Show/S01E01.mkv")
	}
	if got := ApplyRewrites("/data/other/f.mkv", shadowRewrites); got != "/x/other/f.mkv" {
		t.Fatalf("shadow broad: got %q, want %q", got, "/x/other/f.mkv")
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
