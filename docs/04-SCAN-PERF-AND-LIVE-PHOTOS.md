# 04 — Scan performance on network shares + Live Photo pairing

> **Branch**: `preview` • **Goal**: cut scan time ~2.7× on a HEIC-heavy SMB share, and make
> deleting a Live Photo still also delete its `.MOV` half.
> **Constraints**: pure Go, no CGo, no external binaries. No new dependency for Part B.
> **Estimated session size**: Part A is one session; Part B is a second, independent one.
> **Prerequisite**: none. Both parts apply to `preview` as of `8af0ecb`.

This plan is the "explicitly scoped and described in an improvement plan" that CLAUDE.md §8
rule 10 requires before touching `grouper.go`, `hasher.go`, `heic_support.go` or
`hasher_pipeline.go`.

---

## Measured baseline

Profiled against an SMB-mounted iPhone photo library (referred to below as **corpus A**):
**1,134 scannable images (1,038 HEIC, 96 JPG/PNG), 743 `.MOV` skipped by extension, 7.6 GB.**
Harness was a standalone binary built from copies of the repo's own sources, HEIC backend forced
to WASM via `heic.ForceWasmMode` so it matches Windows (which has no dynamic libheif).
8 workers, hash cache deleted before each run, cold page cache enforced with
`posix_fadvise(DONTNEED)`.

| Configuration | Hash | Group | Total | Groups |
|---|---:|---:|---:|---:|
| current code (3 runs) | 44.7–50.5 s | 16.7–18.9 s | **61.7–69.6 s** | 23 |
| + Step 1 only | 52.1 s | 0.76 s | 53.1 s | 23 |
| + Step 2 only | 29.3 s | 15.0 s | 44.6 s | 23 |
| Steps 1+2 | 30.5 s | 0.90 s | 31.7 s | 23 |
| Steps 1+2+3 | 22.9 s | 0.65 s | **23.9 s** | 23 |
| Steps 1+2+3+5 | 24.9 s | 0.56 s | **25.7 s** | 23 |

Warm re-scan (hash cache valid, nothing changed on disk): **16.35 s → 0.79 s**.

Group output was dumped from both builds (match type + sorted member filenames) and diffed:
**identical, 23 for 23.**

### Where the time actually went

Phase 3b component breakdown over all 1,038 HEIC, 8 workers, cold:

```
wall = 52.73 s
  header read (192 KB) : summed 148.24 s = 142.8 ms/file -> 18.53 s wall-equiv
  imagemeta CONFIG     : summed   0.13 s =   0.1 ms/file ->  0.02 s wall-equiv
  HEVC decode + ladder : summed 253.20 s = 243.9 ms/file -> 31.65 s wall-equiv
     of which fullReadFallback: 133.76 s summed, on 22 files
```

Three things this rules out, so nobody re-investigates them:

- **The directory walk is not slow.** 0.22 s for 1,877 entries.
- **The `.MOV` files cost nothing.** Filtered by extension during the walk, never opened,
  never stat'd. They are only a factor for Part B.
- **The WASM decoder is fine.** It scales 4.2× across 8 cores. Dynamic libheif 1.19.8 is
  3.7× faster per call (11.5 ms vs 36.0 ms single-threaded) but is unavailable on Windows.
  There is simply too much decoding, not slow decoding.

---

## Context Claude needs before starting

Read these in full before changing anything:

```
grouper.go            <- GroupDuplicates, findPerceptualPaths, collectUniquePaths
heic_support.go       <- decodeHEICFromHeader, computeDHashHEIC, heicHeaderReadSize
hasher.go             <- computeDHashFromHeaderBuffer (the JPEG precedent for Step 2)
hasher_pipeline.go    <- computePerceptualHashes, HashAllImagesWithProgress
thumb_cache.go        <- storeThumbCache
app.go                <- DeleteFile, GetThumbnail
static/js/table.js    <- showHoverPreview, the mouseenter handler
```

---

# Part A — Performance

## Step 1 — Stop extracting EXIF for the whole library

**File**: `grouper.go`, in `GroupDuplicates`. **Measured: −16.5 s, and 16.35 s → 0.79 s on re-scan.**

`findPerceptualPaths` builds its return map by walking every valid image and appending it to its
Union-Find root:

```go
groups = make(map[string][]string)
for _, h := range valid {
    root := uf.Find(h.Path)
    groups[root] = append(groups[root], h.Path)
}
```

An image with no perceptual match becomes a group of one. Those singletons are only discarded at
the `len(paths) < 2` test further down, which runs *after* `collectUniquePaths` has already handed
all of them to `parallelExtractMetadata`.

Measured on the corpus:

```
percGroups entries=1005   of which singletons=981 (97.6%)
metadata extracted TODAY : 1052 files
metadata actually NEEDED :   71 files  (6.7%)
```

This is also why a re-scan of unchanged files is slow. The hash cache works perfectly
(`Cache split: 1134 hits, 0 misses`, hash phase `0.00 s`) and then grouping spends 16 s re-reading
EXIF off the share regardless.

**Change** — insert immediately before the `allPaths := collectUniquePaths(...)` line:

```go
// A perceptual "group" of one image is not a duplicate group: findPerceptualPaths
// returns one entry per image, and singletons are only dropped by the len < 2 test
// further down. Drop them here instead, so metadata extraction touches only files
// that can actually appear in a result. On a 1,134-file corpus this is the
// difference between opening 1,052 files and opening 71.
realGroups := make(map[string][]string, len(percGroups))
for root, paths := range percGroups {
    if len(paths) >= 2 {
        realGroups[root] = paths
    }
}
percGroups = realGroups
```

The existing `if len(paths) < 2 { continue }` in the group-building loop below becomes redundant.
Leave it — it is cheap and keeps that loop correct on its own.

**Verify**: the `[grouper] Extracting metadata for N files (parallel)...` line must drop from
~1,052 to ~71, and `[grouper] Total: N duplicate groups.` must be unchanged.

---

## Step 2 — Stop full-decoding HEICs that have no embedded thumbnail

**File**: `heic_support.go`, in `decodeHEICFromHeader`. **Measured: −18 s (47.2 s → 29.3 s hash phase).**

The ladder's last rung reads the whole file and decodes the *primary* image when no thumbnail is
found. In WASM that costs 3–13 s per file for a 12 MP iPhone image.

22 of 1,038 HEICs (2.1%) miss the 192 KB window. Each was re-checked against progressively larger
prefixes: two have a thumbnail sitting just past the window, and **20 have no thumbnail item
anywhere in the file**, so the ladder is guaranteed to fall all the way through for them on every
single scan.

```
22 of 1038 HEIC files miss the 192 KB window (2.1%)
  IMG_3765.HEIC   0.8 MB  thumb@2M=false  fullDecode=13.214s
  IMG_3821.HEIC   1.4 MB  thumb@2M=false  fullDecode=11.578s
  IMG_3842.HEIC   2.3 MB  thumb@2M=false  fullDecode=11.084s
  ... 17 more
summed full-decode cost for these 22 files: 125.1 s (5.7 s/file)
```

2% of the files, ~30% of the scan. A JPEG in exactly this position is already handled correctly —
`hasher.go` `computeDHashFromHeaderBuffer` returns `ErrNoThumbnail` and the file is skipped for
perceptual matching rather than fully decoded. HEIC never got the same treatment.

**Change** — replace rungs 3 and 4 (everything from `fullReadStart := time.Now()` to the end of
the function) with one widened retry followed by giving up:

```go
	// Rung 3 — widen the byte range to 1 MB and retry the thumbnail. That recovers
	// files whose thumbnail tile sits just past the header window without paying
	// for the whole file.
	fullReadStart := time.Now()
	if wide, wErr := readHEICPrefix(path, 1024*1024); wErr == nil {
		heicLadderFullReadBytes.Add(int64(len(wide)))
		if img, err := heic.DecodeThumbnail(bytes.NewReader(wide)); err == nil {
			heicLadderFullReadNs.Add(int64(time.Since(fullReadStart)))
			heicLadderFullThumb.Add(1)
			return img, nil
		}
	}
	// No embedded thumbnail anywhere in the file. Decoding the full primary image
	// costs 3-13 s per file under the WASM decoder — measured at 125 s for the 22
	// files in a 1,038-file corpus that reach this point. A JPEG with no EXIF
	// thumbnail already returns ErrNoThumbnail here and is simply skipped for
	// perceptual matching (hasher.go, computeDHashFromHeaderBuffer); do the same
	// rather than spending minutes on a handful of files.
	heicLadderFullReadNs.Add(int64(time.Since(fullReadStart)))
	heicLadderFail.Add(1)
	return nil, ErrNoThumbnail
}

// readHEICPrefix reads up to n bytes from the front of path. Used by the widened
// retry above; readHEICHeader is the same thing pinned to heicHeaderReadSize.
func readHEICPrefix(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	got, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:got], nil
}
```

`heicLadderFullPrimary` is now never incremented. Leave the counter and its `[perf]` field in
place — a non-zero value would mean this step was reverted.

**Accepted tradeoff** (decided): those 20 files get `dHash = 0` and drop out of perceptual
matching. They are still exact-matched by xxHash. On this corpus the final group list was
unchanged.

**Verify**: `[perf] HEIC ladder:` must show `fullPrimary=0 fail=20` and `fullReadFallback` must
fall from ~113 s to ~3 s.

---

## Step 3 — Shrink the HEIC header window to 128 KB

**File**: `heic_support.go`, the `heicHeaderReadSize` constant. **Measured: −26% of the hash phase.**

Whole files were read into RAM and `heic.DecodeThumbnail` replayed against increasing prefixes:

```
rung-1 hit rate by header window     full-scan ladder counter
   64 KB ->  42%                       96 KB : thumbHdr=981
   96 KB ->  98%                      128 KB : thumbHdr=1016
  128 KB -> 100%                      192 KB : thumbHdr=1016
  192 KB -> 100%
```

128 KB reaches exactly the same files as 192 KB. On this share the third 64 KB read request is
disproportionately expensive (`rsize=65536`): a cold read of 221 files takes 0.94 s at 128 KB
versus 2.27 s at 192 KB. Measured inside the pipeline with Steps 1 and 2 already applied, two runs
each:

```
header=192KB   hash=31.11s / 29.72s
header=128KB   hash=22.86s / 22.82s   -26%
header= 96KB   hash=25.34s / 26.55s   (35 files fall through to the 1 MB retry)
```

**Change**:

```go
// 128 KB covers the ftyp + meta + iloc + thumbnail tile on every iPhone HEIC
// tested — measured identical rung-1 hit rate to the previous 192 KB (1016 of
// 1038 files) while reading a third fewer bytes, which is worth 26% of the hash
// phase on an SMB share. 96 KB is too small: 35 files fall through.
const heicHeaderReadSize = 128 * 1024
```

**Verify**: `thumbHdr` in the `[perf] HEIC ladder:` line must stay at its pre-change value. If it
drops, revert to 192 KB — the corpus differs from the one this was measured on.

---

## Step 4 — Debounce and memoise hover thumbnails

**Files**: `static/js/table.js`, `static/js/state.js`. **Measured: 2.6× on hover latency here; likely more on Windows.**

`showHoverPreview` is wired straight to `mouseenter` with no debounce, no in-flight deduplication,
no cancellation on `mouseleave`, and no client-side cache. Sweeping the pointer down a results
table queues one Go call per row crossed, each doing a network read plus a WASM decode; the row the
user actually stops on is served behind all of them.

```
rows crossed   today      debounced
    10         166 ms       71 ms
    25         105 ms       75 ms
    40         209 ms       81 ms     (storm total 925 ms)
```

This is the most likely amplifier of the multi-second hover reported on Windows: it multiplies
whatever the per-file cost is on that machine.

**Change** — in `state.js` add:

```js
thumbCache: new Map(),   // path -> base64 JPEG, populated on first successful hover
hoverTimer: null         // pending setTimeout handle for the hover preview
```

In `table.js`, keep the tooltip appearing immediately (it already shows filename and size) but
delay the *thumbnail request* until the pointer settles, serve from the map when possible, and
cancel on leave:

```js
tr.addEventListener('mouseenter', (e) => showHoverPreview(e, img));
tr.addEventListener('mouseleave', hideHoverPreview);
```

inside `showHoverPreview`, replace the bare `apiGetThumbnail(...)` call with:

```js
  // Serve instantly if this path was fetched before.
  const cached = state.thumbCache.get(img.path);
  if (cached) {
    thumb.src = 'data:image/jpeg;base64,' + cached;
  } else {
    // Only ask Go once the pointer has settled — a fast sweep across the table
    // would otherwise queue one network read + WASM decode per row crossed, and
    // the row the user stopped on ends up behind all of them.
    clearTimeout(state.hoverTimer);
    state.hoverTimer = setTimeout(() => {
      apiGetThumbnail(img.path || '').then(b64 => {
        if (!b64) { tip.style.display = 'none'; return; }
        state.thumbCache.set(img.path, b64);
        // The pointer may have moved on while Go was working; only paint if this
        // tooltip is still the live one.
        if (tip.isConnected) thumb.src = 'data:image/jpeg;base64,' + b64;
      });
    }, 120);
  }
```

and in `hideHoverPreview`, add `clearTimeout(state.hoverTimer);` before removing the element.

**Verify** in `wails dev`: sweep quickly across 30 rows and stop. Only one thumbnail request should
be issued. Hovering an already-seen row must be instant.

---

## Step 5 — Persist the thumbnail the scan already decoded

**Files**: `heic_support.go` (`computeDHashHEIC`), `thumb_cache.go` (`storeThumbCache`), `app.go` (comment).
**Measured: +1.8 s on the scan, hover goes from 55–70 ms to 0.03 ms.**

Phase 3b decodes the embedded thumbnail of every HEIC, computes a dHash from the decoded image,
and drops it. `heic_support.go` documents that as deliberate — thumbnail generation was removed
from the scan in `4ecc858` because it was a hot-path cost. That decision is worth revisiting now
that the decode happens anyway: the marginal cost is only the resize, encode and write.

```
resize + JPEG encode : 11.6 ms/file summed -> 0.46 s wall-equiv
storeThumbCache      :  4.1 ms/file summed -> 0.16 s wall-equiv
full pipeline cost   : +1.8 s on a 24 s scan
hover afterwards     : 40/40 served from disk, 0.03 ms each
```

**Change** in `computeDHashHEIC`, replacing the `// Note: we intentionally do NOT persist...`
block:

```go
	// The decoded thumbnail is in hand here, so persist it for the UI rather than
	// decoding it again on first hover. Measured marginal cost is ~2 ms of worker
	// time per file (+1.8 s on a 24 s scan); decoding on hover instead costs
	// 55-70 ms interactively, and seconds for files with no embedded thumbnail.
	// This is the reverse of 4ecc858, which was correct when the scan generated
	// thumbnails it had to decode specially.
	dh := computeDHashFromImage(img)
	if jb := resizeImageToJPEG(img, 400, 85); jb != nil {
		storeThumbCache(path, jb)
	}
	return dh, width, height, nil
```

Then fix the now-true-again comment in `app.go` `GetThumbnail` (it claims the disk cache "is
populated during the scan's hash phase", which has been false since `4ecc858`).

**Also fix `storeThumbCache`**: it runs a `filepath.Glob` over the entire thumbnail directory on
every write, to find the previous entry for that path. At 317 files that is 275 µs; it grows
linearly, so a 20k-thumbnail library pays it on every store. Since the caller already has the
`os.FileInfo`, compute the previous filename directly rather than globbing for it, or drop the
cleanup to a periodic sweep.

**Verify**: `~/.dedup-photos/thumbs/` should hold roughly one JPEG per scanned HEIC after a scan,
and hovering should be instant with no visible load.

---

## Step 6 — Report progress during the fingerprint phase

**File**: `hasher_pipeline.go`. **No speed change; fixes the scan looking hung.**

`computePerceptualHashes` takes no `ProgressCallback` at all, and neither does the grouping phase.
The last thing the UI is told is `"Computing fingerprints... (0 cached, 1134 to compute)"`, fired
once before phase 3a begins. On the unmodified pipeline that leaves roughly 63 of 65 seconds with a
frozen progress bar, followed by a silent `"Grouping duplicates..."`.

Give `computePerceptualHashes` the same `reportFn`/`total` parameters `statAllFiles` already takes,
and tick an `atomic.Int32` from inside the worker every 25 files. Pass `progressFn` through from
`HashAllImagesWithProgress`.

---

# Part B — Live Photo pairing

## What was verified

A Live Photo is a `.HEIC` still plus a `.MOV` clip that share an `Apple ContentIdentifier` UUID.
Measured across the whole folder:

| | result |
|---|---|
| HEICs carrying `Apple:ContentIdentifier` (maker note tag `0x0011`) | 699 / 1038 (67.3%) |
| MOVs carrying `com.apple.quicktime.content.identifier` | 699 / 699 |
| Same-stem filename pairing vs ContentIdentifier pairing | **699 agree, 0 disagree** |
| HEIC with an ID but no MOV carrying it | 0 |
| MOV with an ID no HEIC carries | 0 |
| Ambiguous (one ID, two MOVs) | 0 |
| HEIC ID reachable from a 128 KB header read | **699 / 699** |
| `.JPG` + `.MOV` pairs in this corpus | 0 |

The remaining 339 HEICs are plain stills: no ContentIdentifier, no `.MOV` sibling.

### Two facts that decide the design

**`bep/imagemeta` cannot be used for this.** It does surface the tag as `MakerNoteApple`, but as a
Go *string*, and the value comes back truncated — 824 bytes against a real maker note of 1,731 on
`IMG_2688.HEIC`. Because Apple's maker-note value offsets are relative to the start of the maker
note, `ContentIdentifier` at offset 1312 falls outside the truncated blob and is unrecoverable.
The tag must be read by walking the TIFF block directly.

**Do not index every MOV during the scan.** `moov` sits at a median of **99.8% of the file** in
these recordings, so reading a MOV's metadata means seeking to the tail of a 4.5 MB file over the
share: 190 ms/file summed, ~17 s for 699 MOVs — as much as the entire optimised scan. Reading one
MOV at delete time costs ~25 ms.

Hence: **pair lazily at delete time.** Part B touches no scan-path code at all.

## Decisions taken

| Question | Decision |
|---|---|
| What happens to the paired `.MOV` | Deleted silently alongside the still. No UI change, no extra confirmation. |
| How the sibling is found | Same-stem candidate from the filesystem, then `ContentIdentifier` verified on both halves before deleting. |
| Recoverability | Unchanged — `os.Remove`, hard delete. The unused `undoStack`/`Action.TrashPath` types stay unused. |
| Thumbnail-less HEICs | Skipped (Step 2), same policy JPEG already gets. |

Consequence worth stating once, since these compound: a batch delete will now remove roughly twice
as many files as before, silently and unrecoverably, on a network share where the recycle bin does
not apply. The ID verification in Step 8 is the only guard, so it must fail closed.

---

## Step 7 — `livephoto_apple.go` (new file)

Extract `Apple:ContentIdentifier` from a HEIC header buffer. Keep under 150 lines; comment
everything per CLAUDE.md §8 rule 11.

Structure, all verified working against the corpus:

```go
// heicContentID returns Apple's ContentIdentifier UUID for a HEIC, or "" when the
// file is not a Live Photo still. header must hold at least the first ~128 KB of
// the file; every one of the 699 Live Photos measured had the tag well inside
// that window (the UUID landed at ~3% into the file).
//
// Path through the containers:
//   ISOBMFF -> Exif item payload -> TIFF header -> IFD0
//     -> tag 0x8769 (Exif IFD) -> tag 0x927C (MakerNote)
//       -> "Apple iOS\0\0\x01" + byte order -> maker-note IFD -> tag 0x0011
func heicContentID(header []byte) string
```

Three helpers:

- `findExifTIFF(buf []byte) ([]byte, bool)` — scan for the TIFF signature (`MM\x00\x2a` or
  `II\x2a\x00`) and accept the first offset where a plausible IFD0 follows. Resolving `iloc`
  properly would be more correct but is far more code; the scan was reliable on all 1,038 files.
  Cap the scan at the buffer length. *This also makes the function work unchanged on JPEG, whose
  APP1 payload begins with the same TIFF header — untested here, since this corpus has no
  JPG+MOV pairs.*
- `tiffIFDEntries(buf []byte, bo binary.ByteOrder, off uint32, fn func(tag, typ uint16, count, valOff uint32, raw []byte))`
  — walk one IFD. Values ≤ 4 bytes are inline in the entry; longer ones live at `valOff`.
- `appleMakerNoteID(blob []byte) string` — parse the Apple IFD and return tag `0x0011`.

The one non-obvious detail, which cost a debugging round and must be commented in the code:

```go
	// Value offsets inside an Apple maker note are relative to the START OF THE
	// MAKER NOTE, not to the TIFF header. This is why a truncated maker-note blob
	// is useless: on IMG_2688.HEIC the tag's value offset is 1312 into a 1731-byte
	// note, so anything shorter than that loses the UUID entirely.
	const appleHeaderLen = 14 // "Apple iOS\0\0\x01" + 2-byte byte order marker
```

## Step 8 — `livephoto_quicktime.go` (new file)

Extract `com.apple.quicktime.content.identifier` from a `.MOV`.

```go
// movContentID returns the QuickTime ContentIdentifier for a video, or "" when
// the file carries none. Reads only the boxes it needs; note that moov sits at a
// median 99.8% of the file in iPhone recordings, so this is a tail seek, ~25 ms
// over SMB.
//
// Layout: moov -> meta -> (keys, ilst)
//   keys : version/flags, entry count, then [size][namespace][key name] entries
//   ilst : one child per value; the child's BOX TYPE is a 1-based index into keys,
//          and its `data` child holds the value after 8 bytes of type/locale.
func movContentID(path string) (string, error)
```

Helpers: `findTopLevelBox(f *os.File, size int64, want string) ([]byte, error)` walking the file's
box chain (handle the 64-bit `size == 1` form), and `findChildBox(buf []byte, want string)` for
in-memory containers.

One compatibility detail to comment: a QuickTime `meta` box is a plain container, while the ISO
variant has a 4-byte version/flags prefix. Detect which by testing for `hdlr` at offset 0 and at
offset 4, and skip 4 bytes if it is the ISO form.

## Step 9 — `livephoto.go` (new file) and wiring into `DeleteFile`

```go
// livePhotoSibling returns the path of the .MOV half of a Live Photo, but only
// when both halves agree on their Apple ContentIdentifier.
//
// Pairing is by filename (IMG_1234.HEIC -> IMG_1234.MOV in the same directory),
// which matched the ContentIdentifier for all 699 pairs measured, then verified
// by reading the UUID from both files. Verification is what makes it safe to
// delete a second file the user did not name, so it FAILS CLOSED: any missing
// tag, unreadable box, or mismatch returns ("", false) and the video is left
// alone.
//
// Cost is ~40 ms per call (a 128 KB HEIC header read plus a MOV tail seek), paid
// only when a file is actually deleted.
func livePhotoSibling(heicPath string) (string, bool)
```

Implementation notes:

- Only attempt this for `.heic`/`.heif` (reuse `isHEIC`).
- Try both `.MOV` and `.mov` — the dev box is case-sensitive even though Windows is not.
- Read the HEIC's own header with `readHEICPrefix(heicPath, heicHeaderReadSize)` from Step 2.
- Return early with `("", false)` if `heicContentID` is empty: 339 of 1,038 HEICs are plain
  stills and must not trigger a MOV read at all.

Then in `app.go` `DeleteFile`, after the still is successfully removed:

```go
	// Live Photo: the .MOV half is the same photo, not a separate item, so it goes
	// with the still. Only ever deleted when both files agree on their Apple
	// ContentIdentifier — see livephoto.go.
	if sibling, ok := livePhotoSibling(path); ok {
		if err := os.Remove(sibling); err != nil {
			log.Printf("[delete] Live Photo video could not be removed: %s: %v", sibling, err)
		} else {
			log.Printf("[delete] Live Photo video removed with its still: %s", sibling)
		}
	}
```

Order matters: resolve the sibling **before** `os.Remove(path)` (the HEIC must still be readable to
verify its UUID), then delete the still, then the video. Restructure accordingly.

`deleteThumbCache` does not need a call for the MOV — videos never had a thumbnail cached.

## Step 10 — Verify Part B

No CI tests (deliberate). Verify by hand on a copy:

```bash
# Copy a handful of pairs somewhere disposable
mkdir -p /tmp/lp && cp "$SHARE"/IMG_268{8,9}.{HEIC,MOV} "$SHARE"/IMG_2687.HEIC /tmp/lp/

# IMG_2688 and IMG_2689 are Live Photos; IMG_2687 is a plain still with no MOV.
# After deleting IMG_2688.HEIC through the UI, IMG_2688.MOV must be gone too,
# IMG_2689.* must be untouched, and IMG_2687.HEIC must delete without any MOV read.
```

Then the negative case, which is the one that matters: copy an unrelated MOV over
`IMG_2689.MOV` so the stem matches but the UUID does not. Deleting `IMG_2689.HEIC` must remove
**only** the HEIC and log the mismatch.

---

## Suggested sequencing

Steps 1 and 2 are the whole story for scan time and are independent of everything else — ship them
first and re-measure before deciding whether the rest is worth it. Step 4 is pure frontend and is
the most likely fix for the hover latency on Windows, so it is worth doing early even though it is
not the biggest number. Part B is self-contained and can be done in a separate session.

## Still unresolved

The 4–5 s hover on Windows was **not reproduced** on Linux over CIFS, where cold hover is p50 66 ms
/ max 106 ms. Candidate mechanisms, in likelihood order: the 20 thumbnail-less files (3–13 s,
measured, and fixed by Step 2); the hover storm amplified by a slower per-file cost (Step 4);
wazero compiling the HEVC module on the first decode in a process (367 ms first vs 55 ms second);
and per-file overhead invisible from here, such as Defender scanning each opened file. If it
survives Steps 2 and 4, instrument `GetThumbnail` to log the cache-lookup / stat / read / decode /
encode split and hover ten images on Windows — that settles it in one run.
