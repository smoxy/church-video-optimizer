// Package naming builds the canonical artifact filename and human-readable
// slug defined by adr-0008 / contract-video-volume:
//
//	{category}_{resource_id}_{n}_{slug}.mp4
//
// n is a 1-based progressive index of the video within its source resource
// (a zip can contain several videos); slug is derived from the original
// filename, falling back to the resource title.
package naming

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxSlugLen is the maximum length (in bytes; the slug is ASCII-only) of the
// slug segment of an artifact filename.
const MaxSlugLen = 40

// FallbackSlug is used when neither the original filename nor the resource
// title yield any sluggable character, so the artifact name stays valid
// (uniqueness is still guaranteed by the {resource_id}_{n} prefix).
const FallbackSlug = "video"

// accentFolder maps common accented Latin letters (Italian titles in
// particular) to their plain ASCII equivalent. Slugify lowercases its input
// first, so only lowercase accented runes need an entry here.
var accentFolder = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a",
	"è", "e", "é", "e", "ê", "e", "ë", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o",
	"ù", "u", "ú", "u", "û", "u", "ü", "u",
	"ý", "y", "ÿ", "y",
	"ñ", "n",
	"ç", "c",
)

// invalidRun matches any run of characters that are not [a-z0-9]; Slugify
// collapses each run into a single dash.
var invalidRun = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify converts s into a URL/filesystem-safe slug: lowercase ASCII,
// alphanumerics only, words separated by single dashes, no leading/trailing
// dash, at most MaxSlugLen characters. It returns "" if s has no sluggable
// content (e.g. empty, or only punctuation/whitespace).
func Slugify(s string) string {
	s = strings.ToLower(s)
	s = accentFolder.Replace(s)
	s = invalidRun.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")

	if len(s) > MaxSlugLen {
		// invalidRun guarantees the slug is pure ASCII at this point, so a
		// byte-index slice never splits a multi-byte rune.
		s = strings.TrimRight(s[:MaxSlugLen], "-")
	}

	return s
}

// DeriveSlug builds the human-readable slug for an artifact: it prefers the
// original filename (extension stripped), and falls back to the resource
// title when the filename has no sluggable content. If both are unusable it
// falls back to FallbackSlug so the artifact name is always well-formed.
func DeriveSlug(originalFilename, fallbackTitle string) string {
	stem := strings.TrimSuffix(originalFilename, filepath.Ext(originalFilename))
	if slug := Slugify(stem); slug != "" {
		return slug
	}
	if slug := Slugify(fallbackTitle); slug != "" {
		return slug
	}
	return FallbackSlug
}

// ArtifactFilename builds the canonical artifact name (adr-0008):
//
//	{category}_{resource_id}_{n}_{slug}.mp4
//
// category and slug are used verbatim (callers are expected to pass an
// already-sanitized slug, e.g. from DeriveSlug/Slugify).
func ArtifactFilename(category string, resourceID, index int, slug string) string {
	return fmt.Sprintf("%s_%d_%d_%s.mp4", category, resourceID, index, slug)
}
