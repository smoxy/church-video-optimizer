package naming

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"simple words", "Decime di Luglio", "decime-di-luglio"},
		{"accents and punctuation", "Città è bella!!", "citta-e-bella"},
		{"leading/trailing/duplicate spaces", "   spaced   out ", "spaced-out"},
		{"already a slug", "already-slug-123", "already-slug-123"},
		{"empty input", "", ""},
		{"only punctuation", "!!!___...", ""},
		{"underscores", "hello_world_video", "hello-world-video"},
		{"uppercase", "HELLO WORLD", "hello-world"},
		{"mixed accents", "Über café naïve", "uber-cafe-naive"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Slugify(c.in)
			if got != c.want {
				t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSlugify_TruncatesLongInputToMaxLen(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := Slugify(long)

	want := strings.Repeat("a", MaxSlugLen)
	if got != want {
		t.Fatalf("Slugify(100 a's) = %q (len %d), want %q (len %d)", got, len(got), want, len(want))
	}
}

func TestSlugify_TrimsTrailingDashIntroducedByTruncation(t *testing.T) {
	// Pre-truncation the string is 39 'a's + '-' (at index 39, i.e. the very
	// last byte kept by a naive s[:40] slice) + more content. Truncating
	// must not leave that dash dangling at the end.
	in := strings.Repeat("a", 39) + " " + strings.Repeat("b", 10)
	want := strings.Repeat("a", 39)

	got := Slugify(in)
	if got != want {
		t.Fatalf("Slugify(%q) = %q, want %q", in, got, want)
	}
	if len(got) > MaxSlugLen {
		t.Fatalf("Slugify result exceeds MaxSlugLen: %d chars: %q", len(got), got)
	}
}

func TestDeriveSlug(t *testing.T) {
	cases := []struct {
		name             string
		originalFilename string
		title            string
		want             string
	}{
		{"uses filename stem", "Decime di Luglio.mp4", "irrelevant title", "decime-di-luglio"},
		{"strips extension case-insensitively", "clip.MP4", "irrelevant title", "clip"},
		{"strips mkv extension", "seconda parte.mkv", "irrelevant title", "seconda-parte"},
		{"falls back to title when filename unusable", "???.mp4", "Missioni di Luglio", "missioni-di-luglio"},
		{"falls back to FallbackSlug when both unusable", "???.mp4", "!!!", FallbackSlug},
		{"falls back to FallbackSlug when both empty", "", "", FallbackSlug},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DeriveSlug(c.originalFilename, c.title)
			if got != c.want {
				t.Errorf("DeriveSlug(%q, %q) = %q, want %q", c.originalFilename, c.title, got, c.want)
			}
		})
	}
}

func TestSanitizeCategory(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"already clean", "vga", "vga"},
		{"uppercase", "VGA", "vga"},
		{"path traversal dots and slashes", "../../etc/passwd", "etc-passwd"},
		{"embedded slash", "foo/bar", "foo-bar"},
		{"spaces and punctuation", "Video Generale Annuale!", "video-generale-annuale"},
		{"url-breaking characters", "vga?x=1&y=2#z", "vga-x-1-y-2-z"},
		{"empty falls back", "", FallbackCategory},
		{"only invalid chars falls back", "../../../", FallbackCategory},
		{"only punctuation falls back", "!!!___...", FallbackCategory},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SanitizeCategory(c.in)
			if got != c.want {
				t.Errorf("SanitizeCategory(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSanitizeCategory_TruncatesLongInputToMaxLen(t *testing.T) {
	long := strings.Repeat("c", 100)
	got := SanitizeCategory(long)

	want := strings.Repeat("c", MaxSlugLen)
	if got != want {
		t.Fatalf("SanitizeCategory(100 c's) = %q (len %d), want %q (len %d)", got, len(got), want, len(want))
	}
}

func TestArtifactFilename_SanitizesUnsafeCategory(t *testing.T) {
	cases := []struct {
		name     string
		category string
		want     string
	}{
		{
			"path traversal category can't escape the output directory",
			"../../../etc/cron.d",
			"etc-cron-d_1_1_slug.mp4",
		},
		{
			"embedded slash can't introduce a URL/path segment",
			"vga/../../etc",
			"vga-etc_1_1_slug.mp4",
		},
		{
			"empty category falls back instead of producing a malformed name",
			"",
			FallbackCategory + "_1_1_slug.mp4",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ArtifactFilename(c.category, 1, 1, "slug")
			if got != c.want {
				t.Errorf("ArtifactFilename(%q, 1, 1, %q) = %q, want %q", c.category, "slug", got, c.want)
			}
			if strings.ContainsAny(got, "/\\") {
				t.Errorf("ArtifactFilename(%q, ...) = %q contains a path separator", c.category, got)
			}
		})
	}
}

func TestArtifactFilename(t *testing.T) {
	cases := []struct {
		name       string
		category   string
		resourceID int
		index      int
		slug       string
		want       string
	}{
		{"basic", "vga", 42, 1, "decime-luglio", "vga_42_1_decime-luglio.mp4"},
		{"progressive index", "gcv", 7, 2, "seconda-parte", "gcv_7_2_seconda-parte.mp4"},
		{"fallback slug", "mis", 100, 1, FallbackSlug, "mis_100_1_video.mp4"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ArtifactFilename(c.category, c.resourceID, c.index, c.slug)
			if got != c.want {
				t.Errorf("ArtifactFilename(%q, %d, %d, %q) = %q, want %q",
					c.category, c.resourceID, c.index, c.slug, got, c.want)
			}
		})
	}
}
