package unpack

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

func TestFixNamesAfterExtract(t *testing.T) {
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

// tree lays out files (slash paths) below a new temp folder.
func tree(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Dir(p), filepath.Base(p), []byte(f))
	}
	return dir
}

func TestNameFixReportsMoves(t *testing.T) {
	files := []string{"#U30a2/#U30aa.mp3", "#U30a2/plain.mp3", "x #U30a2.mp3", "x #U30A2.mp3", "ok.mp3", ".trash/#U30aa.mp3"}
	skip := func(rel string, isDir bool) bool { return isDir && strings.HasPrefix(rel, ".") }
	// The two "x" files decode to the same name: the first in directory order gets it.
	want := map[string]string{
		"#U30a2/#U30aa.mp3": "ア/オ.mp3",
		"#U30a2/plain.mp3":  "ア/plain.mp3",
		"x #U30A2.mp3":      "x ア.mp3",
	}

	dry := tree(t, files...)
	if got := (NameFix{DryRun: true, Skip: skip}).Run(dry); !reflect.DeepEqual(got, want) {
		t.Errorf("dry run: %v", got)
	}
	if got := strings.Join(listTree(t, dry), ","); got != strings.Join(sorted(files), ",") {
		t.Errorf("a dry run must not touch the disk: %s", got)
	}

	dir := tree(t, files...)
	if got := (NameFix{Skip: skip}).Run(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("run: %v", got)
	}
	if got := strings.Join(listTree(t, dir), ","); got != ".trash/#U30aa.mp3,ok.mp3,x #U30a2.mp3,x ア.mp3,ア/plain.mp3,ア/オ.mp3" {
		t.Errorf("tree: %s", got)
	}
}

func TestNameFixKeepsANameItCannotRename(t *testing.T) {
	defer func(f func(string, string) error) { renameFile = f }(renameFile)
	renameFile = func(string, string) error { return errors.New("busy") }
	dir := tree(t, "#U30a2/#U30aa.mp3")
	if got := FixNames(dir); len(got) != 0 {
		t.Errorf("nothing moved, nothing to report: %v", got)
	}
	if got := strings.Join(listTree(t, dir), ","); got != "#U30a2/#U30aa.mp3" {
		t.Errorf("tree: %s", got)
	}
}

func sorted(s []string) []string {
	s = append([]string(nil), s...)
	sort.Strings(s)
	return s
}
