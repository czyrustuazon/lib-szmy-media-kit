package unpack

import (
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DecodeEscapes turns the "#Uxxxx" and "#Lxxxxxx" escapes that Info-ZIP's unzip writes for
// characters it cannot show in a non-UTF-8 locale back into those characters, so a name that
// went through such an unzip (and was then packed again) reads as it was meant to:
// "#U3010#U30aa#U30ea#U3011" becomes "【オリ】". Only escapes of non-ASCII characters are
// decoded, as unzip never escapes ASCII, so "#U0041" and anything malformed are left alone.
func DecodeEscapes(name string) string {
	if !strings.Contains(name, "#U") && !strings.Contains(name, "#L") {
		return name
	}
	var b strings.Builder
	for i := 0; i < len(name); {
		if r, n := escapeAt(name[i:]); n > 0 {
			b.WriteRune(r)
			i += n
			continue
		}
		b.WriteByte(name[i])
		i++
	}
	return b.String()
}

// escapeAt decodes an escape at the start of s, returning the character and the escape's
// length, or 0 when s does not start with one.
func escapeAt(s string) (rune, int) {
	if len(s) < 2 || s[0] != '#' {
		return 0, 0
	}
	n := map[byte]int{'U': 4, 'L': 6}[s[1]]
	if n == 0 || len(s) < 2+n {
		return 0, 0
	}
	v, err := strconv.ParseUint(s[2:2+n], 16, 32)
	if r := rune(v); err == nil && r >= 0x80 && utf8.ValidRune(r) {
		return r, 2 + n
	}
	return 0, 0
}

// NameFix finds, and unless DryRun is set renames, everything below a folder whose name carries
// unzip escapes (see DecodeEscapes). It is best effort: an entry whose decoded name is already
// taken, or that cannot be renamed, keeps the name it has.
type NameFix struct {
	DryRun bool // only report what would be renamed
	// Skip leaves an entry (and, for a folder, everything in it) alone. rel is its path below
	// the folder, slash-separated. Nil skips nothing.
	Skip func(rel string, isDir bool) bool
}

// Run fixes the names below dir. It returns, for every file whose path changed (by its own
// rename or a folder's above it), its old path and its new one, slash-separated and relative
// to dir.
func (o NameFix) Run(dir string) map[string]string {
	moves := map[string]string{}
	o.walk(dir, "", "", moves)
	return moves
}

// FixNames renames everything below dir that carries unzip escapes and reports the moves (see
// NameFix).
func FixNames(dir string) map[string]string { return NameFix{}.Run(dir) }

// renameFile is os.Rename, swapped out by tests (a real rename rarely fails here).
var renameFile = os.Rename

// walk fixes the entries of abs, which used to be at oldRel and is now at newRel.
func (o NameFix) walk(abs, oldRel, newRel string, moves map[string]string) {
	entries, _ := os.ReadDir(abs)
	taken := map[string]bool{} // never rename onto a name that is there, or handed out already
	for _, e := range entries {
		taken[e.Name()] = true
	}
	for _, e := range entries {
		name, from := e.Name(), path.Join(oldRel, e.Name())
		if o.Skip != nil && o.Skip(from, e.IsDir()) {
			continue
		}
		onDisk := filepath.Join(abs, name)
		if fixed := DecodeEscapes(name); fixed != name && !taken[fixed] {
			if o.DryRun {
				taken[fixed], name = true, fixed
			} else if renameFile(onDisk, filepath.Join(abs, fixed)) == nil {
				taken[fixed], name, onDisk = true, fixed, filepath.Join(abs, fixed)
			}
		}
		to := path.Join(newRel, name)
		if e.IsDir() {
			o.walk(onDisk, from, to, moves)
		} else if from != to {
			moves[from] = to
		}
	}
}
