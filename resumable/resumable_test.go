package resumable

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------- helpers

var song = append([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"), bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0}, 16)...)

func crc(b []byte) string { return fmt.Sprintf("%08x", crc32.ChecksumIEEE(b)) }

func newMgr(t *testing.T, tweak ...func(*Options)) (*Manager, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := Options{
		Root: root, Dir: "uploads", MaxBytes: 1 << 30,
		Logf:     func(string, ...any) {},
		LookPath: func(string) (string, error) { return "/usr/bin/7z", nil },
	}
	for _, f := range tweak {
		f(&o)
	}
	return New(o), root
}

func start(t *testing.T, m *Manager, title string) string {
	t.Helper()
	_, rel, err := m.Start(title)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

// send uploads data in chunks of the given size and returns the final offset.
func send(t *testing.T, m *Manager, rel, name string, data []byte, chunk int) int64 {
	t.Helper()
	off, err := m.Begin(rel, name, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for off < int64(len(data)) {
		part := data[off:min(off+int64(chunk), int64(len(data)))]
		if off, err = m.Append(rel, name, off, crc(part), part); err != nil {
			t.Fatal(err)
		}
	}
	return off
}

func awaitFinished(t *testing.T, m *Manager, rel, name string) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, found := m.StatusOf(rel, name)
		if !found {
			t.Fatal("completion is not tracked")
		}
		if st.State != Running {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("completion did not finish")
	return Status{}
}

// finish completes a file and waits for the outcome, whatever the file type.
func finish(t *testing.T, m *Manager, rel, name string, size int) Status {
	t.Helper()
	st, err := m.Complete(rel, name, int64(size))
	if err != nil {
		t.Fatal(err)
	}
	if st.State == Running {
		return awaitFinished(t, m, rel, name)
	}
	return st
}

func zipOf(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, data := range entries {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store}) // stored: easy to corrupt predictably
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

func variant(i byte) []byte { return append(append([]byte{}, song...), 0xFF, 0xFB, 0x90, i) }

// ---------------------------------------------------------------- naming

func TestSanitizeFolderName(t *testing.T) {
	cases := map[string]string{
		"Album":               "Album",
		"  My   Album  ":      "My Album",
		`a/b\c:d*e?f"g<h>i|j`: "a b c d e f g h i j",
		"tab\there\x00":       "tabhere",
		"...":                 "upload",
		"":                    "upload",
		"   ":                 "upload",
		"Trailing dots...":    "Trailing dots",
		"日本語のアルバム":            "日本語のアルバム",
	}
	for in, want := range cases {
		if got := SanitizeFolderName(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	long := SanitizeFolderName(strings.Repeat("あ", 100)) // 300 bytes of multi-byte characters
	if len(long) > 180 || !strings.HasPrefix(long, "あ") || strings.ContainsRune(long, '\uFFFD') {
		t.Errorf("long unicode name must be cut on a character boundary: %d bytes", len(long))
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"song.mp3":               "song.mp3",
		"../../etc/passwd":       "passwd",
		`C:\Users\me\track.flac`: "track.flac",
		`a<b>c:d"e|f?g*h.mp3`:    "a_b_c_d_e_f_g_h.mp3",
		"  spaced.mp3  ":         "spaced.mp3",
		".hidden.mp3":            "hidden.mp3",
		"tab\tname\x00.wav":      "tabname.wav",
		"":                       "",
		"...":                    "",
		"dir/":                   "",
		"日本語.brstm":             "日本語.brstm",
	}
	for in, want := range cases {
		if got := SanitizeFileName(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	long := SanitizeFileName(strings.Repeat("a", 300) + ".mp3")
	if len(long) != 200 || !strings.HasSuffix(long, ".mp3") {
		t.Errorf("long name: %d bytes", len(long))
	}
	if got := SanitizeFileName("x." + strings.Repeat("e", 300)); len(got) != 200 {
		t.Errorf("long extension: %d bytes", len(got))
	}
}

func TestUniqueDestinationAndUniqueFile(t *testing.T) {
	dir := t.TempDir()
	if got := UniqueDestination(dir, "A"); got != filepath.Join(dir, "A") {
		t.Errorf("free name: %s", got)
	}
	os.Mkdir(filepath.Join(dir, "A"), 0o755)
	os.Mkdir(filepath.Join(dir, "A (2)"), 0o755)
	if got := UniqueDestination(dir, "A"); got != filepath.Join(dir, "A (3)") {
		t.Errorf("taken names: %s", got)
	}
	os.WriteFile(filepath.Join(dir, "a.mp3"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "a (2).mp3"), nil, 0o644)
	if got := uniqueFile(dir, "a.mp3"); got != filepath.Join(dir, "a (3).mp3") {
		t.Errorf("got %s", got)
	}
	if got := uniqueFile(dir, "noext"); got != filepath.Join(dir, "noext") {
		t.Errorf("got %s", got)
	}
}

func TestCleanRelAndJoinRel(t *testing.T) {
	for in, want := range map[string]string{"": "", "/a/b/": "a/b", "../../x": "x", "a/../b": "b", "./a//b": "a/b"} {
		if got := cleanRel(in); got != want {
			t.Errorf("cleanRel(%q) = %q, want %q", in, got, want)
		}
	}
	if joinRel("", "x") != "x" || joinRel("a", "x") != "a/x" {
		t.Error("joinRel")
	}
}

// ---------------------------------------------------------------- set-up

func TestNewFillsInDefaults(t *testing.T) {
	m := New(Options{Root: filepath.Join(t.TempDir(), "missing"), MaxBytes: 1})
	if m.root != m.o.Root || m.o.FallbackName != "file" || m.o.Exec == nil || m.o.LookPath == nil ||
		m.o.FreeBytes == nil || m.o.Move == nil || m.o.Rename == nil || m.o.Logf == nil {
		t.Errorf("defaults: %+v", m.o)
	}
	if m.staging != filepath.Join(m.root, StagingDirName) {
		t.Errorf("staging: %s", m.staging)
	}
}

func TestStart(t *testing.T) {
	m, root := newMgr(t)
	name, rel, err := m.Start("My Album")
	if err != nil || name != "My Album" || rel != "uploads/My Album" || !exists(filepath.Join(root, "uploads", "My Album")) {
		t.Fatalf("start: %q %q %v", name, rel, err)
	}
	if name, rel, _ = m.Start("My Album"); name != "My Album (2)" || rel != "uploads/My Album (2)" {
		t.Errorf("a second batch must not reuse the folder: %q %q", name, rel)
	}
	if name, rel, err = m.Start("   "); err != nil || name != "" || rel != "uploads" {
		t.Errorf("blank title uses the upload folder itself: %q %q %v", name, rel, err)
	}

	m2, root2 := newMgr(t)
	os.WriteFile(filepath.Join(root2, "uploads"), []byte("x"), 0o644) // a file where the folder must go
	if _, _, err := m2.Start("x"); err == nil {
		t.Error("expected an error when the upload folder cannot be created")
	}
}

func TestStartOrJoinReusesAnExistingFolder(t *testing.T) {
	m, root := newMgr(t)
	name1, rel1, err := m.StartOrJoin("Album")
	if err != nil || name1 != "Album" || rel1 != "uploads/Album" {
		t.Fatalf("a new folder is created: %q %q %v", name1, rel1, err)
	}
	name2, rel2, err := m.StartOrJoin("Album")
	if err != nil || name2 != "Album" || rel2 != rel1 {
		t.Fatalf("the folder is reused, not renamed to (2): %q %q %v", name2, rel2, err)
	}
	if _, rel, _ := m.Start("Album"); rel != "uploads/Album (2)" {
		t.Errorf("plain Start still never reuses a folder: %s", rel)
	}
	if _, rel, err := m.StartOrJoin(" "); err != nil || rel != "uploads" {
		t.Errorf("a blank title is the upload folder: %q %v", rel, err)
	}
	// A link with that name is not a folder to join.
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "uploads", "Linked")); err == nil {
		if _, rel, _ := m.StartOrJoin("Linked"); rel != "uploads/Linked (2)" {
			t.Errorf("a symlink must not be joined: %s", rel)
		}
	}
	// A plain file with that name is not a folder either.
	os.WriteFile(filepath.Join(root, "uploads", "File"), []byte("x"), 0o644)
	if _, rel, _ := m.StartOrJoin("File"); rel != "uploads/File (2)" {
		t.Errorf("a file must not be joined: %s", rel)
	}
}

func TestUploadsAnywhereBelowRootWhenDirIsEmpty(t *testing.T) {
	m, root := newMgr(t, func(o *Options) { o.Dir = "" })
	if _, rel, err := m.Start(""); err != nil || rel != "" {
		t.Fatalf("blank title is the root itself: %q %v", rel, err)
	}
	rel := start(t, m, "A")
	if rel != "A" {
		t.Fatalf("rel %q", rel)
	}
	if _, rel2, _ := m.StartOrJoin("A"); rel2 != "A" {
		t.Errorf("join: %q", rel2)
	}
	send(t, m, "", "top.bin", []byte("top"), 10)
	if st := finish(t, m, "", "top.bin", 3); st.State != Done || st.Path != "top.bin" {
		t.Errorf("a file at the root: %+v", st)
	}
	send(t, m, rel, "x.bin", []byte("x"), 10)
	if st := finish(t, m, rel, "x.bin", 1); st.State != Done || st.Path != "A/x.bin" {
		t.Errorf("a file in a folder: %+v", st)
	}
	if !exists(filepath.Join(root, StagingDirName)) {
		t.Error("staging sits in the root")
	}
	if _, err := m.Begin(StagingDirName, "y.bin", 1); !errors.Is(err, ErrBadPath) {
		t.Errorf("the staging folder itself is off limits: %v", err)
	}
}

// ---------------------------------------------------------------- begin: validation

func TestBeginRejectsBadRequests(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "ok")
	os.WriteFile(filepath.Join(root, "uploads", "file.txt"), []byte("x"), 0o644)

	cases := []struct {
		name     string
		rel      string
		filename string
		size     int64
		want     error
	}{
		{"outside the upload folder", "music/other", "a.mp3", 1, ErrBadPath},
		{"the root", "", "a.mp3", 1, ErrBadPath},
		{"sibling with the same prefix", "uploads2/x", "a.mp3", 1, ErrBadPath},
		{"hidden folder", "uploads/.trash", "a.mp3", 1, ErrBadPath},
		{"missing folder", "uploads/nope", "a.mp3", 1, ErrNoDest},
		{"a file, not a folder", "uploads/file.txt", "a.mp3", 1, ErrNoDest},
		{"empty name", rel, "", 1, ErrBadName},
		{"name with slash", rel, "a/b.mp3", 1, ErrBadName},
		{"name with backslash", rel, `a\b.mp3`, 1, ErrBadName},
		{"dot-dot", rel, "..", 1, ErrBadName},
		{"dot", rel, ".", 1, ErrBadName},
		{"name with NUL", rel, "a\x00.mp3", 1, ErrBadName},
		{"very long name", rel, strings.Repeat("a", 300), 1, ErrBadName},
		{"negative size", rel, "a.mp3", -1, ErrBadSize},
		{"too large", rel, "a.mp3", 2 << 30, ErrTooLarge},
	}
	for _, c := range cases {
		if _, err := m.Begin(c.rel, c.filename, c.size); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

func TestBeginRefusesSymlinkOutOfTheRoot(t *testing.T) {
	m, root := newMgr(t)
	os.MkdirAll(filepath.Join(root, "uploads"), 0o755)
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "uploads", "escape")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := m.Begin("uploads/escape", "a.mp3", 1); !errors.Is(err, ErrBadPath) {
		t.Errorf("got %v", err)
	}
}

func TestBeginRefusesSevenZipWithoutTheBinary(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	m.o.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	if m.SevenZipAvailable() {
		t.Error("7z should be reported missing")
	}
	if _, err := m.Begin(rel, "a.7z", 10); !errors.Is(err, ErrUnsupported) {
		t.Errorf(".7z: %v", err)
	}
	if _, err := m.Begin(rel, "A.7Z", 10); !errors.Is(err, ErrUnsupported) {
		t.Errorf("case-insensitive: %v", err)
	}
	if _, err := m.Begin(rel, "a.zip", 10); err != nil {
		t.Errorf(".zip needs no external tool: %v", err)
	}
	// With a custom Extract, any archive but a zip needs 7z; a file it declines is just a file.
	m.o.Extract = func(name string) bool { return strings.HasSuffix(name, ".rar") }
	if _, err := m.Begin(rel, "a.rar", 10); !errors.Is(err, ErrUnsupported) {
		t.Errorf(".rar: %v", err)
	}
	if _, err := m.Begin(rel, "b.7z", 10); err != nil {
		t.Errorf("a .7z that is not to be unpacked: %v", err)
	}
	m.o.LookPath = func(string) (string, error) { return "/usr/bin/7z", nil }
	if !m.SevenZipAvailable() {
		t.Error("7z should be reported present")
	}
}

func TestBeginChecksFreeSpace(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	m.o.MinFree = 1000

	m.o.FreeBytes = func(string) (uint64, error) { return 5000, nil }
	if _, err := m.Begin(rel, "ok.mp3", 3000); err != nil {
		t.Errorf("enough space: %v", err)
	}
	if _, err := m.Begin(rel, "big.mp3", 4500); !errors.Is(err, ErrNoSpace) {
		t.Errorf("not enough space: %v", err)
	}
	m.o.FreeBytes = func(string) (uint64, error) { return 0, errors.New("statfs failed") }
	if _, err := m.Begin(rel, "unknown.mp3", 1<<20); err != nil {
		t.Errorf("an unreadable free-space figure must not block uploads: %v", err)
	}
	m.o.MinFree = 0
	m.o.FreeBytes = func(string) (uint64, error) { return 0, nil }
	if _, err := m.Begin(rel, "unchecked.mp3", 1<<20); err != nil {
		t.Errorf("no minimum means no check: %v", err)
	}
	// Big and small figures format sensibly in the message.
	m.o.MinFree = 1 << 20
	m.o.MaxBytes = 10 << 30
	m.o.FreeBytes = func(string) (uint64, error) { return 3 << 30, nil }
	_, err := m.Begin(rel, "huge.mp3", (3<<30)-100)
	if err == nil || !strings.Contains(err.Error(), "GB") || !strings.Contains(err.Error(), "MB") {
		t.Errorf("message should use GB and MB: %v", err)
	}
}

// ---------------------------------------------------------------- loose files

func TestChunkedUploadOfALooseFile(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "Singles")
	if off := send(t, m, rel, "My Song.mp3", song, 25); off != int64(len(song)) {
		t.Fatalf("offset %d", off)
	}
	st := finish(t, m, rel, "My Song.mp3", len(song))
	if st.State != Done || st.Added != 1 || st.Kinds != nil || st.Path != "uploads/Singles/My Song.mp3" || st.BytesWritten != int64(len(song)) {
		t.Fatalf("status: %+v", st)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "uploads", "Singles", "My Song.mp3")); !bytes.Equal(got, song) {
		t.Error("content mismatch")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "uploads", StagingDirName)); len(entries) != 0 {
		t.Errorf("staging should be empty after placing: %v", entries)
	}

	// Asking again, or reloading, returns the tracked outcome instead of redoing anything.
	again, err := m.Complete(rel, "My Song.mp3", int64(len(song)))
	if err != nil || !reflect.DeepEqual(again, st) {
		t.Errorf("idempotent complete: %+v %v", again, err)
	}
	if got, found := m.StatusOf(rel, "My Song.mp3"); !found || !reflect.DeepEqual(got, st) {
		t.Errorf("status: %+v %v", got, found)
	}
	// Starting the same file again discards the old verdict.
	if _, err := m.Begin(rel, "My Song.mp3", int64(len(song))); err != nil {
		t.Fatal(err)
	}
	if _, found := m.StatusOf(rel, "My Song.mp3"); found {
		t.Error("a new session must not inherit the old outcome")
	}
}

func TestAcceptAndClassifyHooks(t *testing.T) {
	var seen []string
	m, root := newMgr(t, func(o *Options) {
		o.Accept = func(path, filename string) error {
			seen = append(seen, filename)
			if data, _ := os.ReadFile(path); !bytes.HasPrefix(data, []byte("ID3")) {
				return errors.New("not an audio file")
			}
			return nil
		}
		o.Classify = func(path string) string { return "audio:" + filepath.Ext(path) }
	})
	rel := start(t, m, "x")
	send(t, m, rel, "notes.txt", []byte("definitely not audio"), 8)
	st := finish(t, m, rel, "notes.txt", 20)
	if st.State != Failed || st.Error != "notes.txt: not an audio file" {
		t.Fatalf("refused: %+v", st)
	}
	if exists(filepath.Join(root, "uploads", "x", "notes.txt")) {
		t.Error("a refused file must not be placed")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "uploads", StagingDirName)); len(entries) != 0 {
		t.Errorf("refused bytes must not linger: %v", entries)
	}
	send(t, m, rel, "a.mp3", song, 1000)
	if st := finish(t, m, rel, "a.mp3", len(song)); st.State != Done || !reflect.DeepEqual(st.Kinds, map[string]int{"audio:.mp3": 1}) {
		t.Errorf("accepted: %+v", st)
	}
	if strings.Join(seen, ",") != "notes.txt,a.mp3" {
		t.Errorf("Accept sees the name the file was sent as: %v", seen)
	}
}

func TestEquivalentFolderSpellingsShareASession(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "Same")
	send(t, m, rel, "a.mp3", song[:20], 10)
	off, err := m.Begin("/"+rel+"/", "a.mp3", 20)
	if err != nil || off != 20 {
		t.Errorf("a differently spelled folder must find the same session, got offset %d (%v)", off, err)
	}
}

func TestLooseCollisionGetsASuffixAndOddNamesAreCleaned(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "x")
	for i := byte(0); i < 2; i++ {
		data := variant(i) // same name, different bytes
		send(t, m, rel, "dup.mp3", data, 1000)
		if st := finish(t, m, rel, "dup.mp3", len(data)); st.State != Done {
			t.Fatalf("round %d: %+v", i, st)
		}
	}
	if files := listTree(t, filepath.Join(root, "uploads", "x")); strings.Join(files, ",") != "dup (2).mp3,dup.mp3" {
		t.Fatalf("expected both copies, got %v", files)
	}

	// Names that sanitise to nothing fall back to FallbackName.
	send(t, m, rel, "...", song, 1000)
	if st := finish(t, m, rel, "...", len(song)); st.State != Done || !strings.HasSuffix(st.Path, "/file") {
		t.Errorf("default fallback name: %+v", st)
	}
	m2, _ := newMgr(t, func(o *Options) { o.FallbackName = "track" })
	rel2 := start(t, m2, "x")
	send(t, m2, rel2, "...", song, 1000)
	if st := finish(t, m2, rel2, "...", len(song)); !strings.HasSuffix(st.Path, "/track") {
		t.Errorf("custom fallback name: %+v", st)
	}
}

func TestLooseDuplicateIsNotAddedTwice(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "Singles")
	send(t, m, rel, "song.mp3", song, 64)
	if st := finish(t, m, rel, "song.mp3", len(song)); st.State != Done || st.Added != 1 {
		t.Fatalf("first: %+v", st)
	}
	send(t, m, rel, "song.mp3", song, 64)
	st := finish(t, m, rel, "song.mp3", len(song))
	if st.State != Done || st.Added != 0 || st.Duplicates != 1 || st.Path != rel+"/song.mp3" {
		t.Fatalf("a repeat is reported as already there: %+v", st)
	}
	changed := variant(3)
	send(t, m, rel, "song.mp3", changed, 64)
	if st := finish(t, m, rel, "song.mp3", len(changed)); st.Added != 1 || st.Duplicates != 0 {
		t.Fatalf("different content: %+v", st)
	}
	if got := strings.Join(listTree(t, filepath.Join(root, "uploads", "Singles")), ","); got != "song (2).mp3,song.mp3" {
		t.Errorf("tree: %s", got)
	}
}

func TestPlacingFailureIsReported(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	m.o.Move = func(string, string) error { return errors.New("disk on fire") }
	send(t, m, rel, "a.mp3", song, 1000)
	st := finish(t, m, rel, "a.mp3", len(song))
	if st.State != Failed || !strings.Contains(st.Error, "disk on fire") {
		t.Fatalf("status: %+v", st)
	}
}

// ---------------------------------------------------------------- resume and offsets

func TestResumeFromTheLastConfirmedOffset(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	data := append([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"), bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0}, 30)...)

	off, err := m.Begin(rel, "big.mp3", int64(len(data)))
	if err != nil || off != 0 {
		t.Fatalf("fresh session: %d %v", off, err)
	}
	if off, err = m.Append(rel, "big.mp3", 0, crc(data[:50]), data[:50]); err != nil || off != 50 {
		t.Fatalf("first chunk: %d %v", off, err)
	}
	// A "reload": the client asks again and is told where to continue.
	if off, err = m.Begin(rel, "big.mp3", int64(len(data))); err != nil || off != 50 {
		t.Fatalf("resume: %d %v", off, err)
	}
	// A stale offset is refused with the true one.
	_, err = m.Append(rel, "big.mp3", 0, crc(data[:50]), data[:50])
	var mismatch *OffsetMismatchError
	if !errors.As(err, &mismatch) || mismatch.Current != 50 || !strings.Contains(err.Error(), "50") {
		t.Fatalf("expected an offset mismatch carrying 50, got %v", err)
	}
	if _, err = m.Append(rel, "big.mp3", 50, crc(data[50:]), data[50:]); err != nil {
		t.Fatal(err)
	}
	if st := finish(t, m, rel, "big.mp3", len(data)); st.State != Done {
		t.Fatalf("status: %+v", st)
	}
}

func TestADifferentFileWithTheSameNameStartsOver(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	m.Begin(rel, "a.mp3", 100)
	m.Append(rel, "a.mp3", 0, crc([]byte("abc")), []byte("abc"))
	if off, _ := m.Begin(rel, "a.mp3", 100); off != 3 {
		t.Fatalf("same size resumes: %d", off)
	}
	if off, err := m.Begin(rel, "a.mp3", 200); err != nil || off != 0 {
		t.Fatalf("a different size must restart at 0: %d %v", off, err)
	}
}

func TestAppendRejectsBadChunks(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	if _, err := m.Append(rel, "never.mp3", 0, crc(nil), nil); !errors.Is(err, ErrNoSession) {
		t.Errorf("no session: %v", err)
	}
	if _, err := m.Append(rel, "../x", 0, crc(nil), nil); !errors.Is(err, ErrBadName) {
		t.Errorf("bad name: %v", err)
	}
	m.Begin(rel, "a.mp3", 10)

	_, err := m.Append(rel, "a.mp3", 0, "deadbeef", []byte("abc"))
	var bad *ChecksumMismatchError
	if !errors.As(err, &bad) || !strings.Contains(err.Error(), "CRC32") {
		t.Fatalf("checksum: %v", err)
	}
	stagingPath, _ := m.sessionPaths(rel, "a.mp3")
	if info, _ := os.Stat(stagingPath); info.Size() != 0 {
		t.Error("a bad chunk must write nothing")
	}
	if _, err := m.Append(rel, "a.mp3", 0, "x", bytes.Repeat([]byte{1}, 11)); !errors.Is(err, ErrOverrun) {
		t.Errorf("overrun: %v", err)
	}
	// Upper-case hex is accepted.
	if off, err := m.Append(rel, "a.mp3", 0, strings.ToUpper(crc([]byte("abc"))), []byte("abc")); err != nil || off != 3 {
		t.Errorf("upper-case CRC: %d %v", off, err)
	}
	// A session whose staged bytes vanished must be restarted, not appended to.
	os.Remove(stagingPath)
	if _, err := m.Append(rel, "a.mp3", 3, crc([]byte("d")), []byte("d")); !errors.Is(err, ErrNoSession) {
		t.Errorf("missing staging: %v", err)
	}
}

func TestAppendReportsFileSystemFailures(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")

	// Cannot open the staged file for appending (a directory sits there).
	m.Begin(rel, "dir.mp3", 1<<20)
	stagingPath, _ := m.sessionPaths(rel, "dir.mp3")
	os.Remove(stagingPath)
	os.Mkdir(stagingPath, 0o755)
	info, _ := os.Stat(stagingPath)
	if _, err := m.Append(rel, "dir.mp3", info.Size(), crc([]byte("a")), []byte("a")); err == nil || !strings.Contains(err.Error(), "appending chunk") {
		t.Errorf("open failure: %v", err)
	}

	// The write itself fails (disk full), simulated with /dev/full.
	if _, err := os.Stat("/dev/full"); err == nil {
		m.Begin(rel, "full.mp3", 1<<20)
		p, _ := m.sessionPaths(rel, "full.mp3")
		os.Remove(p)
		if err := os.Symlink("/dev/full", p); err == nil {
			if _, err := m.Append(rel, "full.mp3", 0, crc([]byte("abc")), []byte("abc")); err == nil || !strings.Contains(err.Error(), "appending chunk") {
				t.Errorf("write failure: %v", err)
			}
		}
	}

	// Recording the chunk fails (a directory squats on the sidecar's temp name).
	m.Begin(rel, "meta.mp3", 1<<20)
	_, metaPath := m.sessionPaths(rel, "meta.mp3")
	os.Mkdir(metaPath+".tmp", 0o755)
	if _, err := m.Append(rel, "meta.mp3", 0, crc([]byte("abc")), []byte("abc")); err == nil || !strings.Contains(err.Error(), "recording chunk") {
		t.Errorf("sidecar failure: %v", err)
	}
}

func TestBeginReportsFileSystemFailures(t *testing.T) {
	// The staging folder cannot be created (a file sits there).
	m, root := newMgr(t)
	rel := start(t, m, "x")
	os.WriteFile(filepath.Join(root, "uploads", StagingDirName), []byte("x"), 0o644)
	if _, err := m.Begin(rel, "a.mp3", 1); err == nil || !strings.Contains(err.Error(), "staging") {
		t.Errorf("staging folder: %v", err)
	}

	// The staged file cannot be created (a directory sits there).
	m2, _ := newMgr(t)
	rel2 := start(t, m2, "x")
	p, _ := m2.sessionPaths(rel2, "a.mp3")
	os.MkdirAll(p, 0o755)
	if _, err := m2.Begin(rel2, "a.mp3", 1); err == nil || !strings.Contains(err.Error(), "creating upload session") {
		t.Errorf("staged file: %v", err)
	}

	// The sidecar cannot be written.
	m3, _ := newMgr(t)
	rel3 := start(t, m3, "x")
	_, metaPath := m3.sessionPaths(rel3, "a.mp3")
	os.MkdirAll(metaPath+".tmp", 0o755)
	if _, err := m3.Begin(rel3, "a.mp3", 1); err == nil || !strings.Contains(err.Error(), "creating upload session") {
		t.Errorf("sidecar: %v", err)
	}
}

// ---------------------------------------------------------------- complete: validation

func TestCompleteRejectsUnfinishedOrUnknownUploads(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	if _, err := m.Complete(rel, "../x", 1); !errors.Is(err, ErrBadName) {
		t.Errorf("bad name: %v", err)
	}
	if _, err := m.Complete("music/elsewhere", "a.mp3", 1); !errors.Is(err, ErrBadPath) {
		t.Errorf("bad folder: %v", err)
	}
	if _, err := m.Complete(rel, "never.mp3", 1); !errors.Is(err, ErrNoSession) {
		t.Errorf("no session: %v", err)
	}
	m.Begin(rel, "a.mp3", 100)
	if _, err := m.Complete(rel, "a.mp3", 99); !errors.Is(err, ErrNoSession) {
		t.Errorf("size that does not match the session: %v", err)
	}
	m.Append(rel, "a.mp3", 0, crc([]byte("abc")), []byte("abc"))
	_, err := m.Complete(rel, "a.mp3", 100)
	if !errors.Is(err, ErrIncomplete) || !strings.Contains(err.Error(), "3 of 100") {
		t.Errorf("incomplete: %v", err)
	}
	// Staged bytes that disappeared.
	p, _ := m.sessionPaths(rel, "a.mp3")
	os.Remove(p)
	if _, err := m.Complete(rel, "a.mp3", 100); !errors.Is(err, ErrNoSession) {
		t.Errorf("missing staging: %v", err)
	}
}

func TestBeginLeavesARunningExtractionAlone(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	p, _ := m.sessionPaths(rel, "a.zip")
	m.setStatus(p, Status{State: Running, TotalBytes: 5})
	if _, err := m.Begin(rel, "a.zip", 5); err != nil {
		t.Fatal(err)
	}
	if st, found := m.StatusOf(rel, "a.zip"); !found || st.State != Running {
		t.Errorf("a running extraction is real and must survive a re-probe: %+v %v", st, found)
	}
	m.setStatus(p, Status{State: Failed})
	m.Begin(rel, "a.zip", 5)
	if _, found := m.StatusOf(rel, "a.zip"); found {
		t.Error("a finished verdict must be cleared")
	}
}

func TestCompleteReturnsTheTrackedStatusWithoutRevalidating(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	p, _ := m.sessionPaths(rel, "gone.zip")
	want := Status{State: Done, TotalBytes: 9, BytesWritten: 9, Added: 3}
	m.setStatus(p, want)
	got, err := m.Complete(rel, "gone.zip", 9) // no session files exist at all
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v %v", got, err)
	}
}

func TestCorruptSidecarStartsTheSessionOver(t *testing.T) {
	m, _ := newMgr(t)
	rel := start(t, m, "x")
	m.Begin(rel, "a.mp3", 10)
	partial, metaPath := m.sessionPaths(rel, "a.mp3")
	m.Append(rel, "a.mp3", 0, crc([]byte("abc")), []byte("abc"))
	os.WriteFile(metaPath, []byte("{ this is not json"), 0o644)

	// Unreadable bookkeeping means the old bytes cannot be trusted: begin again from 0.
	if off, err := m.Begin(rel, "a.mp3", 10); err != nil || off != 0 {
		t.Fatalf("got %d %v", off, err)
	}
	if info, _ := os.Stat(partial); info.Size() != 0 {
		t.Errorf("the staged bytes must be discarded, size %d", info.Size())
	}
	// And Append/Complete treat the garbage sidecar as "no session".
	os.WriteFile(metaPath, []byte("garbage"), 0o644)
	if _, err := m.Append(rel, "a.mp3", 0, crc([]byte("a")), []byte("a")); !errors.Is(err, ErrNoSession) {
		t.Errorf("append: %v", err)
	}
	if _, err := m.Complete(rel, "a.mp3", 10); !errors.Is(err, ErrNoSession) {
		t.Errorf("complete: %v", err)
	}
}

func TestStatusOfUnknownFile(t *testing.T) {
	m, _ := newMgr(t)
	if _, found := m.StatusOf("uploads", "nothing.zip"); found {
		t.Error("nothing is tracked")
	}
}

func TestWritableChecksTheUploadFolderItself(t *testing.T) {
	m, root := newMgr(t)
	if !m.Writable() {
		t.Fatal("a fresh root has a writable upload folder")
	}
	if !exists(filepath.Join(root, "uploads")) {
		t.Error("checking creates the upload folder")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "uploads")); len(entries) != 0 {
		t.Errorf("the probe file must be removed: %v", entries)
	}

	blocked, rootB := newMgr(t)
	os.WriteFile(filepath.Join(rootB, "uploads"), []byte("x"), 0o644) // a file where the folder belongs
	if blocked.Writable() {
		t.Error("an unusable upload folder is not writable")
	}
}

func TestStagingLivesInsideTheUploadFolder(t *testing.T) {
	m, root := newMgr(t)
	rel := start(t, m, "x")
	if _, err := m.Begin(rel, "a.mp3", 10); err != nil {
		t.Fatal(err)
	}
	// On the same disk as the finished files, even when "uploads" is a separate mount.
	if !exists(filepath.Join(root, "uploads", StagingDirName)) {
		t.Error("staging must be inside the upload folder")
	}
	if exists(filepath.Join(root, StagingDirName)) {
		t.Error("nothing may be staged on the root's disk")
	}
}

// ---------------------------------------------------------------- files

func TestSameContent(t *testing.T) {
	dir := t.TempDir()
	big := bytes.Repeat([]byte("0123456789abcdef"), 10000) // more than one 64 KiB block
	exact := bytes.Repeat([]byte("x"), 64<<10)             // ends exactly on a block boundary
	altered := append([]byte{}, big...)
	altered[len(altered)-1] ^= 1
	earlier := append([]byte{}, big...)
	earlier[5] ^= 1
	for name, data := range map[string][]byte{
		"a": big, "b": big, "c": altered, "d": earlier, "e": big[:100], "x1": exact, "x2": exact, "empty1": nil, "empty2": nil,
	} {
		os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
	os.Mkdir(filepath.Join(dir, "folder"), 0o755)
	p := func(n string) string { return filepath.Join(dir, n) }
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"a", "b", true}, {"a", "c", false}, {"a", "d", false}, {"a", "e", false}, {"x1", "x2", true}, {"empty1", "empty2", true},
		{"a", "missing", false}, {"missing", "a", false}, {"folder", "a", false}, {"a", "folder", false},
	} {
		if got := SameContent(p(c.a), p(c.b)); got != c.want {
			t.Errorf("SameContent(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestMoveFile(t *testing.T) {
	dir := t.TempDir()
	p := func(n string) string { return filepath.Join(dir, n) }
	os.WriteFile(p("a"), []byte("hello"), 0o644)
	if err := MoveFile(p("a"), p("b")); err != nil || exists(p("a")) {
		t.Fatalf("rename: %v", err)
	}

	// When a rename is impossible, the file is copied and the original removed.
	old := rename
	rename = func(string, string) error { return errors.New("cross-device link") }
	t.Cleanup(func() { rename = old })
	if err := MoveFile(p("b"), p("c")); err != nil || exists(p("b")) {
		t.Fatalf("copy: %v", err)
	}
	if got, _ := os.ReadFile(p("c")); string(got) != "hello" {
		t.Errorf("copied %q", got)
	}
	if err := MoveFile(p("missing"), p("d")); err == nil {
		t.Error("a missing source is an error")
	}
	os.WriteFile(p("e"), []byte("x"), 0o644)
	if err := MoveFile(p("c"), p("e")); err == nil || !exists(p("c")) {
		t.Errorf("an existing destination is never overwritten: %v", err)
	}
	// A source that cannot be read (a folder): the half-made copy is removed.
	os.Mkdir(p("folder"), 0o755)
	if err := MoveFile(p("folder"), p("f")); err == nil || exists(p("f")) {
		t.Errorf("unreadable source: %v", err)
	}
}
