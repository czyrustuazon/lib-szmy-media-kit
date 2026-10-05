package resumable

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/czyrustuazon/lib-szmy-media-kit/unpack"
)

// ---------------------------------------------------------------- zip archives

func TestZipIsUnpackedUnwrappedAndMerged(t *testing.T) {
	m, root := newMgr(t, func(o *Options) { o.Classify = func(p string) string { return filepath.Ext(p) } })
	rel := start(t, m, "Great Album")
	archive := zipOf(t, map[string][]byte{
		"Great Album/01 One.mp3":        song,
		"Great Album/Disc 2/02 Two.mp3": song,
		"Great Album/notes.txt":         []byte("liner notes"),
	})
	send(t, m, rel, "great.zip", archive, 64)
	st := finish(t, m, rel, "great.zip", len(archive))
	if st.State != Done || st.Added != 3 || st.Skipped != 0 || st.HasReport || st.Path != rel {
		t.Fatalf("status: %+v", st)
	}
	if !reflect.DeepEqual(st.Kinds, map[string]int{".mp3": 2, ".txt": 1}) {
		t.Errorf("kinds: %v", st.Kinds)
	}
	if got := strings.Join(listTree(t, filepath.Join(root, "uploads", "Great Album")), ","); got != "01 One.mp3,Disc 2/02 Two.mp3,notes.txt" {
		t.Fatalf("expected the wrapper folder to be unwrapped and everything kept, got %s", got)
	}
	// No scratch folders or staged bytes are left behind.
	entries, _ := os.ReadDir(filepath.Join(root, "uploads", "Great Album"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("hidden leftover %s", e.Name())
		}
	}
	if staged, _ := os.ReadDir(filepath.Join(root, "uploads", StagingDirName)); len(staged) != 0 {
		t.Errorf("staging should be empty: %v", staged)
	}
}

func TestPruneHookAndReport(t *testing.T) {
	m, root := newMgr(t, func(o *Options) {
		o.Prune = func(dir string) ([]string, error) {
			return unpack.Prune(dir, func(p string) bool { return strings.HasSuffix(p, ".mp3") }), nil
		}
		o.ReportNote = "Only mp3 files are kept."
	})
	rel := start(t, m, "A")
	archive := zipOf(t, map[string][]byte{"a.mp3": song, "notes.txt": []byte("x"), "run.sh": []byte("rm -rf /")})
	send(t, m, rel, "a.zip", archive, 4096)
	st := finish(t, m, rel, "a.zip", len(archive))
	if st.State != Done || st.Added != 1 || st.Skipped != 2 || !st.HasReport || !reflect.DeepEqual(st.SkippedTypes, map[string]int{"txt": 1, "sh": 1}) {
		t.Fatalf("status: %+v", st)
	}
	if files := listTree(t, filepath.Join(root, "uploads", "A")); strings.Join(files, ",") != "a.mp3" {
		t.Errorf("files: %v", files)
	}
	rp, err := m.Report(rel, "a.zip")
	if err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(rp)
	if string(text) != "2 files from a.zip were left out.\nOnly mp3 files are kept.\n\nnotes.txt\nrun.sh\n" {
		t.Errorf("report:\n%s", text)
	}

	// A wrapper folder is unwrapped from the tree and from the report of what was left out.
	rel2 := start(t, m, "Great Album")
	wrapped := zipOf(t, map[string][]byte{
		"Great Album/01.mp3":    song,
		"Great Album/notes.txt": []byte("liner notes"),
		"Great Album/run.sh":    []byte("rm -rf /"),
	})
	send(t, m, rel2, "great.zip", wrapped, 4096)
	st = finish(t, m, rel2, "great.zip", len(wrapped))
	if st.State != Done || st.Added != 1 || st.Skipped != 2 || !st.HasReport {
		t.Fatalf("wrapped status: %+v", st)
	}
	if got := strings.Join(listTree(t, filepath.Join(root, "uploads", "Great Album")), ","); got != "01.mp3" {
		t.Fatalf("wrapper unwrapped: got %s", got)
	}
	rp, err = m.Report(rel2, "great.zip")
	if err != nil {
		t.Fatal(err)
	}
	text, _ = os.ReadFile(rp)
	if string(text) != "2 files from great.zip were left out.\nOnly mp3 files are kept.\n\nnotes.txt\nrun.sh\n" {
		t.Errorf("wrapped report must drop the wrapper prefix:\n%s", text)
	}
}

func TestPruneErrorFailsTheUpload(t *testing.T) {
	m, root := newMgr(t, func(o *Options) {
		o.Prune = func(string) ([]string, error) { return nil, errors.New("contains no audio files") }
	})
	rel := start(t, m, "x")
	archive := zipOf(t, map[string][]byte{"readme.txt": []byte("hi")})
	send(t, m, rel, "text.zip", archive, 4096)
	if st := finish(t, m, rel, "text.zip", len(archive)); st.State != Failed || st.Error != "text.zip: contains no audio files" {
		t.Errorf("status: %+v", st)
	}
	if files := listTree(t, filepath.Join(root, "uploads")); len(files) != 0 {
		t.Errorf("nothing may be added or left staged: %v", files)
	}
}

func TestAnArchiveExtractDeclinesIsPlacedAsAFile(t *testing.T) {
	m, root := newMgr(t, func(o *Options) { o.Extract = func(string) bool { return false } })
	rel := start(t, m, "x")
	archive := zipOf(t, map[string][]byte{"a.mp3": song})
	send(t, m, rel, "keep.zip", archive, 4096)
	if st := finish(t, m, rel, "keep.zip", len(archive)); st.State != Done || st.Path != rel+"/keep.zip" {
		t.Errorf("status: %+v", st)
	}
	if !exists(filepath.Join(root, "uploads", "x", "keep.zip")) {
		t.Error("the archive itself is placed")
	}
}

func TestZipNameCollisionsKeepBothVersions(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "Batch")
	archive := zipOf(t, map[string][]byte{"Artist/Album/a.mp3": song, "Artist/Album/b.mp3": song})
	send(t, m, rel, "one.zip", archive, 4096)
	if st := finish(t, m, rel, "one.zip", len(archive)); st.State != Done || st.Added != 2 {
		t.Fatalf("first: %+v", st)
	}
	archive = zipOf(t, map[string][]byte{"Artist/Album/a.mp3": variant(1), "Artist/Album/b.mp3": variant(1)})
	send(t, m, rel, "two.zip", archive, 4096)
	if st := finish(t, m, rel, "two.zip", len(archive)); st.State != Done || st.Added != 2 || st.Duplicates != 0 {
		t.Fatalf("second: %+v", st)
	}
	if got := strings.Join(listTree(t, filepath.Join(root, "uploads", "Batch")), ","); got != "a (2).mp3,a.mp3,b (2).mp3,b.mp3" {
		t.Fatalf("both archives' files must survive, got %s", got)
	}
}

func TestUnsafeArchivesFail(t *testing.T) {
	m, root := newMgr(t)
	m.o.MaxBytes = 4000
	rel := start(t, m, "x")

	slip := zipOf(t, map[string][]byte{"../../evil.mp3": song})
	send(t, m, rel, "evil.zip", slip, 4096)
	if st := finish(t, m, rel, "evil.zip", len(slip)); st.State != Failed || !strings.Contains(st.Error, "escapes") {
		t.Errorf("zip slip: %+v", st)
	}
	if exists(filepath.Join(root, "evil.mp3")) || exists(filepath.Join(filepath.Dir(root), "evil.mp3")) {
		t.Fatal("a zip entry escaped the destination")
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("big.mp3") // deflated: tiny on the wire, 20 kB once extracted
	f.Write(bytes.Repeat([]byte{0}, 20000))
	w.Close()
	send(t, m, rel, "bomb.zip", buf.Bytes(), 4096)
	if st := finish(t, m, rel, "bomb.zip", buf.Len()); st.State != Failed || !strings.Contains(st.Error, "size limit") {
		t.Errorf("bomb: %+v", st)
	}
	if files := listTree(t, filepath.Join(root, "uploads", "x")); len(files) != 0 {
		t.Errorf("partial extraction must not be published: %v", files)
	}
}

func TestPublishingFailureIsReported(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	m.o.Rename = func(string, string) error { return errors.New("cannot rename") }
	archive := zipOf(t, map[string][]byte{"a.mp3": song})
	send(t, m, rel, "a.zip", archive, 4096)
	if st := finish(t, m, rel, "a.zip", len(archive)); st.State != Failed || !strings.Contains(st.Error, "cannot rename") {
		t.Fatalf("status: %+v", st)
	}
}

// ---------------------------------------------------------------- integrity and recovery

func TestCorruptedChunkIsLocalisedAndOnlyThatPartIsResent(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	content := append(append([]byte{}, song...), bytes.Repeat([]byte("0123456789"), 40)...)
	archive := zipOf(t, map[string][]byte{"song.mp3": content})
	const chunk = 100
	if len(archive) < 4*chunk {
		t.Fatalf("archive too small for the scenario: %d", len(archive))
	}
	send(t, m, rel, "song.zip", archive, chunk)

	// Bitrot after arrival: flip a byte inside the second chunk (bytes 100..199).
	stagingPath, _ := m.sessionPaths(rel, "song.zip")
	staged, _ := os.ReadFile(stagingPath)
	staged[150] ^= 0xFF
	os.WriteFile(stagingPath, staged, 0o644)

	st := finish(t, m, rel, "song.zip", len(archive))
	if st.State != Corrupted || st.ResumeOffset != 100 || !strings.Contains(st.Error, "100") {
		t.Fatalf("expected a corruption verdict pointing at byte 100, got %+v", st)
	}
	if info, _ := os.Stat(stagingPath); info.Size() != 100 {
		t.Fatalf("the session should be rewound to 100, is %d", info.Size())
	}

	// The client asks where to resume and re-sends only the damaged part onwards.
	off, err := m.Begin(rel, "song.zip", int64(len(archive)))
	if err != nil || off != 100 {
		t.Fatalf("resume point: %d %v", off, err)
	}
	for off < int64(len(archive)) {
		part := archive[off:min(off+chunk, int64(len(archive)))]
		if off, err = m.Append(rel, "song.zip", off, crc(part), part); err != nil {
			t.Fatal(err)
		}
	}
	if st = finish(t, m, rel, "song.zip", len(archive)); st.State != Done || st.Added != 1 {
		t.Fatalf("after recovery: %+v", st)
	}
}

func TestADamagedSourceFileIsDiscardedNotRetriedForever(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "x")
	garbage := bytes.Repeat([]byte("this is not a zip file. "), 10)
	send(t, m, rel, "bad.zip", garbage, 50)
	st := finish(t, m, rel, "bad.zip", len(garbage))
	if st.State != Failed || !strings.Contains(st.Error, "source file itself is damaged") {
		t.Fatalf("status: %+v", st)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "uploads", StagingDirName)); len(entries) != 0 {
		t.Errorf("a doomed session must not linger: %v", entries)
	}
}

func TestRepairOrDiscardEdgeCases(t *testing.T) {
	dir := t.TempDir()
	meta := &sessionMeta{Chunks: []chunk{{Offset: 0, Length: 3, CRC32: crc([]byte("abc"))}}}

	// The staged file is missing altogether.
	err := repairOrDiscard(filepath.Join(dir, "missing.partial"), filepath.Join(dir, "m.json"), meta, errors.New("bad zip"))
	if err == nil || !strings.Contains(err.Error(), "could not be re-examined") {
		t.Errorf("missing staging: %v", err)
	}

	// A chunk cannot be read back and the rewind fails too (a directory stands in for the file).
	asDir := filepath.Join(dir, "dir.partial")
	os.Mkdir(asDir, 0o755)
	err = repairOrDiscard(asDir, filepath.Join(dir, "m.json"), meta, errors.New("bad zip"))
	if err == nil || !strings.Contains(err.Error(), "rewinding the session failed") {
		t.Errorf("rewind failure: %v", err)
	}
	if got := (&CorruptUploadError{ResumeOffset: 7}).Error(); !strings.Contains(got, "7") {
		t.Errorf("message: %q", got)
	}
	if chunksBefore(nil, 5) != nil || len(chunksBefore([]chunk{{Offset: 0}, {Offset: 9}}, 5)) != 1 {
		t.Error("chunksBefore")
	}
}

// ---------------------------------------------------------------- 7z (through a fake binary)

func fake7z(files map[string][]byte, failOn string, calls *[]string) unpack.Exec {
	return func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, name+" "+args[0])
		switch {
		case args[0] == failOn:
			return []byte("it went wrong"), errors.New("exit status 2")
		case args[0] == "l":
			var b strings.Builder
			b.WriteString("----------\n")
			for rel, data := range files {
				fmt.Fprintf(&b, "Path = %s\nSize = %d\nAttributes = A\n\n", rel, len(data))
			}
			return []byte(b.String()), nil
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

func TestSevenZipUpload(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "Packed")
	var calls []string
	m.o.Exec = fake7z(map[string][]byte{"Packed/01.mp3": song}, "", &calls)
	payload := []byte("pretend this is a 7z archive")
	send(t, m, rel, "packed.7z", payload, 10)
	if st := finish(t, m, rel, "packed.7z", len(payload)); st.State != Done || st.Added != 1 {
		t.Fatalf("status: %+v", st)
	}
	if strings.Join(calls, ",") != "7z t,7z l,7z x" {
		t.Errorf("expected an integrity test then an extraction, got %v", calls)
	}
	if files := listTree(t, filepath.Join(root, "uploads", "Packed")); len(files) != 1 || files[0] != "01.mp3" {
		t.Errorf("files: %v", files)
	}
}

func TestSevenZipFailures(t *testing.T) {
	payload := []byte("pretend this is a 7z archive")
	for failOn, want := range map[string]string{"t": "source file itself is damaged", "l": "extracting a.7z: 7z listing failed", "x": "7z extraction failed"} {
		m, _ := newMgr(t)
		rel := start(t, m, "x")
		var calls []string
		m.o.Exec = fake7z(nil, failOn, &calls)
		send(t, m, rel, "a.7z", payload, 100)
		if st := finish(t, m, rel, "a.7z", len(payload)); st.State != Failed || !strings.Contains(st.Error, want) {
			t.Errorf("fail on %s: %+v", failOn, st)
		}
	}
}

// ---------------------------------------------------------------- live progress

func TestStatusOfReportsLiveExtractionProgress(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	archive := zipOf(t, map[string][]byte{"a.mp3": song})
	send(t, m, rel, "a.zip", archive, 4096)

	m.extractMu.Lock() // hold the extractor so the background job stays "running"
	st, err := m.Complete(rel, "a.zip", int64(len(archive)))
	if err != nil || st.State != Running || st.TotalBytes != int64(len(archive)) {
		m.extractMu.Unlock()
		t.Fatalf("complete: %+v %v", st, err)
	}
	clean, dest, _ := m.verifyDest(rel)
	key, _ := m.sessionPaths(clean, "a.zip")
	scratch := m.extractDir(dest, key)
	os.MkdirAll(scratch, 0o755)
	os.WriteFile(filepath.Join(scratch, "half.bin"), bytes.Repeat([]byte{1}, 77), 0o644)

	live, found := m.StatusOf(rel, "a.zip")
	if !found || live.State != Running || live.BytesWritten != 77 {
		m.extractMu.Unlock()
		t.Fatalf("live status: %+v %v", live, found)
	}
	// If the destination disappears mid-run the status simply has no live figure.
	os.RemoveAll(dest)
	if gone, _ := m.StatusOf(rel, "a.zip"); gone.State != Running || gone.BytesWritten != 0 {
		t.Errorf("destination gone: %+v", gone)
	}
	m.extractMu.Unlock()
	if final := awaitFinished(t, m, rel, "a.zip"); final.State == Running {
		t.Error("should finish once the extractor is free")
	}
}

// ---------------------------------------------------------------- merging

func TestReuploadingAnArchiveIntoItsFolderAddsOnlyWhatIsNew(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "Album")
	first := zipOf(t, map[string][]byte{"d1/a.mp3": song, "d1/b.mp3": song, "cover.jpg": []byte("jpeg")})
	send(t, m, rel, "one.zip", first, 4096)
	if st := finish(t, m, rel, "one.zip", len(first)); st.Added != 3 || st.Duplicates != 0 {
		t.Fatalf("first: %+v", st)
	}

	_, rel2, err := m.StartOrJoin("Album")
	if err != nil || rel2 != rel {
		t.Fatal(rel2, err)
	}
	second := zipOf(t, map[string][]byte{
		"d1/a.mp3":  song,       // identical: skipped
		"d1/b.mp3":  variant(2), // same name, other bytes: kept beside it
		"d1/c.mp3":  song,       // new
		"d2/x.mp3":  song,       // new
		"cover.jpg": []byte("jpeg"),
	})
	send(t, m, rel2, "two.zip", second, 4096)
	st := finish(t, m, rel2, "two.zip", len(second))
	if st.State != Done || st.Added != 3 || st.Duplicates != 2 || st.Path != rel {
		t.Fatalf("second: %+v", st)
	}
	if got := strings.Join(listTree(t, filepath.Join(root, "uploads", "Album")), ","); got != "cover.jpg,d1/a.mp3,d1/b (2).mp3,d1/b.mp3,d1/c.mp3,d2/x.mp3" {
		t.Errorf("tree: %s", got)
	}
}

func TestALoneFolderThatAlreadyExistsIsMergedNotUnwrapped(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "Album")
	first := zipOf(t, map[string][]byte{"d1/a.mp3": song, "d2/b.mp3": song})
	send(t, m, rel, "one.zip", first, 4096)
	finish(t, m, rel, "one.zip", len(first))

	// Only d1 has something new: it must still land in d1, not at the top of Album.
	second := zipOf(t, map[string][]byte{"d1/new.mp3": variant(4)})
	send(t, m, rel, "two.zip", second, 4096)
	if st := finish(t, m, rel, "two.zip", len(second)); st.Added != 1 {
		t.Fatalf("second: %+v", st)
	}
	if got := strings.Join(listTree(t, filepath.Join(root, "uploads", "Album")), ","); got != "d1/a.mp3,d1/new.mp3,d2/b.mp3" {
		t.Errorf("tree: %s", got)
	}
}

func TestMergeKeepsClashingNamesApart(t *testing.T) {
	m, _ := newMgr(t, func(o *Options) { o.Classify = func(string) string { return "f" } })
	dst, src := t.TempDir(), t.TempDir()
	put := func(base, rel string, data []byte) {
		os.MkdirAll(filepath.Join(base, filepath.Dir(rel)), 0o755)
		os.WriteFile(filepath.Join(base, rel), data, 0o644)
	}
	put(dst, "x", []byte("a file"))
	put(dst, "dir/f.mp3", song)
	put(dst, "same.mp3", song)
	put(src, "x/inner.mp3", song) // a folder where the destination has a file
	put(src, "dir", song)         // a file where the destination has a folder
	put(src, "same.mp3", song)
	put(src, "new.mp3", song)

	var st tally
	if err := m.merge(src, dst, &st); err != nil {
		t.Fatal(err)
	}
	if st.Added != 3 || st.Duplicates != 1 || !reflect.DeepEqual(st.Kinds, map[string]int{"f": 3}) {
		t.Errorf("tally: %+v", st)
	}
	if got := strings.Join(listTree(t, dst), ","); got != "dir/f.mp3,dir (2),new.mp3,same.mp3,x,x (2)/inner.mp3" {
		t.Errorf("tree: %s", got)
	}

	// Errors are reported, not swallowed.
	if err := m.merge(filepath.Join(src, "missing"), dst, &st); err == nil {
		t.Error("an unreadable source is an error")
	}
	put(src, "again.mp3", song)
	m.o.Rename = func(string, string) error { return errors.New("disk on fire") }
	if err := m.merge(src, dst, &st); err == nil || !strings.Contains(err.Error(), "adding") {
		t.Errorf("a failed move: %v", err)
	}
	// ... also when it happens inside a folder that is being merged into.
	src2 := t.TempDir()
	put(src2, "dir/deeper.mp3", song)
	if err := m.merge(src2, dst, &st); err == nil || !strings.Contains(err.Error(), "adding") {
		t.Errorf("a failed move in a merged folder: %v", err)
	}
}

// ---------------------------------------------------------------- reports

func TestTypeOfNamesWhatWasSkipped(t *testing.T) {
	for in, want := range map[string]string{
		"a/b/Cover.JPG": "jpg", "notes.txt": "txt", "Makefile": "no extension", "a.b/readme": "no extension",
		"weird.this-is-not-an-extension": "other", "x.ünï": "other", "archive.tar.gz": "gz", ".hidden": "hidden",
	} {
		if got := typeOf(in); got != want {
			t.Errorf("typeOf(%q) = %q, want %q", in, got, want)
		}
	}
	got := countTypes([]string{"a.jpg", "b.JPG", "c.txt", "d"})
	if !reflect.DeepEqual(got, map[string]int{"jpg": 2, "txt": 1, "no extension": 1}) {
		t.Errorf("counts: %v", got)
	}
}

func TestReportErrorsAndPurging(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "R")
	if _, err := m.Report(rel, "none.zip"); !errors.Is(err, ErrNoReport) {
		t.Errorf("no report yet: %v", err)
	}
	if _, err := m.Report(rel, "../x"); !errors.Is(err, ErrBadName) {
		t.Errorf("bad name: %v", err)
	}

	// A long list is capped, and says so.
	many := make([]string, maxReportLines+5)
	for i := range many {
		many[i] = fmt.Sprintf("f%06d.txt", i)
	}
	key := m.sessionKey(rel, "big.zip")
	if err := m.writeReport(key, "big.zip", many); err != nil {
		t.Fatal(err)
	}
	p, err := m.Report(rel, "big.zip")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(data), fmt.Sprintf("%d files from big.zip were left out.\n\n", len(many))) ||
		!strings.HasSuffix(string(data), "... and 5 more\n") || strings.Contains(string(data), "f020004.txt") {
		t.Errorf("report without a note, capped: %q ... %q", data[:60], data[len(data)-60:])
	}

	// Reports follow the janitor: fresh ones stay, old ones go.
	m.PurgeExpired(time.Hour)
	if _, err := m.Report(rel, "big.zip"); err != nil {
		t.Errorf("a fresh report must survive: %v", err)
	}
	backdate(t, p, 72*time.Hour)
	m.PurgeExpired(time.Hour)
	if _, err := m.Report(rel, "big.zip"); !errors.Is(err, ErrNoReport) {
		t.Errorf("an old report must be purged: %v", err)
	}
	// A missing reports folder is fine, and a blocked one is reported.
	m.purgeReports(0)
	os.RemoveAll(filepath.Join(root, "uploads", StagingDirName, "reports"))
	os.WriteFile(filepath.Join(root, "uploads", StagingDirName, "reports"), []byte("a file, not a folder"), 0o644)
	if err := m.writeReport(key, "big.zip", []string{"x"}); err == nil {
		t.Error("an unwritable reports folder must be an error")
	}
}

// ---------------------------------------------------------------- housekeeping

func backdate(t *testing.T, path string, age time.Duration) {
	t.Helper()
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeExpiredRemovesOnlyAbandonedSessions(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "x")
	m.Begin(rel, "old.mp3", 10)
	m.Begin(rel, "fresh.mp3", 10)
	oldPartial, oldMeta := m.sessionPaths(rel, "old.mp3")
	freshPartial, freshMeta := m.sessionPaths(rel, "fresh.mp3")
	backdate(t, oldPartial, 72*time.Hour)
	backdate(t, oldMeta, 72*time.Hour)
	m.setStatus(oldPartial, Status{State: Corrupted})

	// An orphaned partial from a crash, a stray temp sidecar and an unrelated file.
	orphan := filepath.Join(root, "uploads", StagingDirName, strings.Repeat("a", 64)+".partial")
	os.WriteFile(orphan, []byte("x"), 0o644)
	backdate(t, orphan, 72*time.Hour)
	tmp := filepath.Join(root, "uploads", StagingDirName, strings.Repeat("b", 64)+".json.tmp")
	os.WriteFile(tmp, []byte("x"), 0o644)
	unrelated := filepath.Join(root, "uploads", StagingDirName, "notes.txt")
	os.WriteFile(unrelated, []byte("x"), 0o644)

	n, err := m.PurgeExpired(48 * time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("purged %d (%v), want the old session and the orphan", n, err)
	}
	if exists(oldPartial) || exists(oldMeta) || exists(orphan) {
		t.Error("abandoned files must be gone")
	}
	if !exists(freshPartial) || !exists(freshMeta) || !exists(unrelated) {
		t.Error("recent sessions and unrelated files must stay")
	}
	if _, found := m.StatusOf(rel, "old.mp3"); found {
		t.Error("the tracked outcome of a purged session must be dropped too")
	}
	// maxAge <= 0 purges everything, including the fresh session.
	if n, _ = m.PurgeExpired(0); n != 1 || exists(freshPartial) {
		t.Errorf("force purge: %d", n)
	}
}

func TestPurgeExpiredOnMissingOrBrokenStagingFolder(t *testing.T) {
	m, root := newMgr(t)
	if n, err := m.PurgeExpired(time.Hour); n != 0 || err != nil {
		t.Errorf("nothing staged yet: %d %v", n, err)
	}
	os.MkdirAll(filepath.Join(root, "uploads"), 0o755)
	os.WriteFile(filepath.Join(root, "uploads", StagingDirName), []byte("x"), 0o644) // a file where the folder should be
	if _, err := m.PurgeExpired(time.Hour); err == nil {
		t.Error("an unreadable staging folder is an error")
	}
}

func TestJanitorSweepsAndStops(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	m, _ := newMgr(t, func(o *Options) {
		o.Logf = func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }
	})
	rel := start(t, m, "x")
	m.Begin(rel, "abandoned.mp3", 10)
	p, meta := m.sessionPaths(rel, "abandoned.mp3")
	backdate(t, p, 72*time.Hour)
	backdate(t, meta, 72*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.RunJanitor(ctx, 48*time.Hour, 5*time.Millisecond); close(done) }()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(logs) > 0 })
	time.Sleep(40 * time.Millisecond) // a few ticks with nothing left to purge
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("janitor did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(logs[0], "purged 1") || len(logs) != 1 {
		t.Errorf("logs: %v", logs)
	}
	if exists(p) {
		t.Error("the abandoned session should be gone")
	}
}

func TestJanitorReportsErrors(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	m, root := newMgr(t, func(o *Options) {
		o.Logf = func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }
	})
	os.MkdirAll(filepath.Join(root, "uploads"), 0o755)
	os.WriteFile(filepath.Join(root, "uploads", StagingDirName), []byte("x"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.RunJanitor(ctx, time.Hour, time.Hour); close(done) }()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(logs) > 0 })
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(logs[0], "upload janitor:") {
		t.Errorf("logs: %v", logs)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestPlatformFreeBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := platformFreeBytes("."); err == nil {
			t.Error("not implemented on Windows")
		}
		return
	}
	if n, err := platformFreeBytes(t.TempDir()); err != nil || n == 0 {
		t.Errorf("free bytes: %d %v", n, err)
	}
	if _, err := platformFreeBytes(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing path is an error")
	}
}

func TestFormatSize(t *testing.T) {
	if got := fmtSize(3 << 30); got != "3.00 GB" {
		t.Errorf("%s", got)
	}
	if got := fmtSize(5 << 20); got != "5 MB" {
		t.Errorf("%s", got)
	}
}
