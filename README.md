# media-kit

Reusable pieces pulled out of Master Music Player. Nothing to install: the browser modules are
plain ES modules with no dependencies and no build step, and the Go packages use only the
standard library.

| Piece | Where | What it does |
| --- | --- | --- |
| Visualizer | `js/viz/` | Canvas audio visualizer: a core plus mode plugins (`bars`, `scope`, or your own) |
| Upload client | `js/upload.js` | Chunked, resumable, CRC32-checked uploads of any size, with archive progress |
| Upload server | `resumable/` (Go) | The server half of that protocol: sessions, resume, corrupt-chunk recovery, merging without overwriting |
| Archives | `unpack/` (Go) | Safe `.zip` / `.7z` verify and extract: no Zip Slip, no links, no bombs |
| Fuzzy search | `js/fuzzy.js` | Typo-tolerant multi-word search with match positions for highlighting |
| Virtual list | `js/virtual-list.js` | Fixed-row-height list that keeps only the visible rows in the DOM |

## Using it in a project

**Browser.** Copy `js/` into the project's static files (or `npm install` this repo by its git
URL and import `media-kit/viz`, `media-kit/upload` and so on), then import the modules directly:

```js
import { Visualizer, bars, scope } from './lib/media-kit/viz/index.js';
```

**Go.** `go get github.com/czyrustuazon/lib-szmy-media-kit` once the repo is pushed. To build without
network access, keep a copy in the project and point at it:

```
require github.com/czyrustuazon/lib-szmy-media-kit v0.0.0
replace github.com/czyrustuazon/lib-szmy-media-kit => ./third_party/media-kit
```

Master Music Player does exactly that with `scripts/sync-media-kit.mjs`, which copies the Go
packages and the browser modules (not the tests) into the player.

## Visualizer

```js
const ctx = new AudioContext();
const analyser = ctx.createAnalyser();
analyser.fftSize = 2048;
source.connect(analyser);

const viz = new Visualizer(canvas, analyser, { modes: [bars(), scope()] });
viz.start();              // draws every animation frame
viz.setMode('scope');     // any registered mode, or 'off' (stops the loop)
viz.stop();               // stop drawing, e.g. while the page is hidden
```

The canvas is sized to its CSS box at the device pixel ratio, so give it a size in CSS. The
colour is the canvas's `--viz` custom property (re-read every frame, so theme changes apply
live), or pass `color: '#f80'` or `color: () => someColour()`.

The built-in modes take options: `bars({ bands: 32, minHz: 60, maxHz: 16000 })`,
`scope({ gain: 2, lineWidth: 1 })`. Their maths is exported for testing or reuse: `bandEdges`,
`bandLevels`, `step`, `scopePoints`.

### Writing a mode

A mode is an object with a `name` and a `create()` that returns a renderer. `create` runs once,
the first time the mode is shown; keep the mode's state in its closure.

```js
const dots = {
  name: 'dots',
  create({ analyser, sampleRate }) {
    const freq = new Uint8Array(analyser.frequencyBinCount);
    return {
      draw({ g, width, height, dt, dpr, color }) {
        analyser.getByteFrequencyData(freq);
        for (let x = 0; x < width; x += 8 * dpr) {
          const v = freq[Math.floor((x / width) * freq.length)] / 255;
          g.fillRect(x, height - v * height, 3 * dpr, 3 * dpr);
        }
      },
      reset() {}, // optional: called when the loop stops
    };
  },
};
viz.register(dots).setMode('dots');
```

`g` is the canvas 2D context, already cleared, with `fillStyle` and `strokeStyle` set to the
colour. Sizes are in device pixels; `dt` is milliseconds since the last frame (at most 100).
`viz.modes` lists the names, with `'off'` last, which makes a settings menu easy.

## Resumable uploads

The browser sends each file in 16 MiB chunks, each with a CRC32. The server accepts a chunk only
at the offset it already has on disk, so any failure (a dropped connection, a reload, two tabs) is
recovered by asking for the real offset and carrying on. `.zip` and `.7z` files are verified and
unpacked in the background while the browser polls for progress. If an archive fails its check,
the server re-checks every chunk it stored. If one has changed since it arrived, only the data from
that chunk onwards is sent again. If none has, the source file itself is damaged and the upload
fails.

### Browser

```js
import { createUploader } from './lib/media-kit/upload.js';

const uploader = createUploader({
  baseUrl: '/api/upload',                        // where the endpoints below live
  headers: { 'X-Requested-With': 'my-app' },     // sent with every request
  storageKey: 'my-app-upload-batch',             // one per app
});

const results = await uploader.uploadBatch(files, {
  title: 'Holiday photos',  // a new folder for this batch ('' = the upload folder itself)
  merge: false,             // true: add to an existing folder of that name instead
  onProgress: ({ fraction, file }) => bar.value = fraction,
  onStatus: (text) => label.textContent = text,
});
// one result per file: { name, size, state: 'done' | 'failed' | 'corrupted', added, duplicates, error, ... }
```

`uploadBatch` rejects only when the connection is truly gone, or the user is signed out. The batch
is remembered in `localStorage`, and choosing the same files again (a reload cannot reopen files)
resumes it in the same folder: `uploader.pending()` tells you there is one to resume. A per-file
refusal (too large, no space, wrong type) is recorded in the results and the batch goes on.

### Server (Go)

```go
m := resumable.New(resumable.Options{
	Root:     "/srv/files",   // every relPath is relative to this
	Dir:      "uploads",      // uploads may land only in here ("" = anywhere below Root)
	MaxBytes: 10 << 30,       // per file, and per archive once unpacked
	MinFree:  1 << 30,        // keep this much disk free (0 = no check)

	// All optional:
	Accept:   func(path, filename string) error { ... },     // vet a finished loose file
	Prune:    func(dir string) ([]string, error) { ... },    // reduce an unpacked archive
	Classify: func(path string) string { ... },              // counts in Status.Kinds
})
go m.RunJanitor(ctx, 48*time.Hour, time.Hour) // clean up uploads nobody resumed
```

Without `Prune` an archive keeps every regular file. `unpack.Prune(dir, keep)` is the usual
building block for writing one: it drops links, every file `keep` refuses, and the folders that
end up empty. Use `SevenZipAvailable()` to tell the browser whether `.7z` works (it needs the
`7z` binary; `.zip` is pure Go).

Wire the endpoints to the Manager. The bodies are JSON unless noted.

| Endpoint | Body or query | Answer | Manager |
| --- | --- | --- | --- |
| `POST start` | `{title, merge?}` | `{name, relPath}` | `Start(title)`, or `StartOrJoin` when `merge` |
| `POST begin` | `{relPath, filename, size}` | `{offset}` | `Begin` |
| `POST chunk` | `?relPath=&filename=&offset=`, header `X-Chunk-CRC32`, raw body | `{offset}` | `Append`; on `*OffsetMismatchError` answer **409** `{offset: e.Current}` |
| `POST complete` | `{relPath, filename, size}` | `Status` | `Complete` |
| `GET status` | `?relPath=&filename=` | `Status`, or 404 if not tracked | `StatusOf` |
| `GET report` | `?relPath=&filename=` | text: what an archive left out | `Report` returns the file's path |

Limit the chunk body with `http.MaxBytesReader(w, r.Body, resumable.MaxChunkBytes)`. Map errors
to status codes with `errors.Is`: `ErrTooLarge` 413, `ErrNoSpace` 507, `ErrUnsupported` 415,
`ErrNoDest` and `ErrNoReport` 404, `*ChecksumMismatchError` and the other `Err*` values 400. The
client treats 400, 404, 413, 415 and 507 as a final answer for that one file. It retries anything
else. `internal/api/upload.go` in Master Music Player is a complete example.

## Fuzzy search

```js
import { createIndex, searchIndex } from './lib/media-kit/fuzzy.js';

const index = createIndex(tracks, (t) => `${t.title} · ${t.album}`); // normalise once
const hits = searchIndex(index, 'fnal fantsy', 50);                   // [{ item, score, marks }]
```

Every word of the query must match: as a substring, as letters in order (`fnlfntsy`), or with a
typo (one slip in a 4–7 letter word, two in longer words). It ignores case and accents. `marks`
are character positions in the original text, ready for highlighting.

## Virtual list

```js
import { VirtualList } from './lib/media-kit/virtual-list.js';

const list = new VirtualList(scroller, 56, (item, i) => renderRow(item, i)); // row height in px
list.setItems(items);
list.refresh();          // re-render the visible rows (selection changed)
list.scrollToIndex(42);  // centre a row
```

The scroller needs a fixed height and `overflow: auto`. The list sets each row's `top` and
`height`; the CSS is yours: `.vl-inner { position: relative }` and
`position: absolute; left: 0; right: 0` on the rows.

## Tests

```sh
npm test                   # browser modules, with Node 20+ (npm run cover for coverage)
sh scripts/coverage.sh     # Go: go test with a 100% statement-coverage gate (COVER_MIN to change)
```
