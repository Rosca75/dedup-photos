# DedupPhotos

[![CI](https://github.com/Rosca75/dedup-photos/actions/workflows/ci.yml/badge.svg)](https://github.com/Rosca75/dedup-photos/actions/workflows/ci.yml)
[![Latest Release](https://img.shields.io/github/v/release/Rosca75/dedup-photos)](https://github.com/Rosca75/dedup-photos/releases/latest)

A fast, local duplicate photo detector with a web-based UI for reviewing and managing duplicate images.

## Features

- Scans directories recursively for duplicate and near-duplicate photos
- Perceptual hashing for visual similarity detection beyond exact matches
- Quality scoring to recommend which duplicate to keep
- Web-based UI for side-by-side comparison and review
- Single binary — no installation required on Windows; Linux requires WebKit2GTK (see Linux Requirements)
- Cross-platform support (Windows, macOS, Linux)

## Screenshot

<!-- TODO: Add screenshot of the web UI here -->

## Linux Requirements

The Linux binary requires WebKit2GTK to be installed on the host system (it provides
the embedded browser engine). Install it once with:

```bash
# Debian / Ubuntu
sudo apt-get install libwebkit2gtk-4.0-37

# Fedora / RHEL
sudo dnf install webkit2gtk3
```

This is a runtime dependency — it is not bundled into the binary.

## Quick Start

1. Download the latest binary for your platform from the [Releases](https://github.com/Rosca75/dedup-photos/releases/latest) page.
   - **Windows**: download `dedup-photos.exe` — double-click or run from terminal, no install needed.
   - **Linux**: download `dedup-photos-linux`. Make it executable first: `chmod +x dedup-photos-linux`. Ensure [WebKit2GTK is installed](#linux-requirements).

2. Run DedupPhotos, pointing it at the directory you want to scan:

   ```sh
   ./dedup-photos /path/to/photos
   ```

3. Open your browser to the address shown in the terminal (default: `http://localhost:8080`) to review duplicates.

## Build from Source

Requires Go 1.22+, Node.js 20+, and the [Wails CLI](https://wails.io/docs/gettingstarted/installation).

### Install Wails
```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

### Linux — additional system dependencies required
```bash
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.0-dev pkg-config
```

### Build
```bash
git clone https://github.com/Rosca75/dedup-photos.git
cd dedup-photos

# Windows
wails build -platform windows/amd64

# Linux
wails build -platform linux/amd64
```

Output is placed in `build/bin/`.

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
