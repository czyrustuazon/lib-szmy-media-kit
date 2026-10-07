package unpack

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeEscapes(t *testing.T) {
	cases := map[string]string{
		"plain.mp3":                           "plain.mp3",
		"We are ROCK-MEN! #U3010#U30aa#U3011": "We are ROCK-MEN! 【オ】",
		"#U30AA upper hex":                    "オ upper hex",
		"emoji #L01f3b5.opus":                 "emoji 🎵.opus",
		"#U0041 is ASCII, never escaped":      "#U0041 is ASCII, never escaped",
		"#Ud800 is a lone surrogate":          "#Ud800 is a lone surrogate",
		"#L110000 is past Unicode":            "#L110000 is past Unicode",
		"#U30x0 is not hex":                   "#U30x0 is not hex",
		"short #U30":                          "short #U30",
		"#X3010 and # and #":                  "#X3010 and # and #",
		"Track #Live":                         "Track #Live",
	}
	for in, want := range cases {
		if got := DecodeEscapes(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestFixNames(t *testing.T) {
	dir := t.TempDir()
	if err := Extract(nil, "a.zip", writeFile(t, dir, "a.zip", zipOf(t, map[string][]byte{
		"#U30a2 Album/01 #U3010#U30aa#U3011.opus": []byte("one"),
		"#U30a2 Album/02 plain.opus":              []byte("two"),
		"taken #U30a2.opus":                       []byte("escaped"),
		"taken ア.opus":                            []byte("real"),
	})), filepath.Join(dir, "out"), 1<<20); err != nil {
		t.Fatal(err)
	}
	// An escaped name whose real name is already there keeps its escapes rather than overwrite.
	want := "taken #U30a2.opus,taken ア.opus,ア Album/01 【オ】.opus,ア Album/02 plain.opus"
	if got := strings.Join(listTree(t, filepath.Join(dir, "out")), ","); got != want {
		t.Errorf("tree: %s", got)
	}
}
