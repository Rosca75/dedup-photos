# TASK: Set up release workflow and fix README for dedup-photos

## Context

This is a Go + Wails v2 desktop application. The goal of this task is:
1. Add a GitHub Actions release workflow that builds the app for Windows and Linux
2. Fix outdated sections in README.md that describe it as a plain `go build` project

---

## Task 1 — Add the release workflow

### File to create
`.github/workflows/release.yml`

### Action
Copy the content from the provided `release.yml` file exactly as-is into that path.
Do not modify it. Commit with message: `ci: add automated release workflow for Windows and Linux`

---

## Task 2 — Fix README.md

The current README.md contains several inaccuracies now that the app is a Wails v2 desktop application. Apply the following targeted fixes.

### Fix 2a — Replace the "Build from Source" section

**Find this section** (exact heading):
```
## Build from Source
```

**Replace the entire section** with:

```markdown
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
```

---

### Fix 2b — Add a "Linux Requirements" section

**Find this section heading** in the README:
```
## Quick Start
```

**Insert the following block immediately BEFORE it** (above the Quick Start heading):

```markdown
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

```

---

### Fix 2c — Update the Features list

**Find this line** in the Features bullet list:
```
* Single self-contained binary with no external dependencies
```

**Replace it with:**
```
* Single binary — no installation required on Windows; Linux requires WebKit2GTK (see Linux Requirements)
```

---

### Fix 2d — Update the Quick Start step 1

**Find this line** under Quick Start:
```
1. Download the latest binary for your platform from the [Releases](https://github.com/Rosca75/dedup-photos/releases/latest) page.
```

**Replace it with:**
```
1. Download the latest binary for your platform from the [Releases](https://github.com/Rosca75/dedup-photos/releases/latest) page.
   - **Windows**: download `dedup-photos.exe` — double-click or run from terminal, no install needed.
   - **Linux**: download `dedup-photos-linux`. Make it executable first: `chmod +x dedup-photos-linux`. Ensure [WebKit2GTK is installed](#linux-requirements).
```

---

## Commit instructions

After applying all README changes, commit with:
```
docs: fix README for Wails v2 — update build instructions and add Linux requirements
```

---

## Verification checklist (do not commit, just verify locally)

- [ ] `.github/workflows/release.yml` exists and is valid YAML
- [ ] README `## Build from Source` no longer references `go build`
- [ ] README has a `## Linux Requirements` section before `## Quick Start`
- [ ] README Features list no longer claims "no external dependencies" unconditionally
- [ ] README Quick Start step 1 distinguishes Windows vs Linux download instructions
