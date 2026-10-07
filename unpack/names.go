package unpack

import (
	"os"
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

// FixNames renames everything below dir whose name carries unzip escapes (see DecodeEscapes)
// to the decoded name. It is best effort: an entry whose decoded name is already taken, or
// that cannot be renamed, keeps the name it has.
func FixNames(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		from := filepath.Join(dir, e.Name())
		if e.IsDir() {
			FixNames(from)
		}
		fixed := DecodeEscapes(e.Name())
		if fixed == e.Name() {
			continue
		}
		to := filepath.Join(dir, fixed)
		if _, err := os.Lstat(to); err == nil {
			continue
		}
		os.Rename(from, to)
	}
}
