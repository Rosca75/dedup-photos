# DedupPhotos

[![CI](https://github.com/Rosca75/dedup-photos/actions/workflows/ci.yml/badge.svg)](https://github.com/Rosca75/dedup-photos/actions/workflows/ci.yml)
[![Latest Release](https://img.shields.io/github/v/release/Rosca75/dedup-photos)](https://github.com/Rosca75/dedup-photos/releases/latest)

A fast, local duplicate photo detector with a web-based UI for reviewing and managing duplicate images.

## Features

- Scans directories recursively for duplicate and near-duplicate photos
- Perceptual hashing for visual similarity detection beyond exact matches
- Quality scoring to recommend which duplicate to keep
- Web-based UI for side-by-side comparison and review
- Single self-contained binary with no external dependencies
- Cross-platform support (Windows, macOS, Linux)

## Screenshot

<!-- TODO: Add screenshot of the web UI here -->

## Quick Start

1. Download the latest binary for your platform from the [Releases](https://github.com/Rosca75/dedup-photos/releases/latest) page.

2. Run DedupPhotos, pointing it at the directory you want to scan:

   ```sh
   ./dedup-photos /path/to/photos
   ```

3. Open your browser to the address shown in the terminal (default: `http://localhost:8080`) to review duplicates.

## Build from Source

Requires Go 1.22 or later.

```sh
git clone https://github.com/Rosca75/dedup-photos.git
cd dedup-photos
go build -o dedup-photos .
```

## How It Works

DedupPhotos uses a multi-stage detection pipeline:

1. **File hashing** -- Files are first grouped by size, then compared using SHA-256 to find exact duplicates.
2. **Perceptual hashing** -- Images are resized and converted to perceptual hashes (pHash) to detect visually similar photos that may differ in resolution, compression, or metadata.
3. **Quality scoring** -- Each image in a duplicate group is scored based on resolution, file size, and format to suggest the best copy to keep.
4. **Review UI** -- Duplicate groups are presented in a web interface where you can compare images side by side and choose which to keep or delete.

## CLI Flags

| Flag      | Description                          | Default |
|-----------|--------------------------------------|---------|
| `--port`  | Port for the web UI server           | `8080`  |
| `--help`  | Show usage information               | --      |

## API Endpoints

| Method | Endpoint            | Description                        |
|--------|---------------------|------------------------------------|
| GET    | `/`                 | Serve the web UI                   |
| GET    | `/api/duplicates`   | Return all duplicate groups as JSON|
| POST   | `/api/delete`       | Delete a specified file             |
| GET    | `/api/photo`        | Serve a photo by file path          |
| GET    | `/api/status`       | Return current scan progress        |

## Quality Scoring

| Factor         | Weight | Description                              |
|----------------|--------|------------------------------------------|
| Resolution     | 40%    | Higher megapixel count scores higher      |
| File size      | 30%    | Larger file size indicates less compression |
| Format         | 20%    | Lossless formats (PNG, TIFF) score higher |
| Metadata       | 10%    | Presence of EXIF data adds to the score   |

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.
