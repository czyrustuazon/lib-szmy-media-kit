package unpack

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- helpers

func zipOf(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, data := range entries {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		f.Write(data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

// ---------------------------------------------------------------- names

func TestIsArchiveAndIsZip(t *testing.T) {
	for name, want := range map[string]bool{"a.zip": true, "A.ZIP": true, "b.7z": true, "c.mp3": false, "d.rar": false, "zip": false} {
		if IsArchive(name) != want {
			t.Errorf("IsArchive(%s): want %v", name, want)
		}
	}
	if !IsZip("x.Zip") || IsZip("x.7z") {
		t.Error("IsZip")
	}
}

// ---------------------------------------------------------------- zip

func TestZipExtractsAndVerifies(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "a.zip", zipOf(t, map[string][]byte{"Album/": nil, "Album/01.mp3": []byte("one"), "Album/d/02.mp3": []byte("two")}))
	if err := Verify(nil, "a.zip", p); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := Extract(nil, "a.zip", p, out, 1<<20); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listTree(t, out), ","); got != "Album/01.mp3,Album/d/02.mp3" {
		t.Errorf("tree: %s", got)
	}
}

func TestZipSymlinkEntriesAreNeverExtracted(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	real, _ := w.CreateHeader(&zip.FileHeader{Name: "real.mp3", Method: zip.Store})
	real.Write([]byte("audio"))
	hdr := &zip.FileHeader{Name: "link.mp3", Method: zip.Store}
	hdr.SetMode(os.ModeSymlink | 0o777)
	link, _ := w.CreateHeader(hdr)
	link.Write([]byte("/etc/passwd")) // a symlink entry's data is its target path
	w.Close()
	dir := t.TempDir()
	p := writeFile(t, dir, "links.zip", buf.Bytes())
	out := filepath.Join(dir, "out")
	if err := Zip(p, out, 1<<20); err != nil {
		t.Fatal(err)
	}
	if files := listTree(t, out); len(files) != 1 || files[0] != "real.mp3" {
		t.Errorf("only the real file may arrive, got %v", files)
	}
}

func TestZipSlipIsRefused(t *testing.T) {
	for _, godebug := range []string{"", "zipinsecurepath=0"} {
		// With zipinsecurepath=0 Go reports ErrInsecurePath alongside a usable reader.
		t.Setenv("GODEBUG", godebug)
		dir := t.TempDir()
		p := writeFile(t, dir, "evil.zip", zipOf(t, map[string][]byte{"../../evil.mp3": []byte("x")}))
		out := filepath.Join(dir, "a", "b", "out")
		if err := Zip(p, out, 1<<20); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Errorf("GODEBUG=%q: %v", godebug, err)
		}
		if exists(filepath.Join(dir, "a", "evil.mp3")) {
			t.Fatal("a zip entry escaped the destination")
		}
	}
}

func TestZipThatExpandsPastTheBudgetIsRefused(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("big.bin") // deflated: tiny on the wire, 20 kB once extracted
	f.Write(bytes.Repeat([]byte{0}, 20000))
	w.Close()
	dir := t.TempDir()
	p := writeFile(t, dir, "bomb.zip", buf.Bytes())
	if err := Zip(p, filepath.Join(dir, "out"), 4000); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestZipReadingErrors(t *testing.T) {
	dir := t.TempDir()

	notZip := writeFile(t, dir, "x.zip", []byte("nope"))
	if err := Verify(nil, "x.zip", notZip); err == nil || !strings.Contains(err.Error(), "opening zip") {
		t.Errorf("not a zip: %v", err)
	}
	if err := Zip(notZip, dir, 1<<20); err == nil || !strings.Contains(err.Error(), "opening zip") {
		t.Errorf("extract a non-zip: %v", err)
	}

	// An entry that uses a compression method nobody implements cannot be read.
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	if _, err := w.CreateHeader(&zip.FileHeader{Name: "dir/", Method: zip.Store}); err != nil { // a folder entry, skipped by Verify
		t.Fatal(err)
	}
	raw, err := w.CreateRaw(&zip.FileHeader{Name: "weird.mp3", Method: 99, CompressedSize64: 3, UncompressedSize64: 3})
	if err != nil {
		t.Fatal(err)
	}
	raw.Write([]byte("xyz"))
	w.Close()
	weird := writeFile(t, dir, "weird.zip", buf.Bytes())
	if err := Verify(nil, "weird.zip", weird); err == nil || !strings.Contains(err.Error(), "reading zip entry") {
		t.Errorf("unreadable entry (verify): %v", err)
	}
	if err := Zip(weird, filepath.Join(dir, "out1"), 1<<20); err == nil || !strings.Contains(err.Error(), "reading zip entry") {
		t.Errorf("unreadable entry (extract): %v", err)
	}

	// A zip whose entry data is corrupt fails its checksum while being read.
	good := zipOf(t, map[string][]byte{"a.mp3": bytes.Repeat([]byte("x"), 300)})
	good[60] ^= 0xFF
	broken := writeFile(t, dir, "broken.zip", good)
	if err := Verify(nil, "broken.zip", broken); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("corrupt entry (verify): %v", err)
	}
	if err := Zip(broken, filepath.Join(dir, "out2"), 1<<20); err == nil || !strings.Contains(err.Error(), "writing") {
		t.Errorf("corrupt entry (extract): %v", err)
	}
}

func TestZipFileSystemFailures(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "blocker", []byte("x"))
	p := filepath.Join(dir, "a.zip")

	// A folder entry where a file already sits.
	os.WriteFile(p, zipOf(t, map[string][]byte{"blocker/": nil}), 0o644)
	if err := Zip(p, dir, 1<<20); err == nil {
		t.Error("cannot create a folder over a file")
	}
	// A file whose parent folder cannot be created.
	os.WriteFile(p, zipOf(t, map[string][]byte{"blocker/a.mp3": []byte("x")}), 0o644)
	if err := Zip(p, dir, 1<<20); err == nil {
		t.Error("cannot create a parent folder over a file")
	}
	// A file whose target is already a folder.
	os.Mkdir(filepath.Join(dir, "taken.mp3"), 0o755)
	os.WriteFile(p, zipOf(t, map[string][]byte{"taken.mp3": []byte("x")}), 0o644)
	if err := Zip(p, dir, 1<<20); err == nil || !strings.Contains(err.Error(), "writing") {
		t.Errorf("target is a folder: %v", err)
	}
}

// ---------------------------------------------------------------- 7z (through a fake binary)

func fake7z(files map[string][]byte, failOn string, calls *[]string) Exec {
	return func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, name+" "+args[0])
		switch {
		case args[0] == "t" && failOn == "t":
			return []byte("CRC Failed"), errors.New("exit status 2")
		case args[0] == "l" && failOn == "l":
			return []byte("Can not open the file as archive"), errors.New("exit status 2")
		case args[0] == "l":
			var b strings.Builder
			b.WriteString("7-Zip [64] 16.02\n\nListing archive: a.7z\n\n--\nPath = a.7z\nType = 7z\n\n----------\n")
			for rel, data := range files {
				fmt.Fprintf(&b, "Path = %s\nSize = %d\nAttributes = A_ -rw-r--r--\n\n", filepath.FromSlash(rel), len(data))
			}
			return []byte(b.String()), nil
		case args[0] == "x" && failOn == "x":
			return []byte("disk full"), errors.New("exit status 2")
		case args[0] == "x":
			out := strings.TrimPrefix(args[2], "-o")
			for rel, data := range files {
				p := filepath.Join(out, rel)
				os.MkdirAll(filepath.Dir(p), 0o755)
				os.WriteFile(p, data, 0o644)
			}
		}
		return nil, nil
	}
}

func TestSevenZip(t *testing.T) {
	var calls []string
	run := fake7z(map[string][]byte{"Packed/01.mp3": []byte("one")}, "", &calls)
	out := filepath.Join(t.TempDir(), "out")
	if err := Verify(run, "a.7z", "a.7z"); err != nil {
		t.Fatal(err)
	}
	if err := Extract(run, "a.7z", "a.7z", out, 1<<20); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "7z t,7z l,7z x" {
		t.Errorf("expected a test, a listing, then an extraction, got %v", calls)
	}
	if files := listTree(t, out); len(files) != 1 || files[0] != "Packed/01.mp3" {
		t.Errorf("files: %v", files)
	}
}

func TestSevenZipFailures(t *testing.T) {
	for failOn, want := range map[string]string{"t": "7z integrity test failed", "l": "7z listing failed", "x": "7z extraction failed"} {
		var calls []string
		run := fake7z(nil, failOn, &calls)
		err := Verify(run, "a.7z", "a.7z")
		if err == nil {
			err = SevenZip(run, "a.7z", t.TempDir(), 1<<20)
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("fail on %s: %v", failOn, err)
		}
	}
	if out, err := DefaultExec("definitely-not-a-real-binary-xyz", "t"); err == nil {
		t.Errorf("the default runner must report a missing binary, got %q", out)
	}
}

func TestSevenZipRefusedBeforeExtracting(t *testing.T) {
	var calls []string
	run := func(name string, args ...string) ([]byte, error) {
		calls = append(calls, args[0])
		return []byte("----------\nPath = evil\nSize = 4\nAttributes = A_ lrwxrwxrwx\n"), nil
	}
	if err := SevenZip(run, "a.7z", t.TempDir(), 1<<20); err == nil || !strings.Contains(err.Error(), "link") || strings.Join(calls, ",") != "l" {
		t.Errorf("a link must stop 7z before x runs: %v %v", err, calls)
	}
}

func TestSevenZipSizeIsCheckedAfterExtractingToo(t *testing.T) {
	dir := t.TempDir()
	run := func(name string, args ...string) ([]byte, error) {
		if args[0] == "l" {
			return []byte("----------\nPath = a.mp3\nSize = 1\nAttributes = A\n"), nil // lies
		}
		os.WriteFile(filepath.Join(strings.TrimPrefix(args[2], "-o"), "a.mp3"), make([]byte, 50), 0o644)
		return nil, nil
	}
	if err := SevenZip(run, "a.7z", dir, 10); !errors.Is(err, ErrTooLarge) {
		t.Errorf("got %v", err)
	}
}

func TestSevenZipListingChecks(t *testing.T) {
	head := "Listing archive: a.7z\n--\nPath = a.7z\nType = 7z\nPhysical Size = 99\n\n----------\n"
	entry := func(path, size, attrs string) string {
		return "Path = " + path + "\nSize = " + size + "\nAttributes = " + attrs + "\n\n"
	}
	ok := []string{
		head + entry("Album/01.mp3", "100", "A_ -rw-r--r--") + entry("Album", "0", "D_ drwxr-xr-x") + entry(`Win\02.mp3`, "100", "A"),
		head + entry("..odd name/01.mp3", "100", "A") + "Size = not-a-number\nSymbolic Link = \n",
		"\r\n----------\r\n" + strings.ReplaceAll(entry("a.mp3", "1000", "A"), "\n", "\r\n"),
	}
	for i, out := range ok {
		if err := checkListing([]byte(out), 1000); err != nil {
			t.Errorf("ok[%d]: %v", i, err)
		}
	}
	bad := map[string]string{
		"unix symlink":    head + entry("Album/cover", "10", "A_ lrwxrwxrwx"),
		"windows link":    head + entry("Album/cover", "10", "AL"),
		"link target":     head + entry("Album/cover", "10", "A") + "Symbolic Link = /etc\n",
		"hard link":       head + entry("Album/x", "10", "A") + "Hard Link = Album/y\n",
		"absolute":        head + entry("/etc/cron.d/x", "10", "A"),
		"drive":           head + entry(`C:\x`, "10", "A"),
		"climbs":          head + entry("../x", "10", "A"),
		"climbs inside":   head + entry(`a\..\..\x`, "10", "A"),
		"ends climbing":   head + entry("a/..", "0", "D"),
		"just dots":       head + entry("..", "0", "D"),
		"bomb":            head + entry("a.wav", "600", "A") + entry("b.wav", "600", "A"),
		"no entries list": "Listing archive: a.7z\nPath = a.7z\n",
	}
	for name, out := range bad {
		if err := checkListing([]byte(out), 1000); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if err := checkListing([]byte(head+entry("a.wav", "1001", "A")), 1000); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
	if isLinkAttributes("A_ -rw-r--r--") || isLinkAttributes("") || isLinkAttributes("A lrwx") {
		t.Error("plain files are not links")
	}
}

// ---------------------------------------------------------------- tidying

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	writeFile(t, dir, "x", make([]byte, 10))
	writeFile(t, filepath.Join(dir, "a", "b"), "y", make([]byte, 5))
	os.Symlink(filepath.Join(dir, "x"), filepath.Join(dir, "link")) // ignored if supported
	if got := DirSize(dir); got != 15 {
		t.Errorf("got %d", got)
	}
	if got := DirSize(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("missing dir: %d", got)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	for p, data := range map[string][]byte{
		"keep/a.mp3": []byte("a"), "keep/deep/b.flac": []byte("b"), "drop/readme.txt": []byte("x"), "drop/sub/c.png": []byte("y"),
	} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		writeFile(t, dir, p, data)
	}
	outside := filepath.Join(t.TempDir(), "secret.mp3")
	os.WriteFile(outside, []byte("s"), 0o644)
	linked := os.Symlink(outside, filepath.Join(dir, "link.mp3")) == nil

	var seen []string
	skipped := Prune(dir, func(p string) bool {
		rel, _ := filepath.Rel(dir, p)
		seen = append(seen, filepath.ToSlash(rel))
		return strings.HasPrefix(filepath.ToSlash(rel), "keep/")
	})
	want := []string{"drop/readme.txt", "drop/sub/c.png", "link.mp3"}
	if !linked {
		want = want[:2]
	}
	if !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped %v, want %v", skipped, want)
	}
	if strings.Join(seen, ",") != "drop/readme.txt,drop/sub/c.png,keep/a.mp3,keep/deep/b.flac" {
		t.Errorf("keep must see every regular file once, never a link: %v", seen)
	}
	if exists(filepath.Join(dir, "drop")) || !exists(filepath.Join(dir, "keep", "deep", "b.flac")) {
		t.Error("emptied folders go, kept files stay")
	}
	if !exists(outside) {
		t.Error("pruning must not follow a link and delete its target")
	}

	// keep == nil keeps every regular file and still drops links.
	other := t.TempDir()
	writeFile(t, other, "x.bin", []byte("x"))
	os.Symlink(outside, filepath.Join(other, "l"))
	if got := Prune(other, nil); linked && (len(got) != 1 || got[0] != "l") || !exists(filepath.Join(other, "x.bin")) {
		t.Errorf("nil keep: %v", got)
	}
	if got := Prune(filepath.Join(dir, "missing"), nil); len(got) != 0 {
		t.Errorf("missing dir: %v", got)
	}
}

func TestUnwrapLone(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "A", "B"), 0o755)
	writeFile(t, filepath.Join(dir, "A", "B"), "x.mp3", nil)
	writeFile(t, filepath.Join(dir, "A", "B"), "y.mp3", nil)
	if got := UnwrapLone(dir, ""); got != "A/B" {
		t.Errorf("stripped prefix %q, want A/B", got)
	}
	if files := listTree(t, dir); len(files) != 2 || files[0] != "x.mp3" || files[1] != "y.mp3" {
		t.Errorf("two wrappers should be removed: %v", files)
	}

	// Several entries at the top: nothing to unwrap.
	multi := t.TempDir()
	os.MkdirAll(filepath.Join(multi, "A"), 0o755)
	writeFile(t, filepath.Join(multi, "A"), "x.mp3", nil)
	writeFile(t, multi, "y.mp3", nil)
	if got := UnwrapLone(multi, ""); got != "" {
		t.Errorf("nothing to unwrap, got prefix %q", got)
	}
	if !exists(filepath.Join(multi, "A", "x.mp3")) {
		t.Error("a folder next to a file is not a wrapper")
	}

	// "Album/Album": the inner folder would collide with its own wrapper, so it is left alone.
	clash := t.TempDir()
	os.MkdirAll(filepath.Join(clash, "Album", "Album"), 0o755)
	writeFile(t, filepath.Join(clash, "Album", "Album"), "z.mp3", nil)
	if got := UnwrapLone(clash, ""); got != "" {
		t.Errorf("clash left alone, got prefix %q", got)
	}
	if !exists(filepath.Join(clash, "Album", "Album", "z.mp3")) && !exists(filepath.Join(clash, "Album", "z.mp3")) {
		t.Error("files must never be lost while unwrapping")
	}

	// A folder that already exists in the destination is content, not a wrapper.
	dest := t.TempDir()
	os.Mkdir(filepath.Join(dest, "d1"), 0o755)
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "Outer", "d1"), 0o755)
	writeFile(t, filepath.Join(src, "Outer", "d1"), "n.mp3", nil)
	if got := UnwrapLone(src, dest); got != "Outer" {
		t.Errorf("stripped %q, want Outer", got)
	}
	if !exists(filepath.Join(src, "d1", "n.mp3")) {
		t.Errorf("the wrapper goes, the existing folder stays: %v", listTree(t, src))
	}

	// A missing directory and a file are no-ops.
	if got := UnwrapLone(filepath.Join(dir, "missing"), ""); got != "" {
		t.Errorf("missing dir: %q", got)
	}
	if got := UnwrapLone(writeFile(t, t.TempDir(), "file", nil), ""); got != "" {
		t.Errorf("file: %q", got)
	}
}
