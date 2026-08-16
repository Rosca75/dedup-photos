# 05 — Cut grouping time by reusing bytes the hash phase already read

> **Branch**: new branch off `main` • **Goal**: remove the per-file re-open and container
> seek from the metadata phase, which is now the dominant cost of grouping.
> **Constraints**: pure Go, no CGo, no new dependency. Must not regress the decision taken
> in plan 04 Step 1 (do not extract EXIF for the whole library).
> **Estimated session size**: Step 1 + Step 2 is one session. Step 3 is optional and separate.
> **Prerequisite**: `f485ee3` (PR #18). The matching fixes must be in, because this plan
> measures against the group counts they produce.

This plan is the "explicitly scoped and described in an improvement plan" that CLAUDE.md §8
rule 10 requires before touching `metadata.go`, `hasher_pipeline.go`, `heic_support.go`
or `cache.go`.

---

## Measured baseline

From the verification runs on PR #18, against
`smb://bogota.local/photo/Portable/8. iPhone 12 PwC/bkp2022` — **3,542 images, 2,939 with a
perceptual fingerprint**, hash cache warm so the hash phase is not in the way:

```
[perf]    BK-tree search: 189 ms, cluster refinement: 0 ms
[perf]    Metadata extraction: 24,275 ms for 551 files
```

and at a looser threshold:

```
[perf]    BK-tree search: 390 ms, cluster refinement: 0 ms
[perf]    Metadata extraction: 45,300 ms for 1,100 files
```

**Grouping is ~99% metadata I/O.** The BK-Tree and the cluster refinement together are under
half a second; everything else is `parallelExtractMetadata`. Per file that is roughly
**44 ms**, and it scales linearly with the number of files that land in a group — so the
better the matching gets, the more this hurts.

### Where the time actually goes

The existing `[perf] Metadata split` counters, same runs:

| files | `read128k` (summed) | `exif` (summed) | HEIC | other |
|---|---:|---:|---:|---:|
| 551 | 36.68 s | 106.14 s | 314 | 237 |
| 1,100 | 38.28 s | 276.85 s | 837 | 263 |

Read the counters carefully before drawing conclusions from them:

- `metaRead128kNs` only covers the **non-HEIC** path, which reads 128 KB into a pooled
  buffer and parses EXIF from memory.
- For HEIC, `ExtractMetadataFast` short-circuits to `extractHEICExif`, which does its own
  `os.Open` and hands the **file handle** to `imagemeta.Decode`. All of that — open, seek,
  read, parse — is charged to `metaExifNs`.

So the 276.85 s is not parsing cost. It is `imagemeta` seeking around an ISOBMFF container
over SMB, one file at a time. That is the thing to remove.

---

## Root cause

The bytes are read twice, and the second read is the expensive one.

**Hash phase**, for every HEIC: `readHEICHeader` reads the first `heicHeaderReadSize`
(128 KB) and `computeDHashHEIC` already parses that buffer with `imagemeta` for dimensions:

```go
imagemeta.Decode(imagemeta.Options{
    R:           bytes.NewReader(header),   // in-memory, no I/O
    ImageFormat: imagemeta.HEIF,
    Sources:     imagemeta.CONFIG,
})
```

Then the buffer is discarded.

**Metadata phase**, for the subset that ended up in a group: `extractHEICExif` re-opens the
same file and lets `imagemeta` seek through it for `Sources: EXIF`.

The container header needed to locate EXIF is, in the overwhelming majority of cases,
already sitting in the buffer the hash phase threw away.

### Why the obvious fix is wrong

"Extract EXIF for every file during hashing" was already tried and reverted — plan 04
Step 1 removed exactly that, because opening 8,000 files to build metadata for the ~5% that
appear in a result cost 17.9 s of a 28 s scan. **Do not reintroduce it.**

The distinction that makes this plan safe: plan 04 removed the extra **file opens**. Parsing
EXIF out of a buffer that is already in memory costs no I/O at all. The trap is retention,
not parsing — 128 KB × 8,000 files is 1 GB, so the raw buffers cannot simply be kept.

---

## Step 1 — Establish whether HEIC EXIF is inside the 128 KB window

**This step is measurement only. Do not write production code until it answers.**

Everything below depends on one unknown: for an iPhone HEIC, does the `Exif` item payload
fall inside the first 128 KB? The `iloc` box gives absolute file offsets, so the answer is
per-file and must be measured, not assumed. `computeDHashHEIC` proves the *container header*
parses fine from a truncated buffer, but `Sources: CONFIG` does not touch the EXIF payload.

Write a throwaway harness (delete it afterwards — see CLAUDE.md §8 rule 3) that, over at
least 2,000 real HEICs:

1. reads `heicHeaderReadSize` bytes,
2. runs `imagemeta.Decode` with `R: bytes.NewReader(header)` and `Sources: imagemeta.EXIF`,
3. records whether `DateTimeOriginal`, `Make`, `Model` and GPS came back,
4. compares each field against what the current `extractHEICExif(path)` returns.

Report: **percentage of files whose EXIF is fully recoverable from the 128 KB buffer**, and
the per-field agreement rate.

Decision gate:

| result | what to do |
|---|---|
| ≥ 95% recoverable | proceed to Step 2 as written |
| 60–95% | proceed, but keep the re-open as a per-file fallback (Step 2 already does) |
| < 60% | stop; the window is in the wrong place. Consider a widened read or an `iloc`-guided second range read, and re-plan |

---

## Step 2 — Carry EXIF forward from the hash phase, via the cache

The scan already has a per-file persistent store keyed on path + size + mtime. Put the
metadata in it.

### 2a. Extend `CachedEntry` (`cache.go`)

Add the small, flat fields the grouper actually consumes — `DateTaken`, `Camera`,
`GPSLat`, `GPSLon`, `Orientation`. Do **not** store `ImageMetadata` wholesale; it carries
derived values like `QualityScore` that must stay computed, not cached.

Bump `cacheVersion` to 5. Roughly 100 bytes per entry — about 1 MB for a 10,000-image
library, against the ~1 GB that retaining raw buffers would cost.

### 2b. Parse EXIF where the bytes already are

In `computeDHashHEIC`, the `header` buffer is in hand and already being handed to
`imagemeta` for `CONFIG`. Ask for `EXIF` in the same call and populate a small struct
alongside the hashes. Same for the non-HEIC path in `computeDHashFromHeaderBuffer`, which
holds the 128 KB header for exactly the same reason.

Thread that struct through `computePerceptualHashes` → `buildFinalResults` → `ImageHash`,
the way `PHash` was threaded in PR #18. Store it via `StoreAll`.

**Cost check before committing to this**: benchmark `imagemeta.Decode` with `Sources: EXIF`
over an in-memory buffer. It must be sub-millisecond. If it is not, this step is only worth
doing for files that reach a group, which the hash phase cannot know — in that case fall
back to Step 3 instead.

### 2c. Consume it in `ExtractMetadataFast`

Give `ExtractMetadataFast` an optional pre-populated metadata argument (mirroring how it
already takes pre-computed `width`/`height`). When present and complete, it skips both the
file open and the EXIF decode and goes straight to `ComputeQualityScore`.

Keep the existing path as the fallback for anything the cache does not have — files whose
EXIF was outside the window, cache misses, and non-HEIC formats that failed to parse.
**Never let a missing cache entry silently produce empty metadata**; that would degrade the
"best image" choice, which drives which file the user is offered to delete.

---

## Step 3 — Optional: skip metadata entirely for groups that do not need it

Independent of Steps 1–2, and only worth doing if they fall short.

`buildGroup` sorts by `QualityScore` to mark the best image. `ComputeQualityScore` needs
dimensions and file size — both already known from the hash phase — plus EXIF for the
tie-breakers. For an **exact** group every member is byte-identical, so the score is the
same for all of them and the EXIF read cannot change the outcome. On the measured library
that is 35 of 132 groups at the default threshold.

Skipping EXIF for exact groups is a straight subtraction from the metadata phase, at the
cost of showing blank camera/date columns for those rows — check with the owner whether
that trade is acceptable before implementing, since it is a visible UI change.

---

## Validation

Run against `bkp2022` (3,542 images) and `bkp2024` (1,177 images), warm hash cache, and
compare against the numbers at the top of this document.

**Must hold:**

1. `[perf] Metadata extraction` drops by at least 5× at the default threshold.
2. `[perf] BK-tree search` and `cluster refinement` are unchanged.
3. **Group output is byte-identical to the current build.** Dump match type, sorted member
   filenames, and the chosen best image per group from both builds and diff them. This is
   the acceptance test — a faster grouper that picks a different "best image" is a
   regression, not an optimisation.
4. `ScanStats.SkippedPerceptual` and `Unreadable` are unchanged.
5. A cold scan is no slower than before. Steps 2b adds CPU to the hash phase; confirm it
   does not show up behind the I/O.

**Watch for**: files whose EXIF is not in the window must still get correct metadata via the
fallback. Verify explicitly by forcing the fallback path on a sample and diffing the result
against the fast path.

---

## Risks

- **Reintroducing the plan 04 regression.** The rule is: no new file *opens* during the hash
  phase. Parsing bytes already in memory is fine; re-opening a file to get EXIF is not.
- **Cache size and invalidation.** `cacheVersion` must be bumped, or v4 entries will be read
  as v5 and yield empty metadata — silently, since EXIF absence is not an error.
- **Quality scoring drift.** `QualityScore` must stay computed at group time from the stored
  inputs, never cached itself; otherwise a scoring change would need a cache wipe to take
  effect.
- **The 128 KB window is shared.** `heicHeaderReadSize` was narrowed from 192 KB to 128 KB in
  plan 04 Step 3 on decode-ladder evidence. If Step 1 shows EXIF sits beyond it, widening the
  window trades hash-phase I/O for metadata-phase I/O and needs measuring on both sides
  before it is done.
