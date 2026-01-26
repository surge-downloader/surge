# Surge

[![Release](https://img.shields.io/github/v/release/surge-downloader/surge)](https://github.com/surge-downloader/surge/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/surge-downloader/surge)](go.mod)
[![Stars](https://img.shields.io/github/stars/surge-downloader/surge?style=flat-square)](https://github.com/surge-downloader/surge/stargazers)
[![Last Commit](https://img.shields.io/github/last-commit/surge-downloader/surge?style=flat-square)](https://github.com/surge-downloader/surge/commits/main)

Surge is a blazing fast, open-source download manager built in Go. While it features a beautiful **Terminal User Interface (TUI)**, it is architected to run equally well as a background **Headless Server** or a **CLI tool** for automation.

Designed for power users who prefer a keyboard-driven workflow and want full control over their downloads.

![demo](assets/demo.gif)

## Quick Start

### Prebuilt Binaries

[![Get it on GitHub](https://img.shields.io/badge/Get%20it%20on-GitHub-blue?style=for-the-badge&logo=github)](https://github.com/surge-downloader/surge/releases/latest)


### Homebrew (macOS/Linux)

```bash
brew install surge-downloader/tap/surge

```

### Go Install

```bash
go install github.com/surge-downloader/surge@latest

```

### Build from Source

```bash
git clone https://github.com/surge-downloader/surge.git
cd surge
go build -o surge .

```

---
<details><summary>
  <h2> Architecture & Modes </h2>
</summary>

Surge employs a **Strict Single Instance Architecture** to ensure data integrity and efficient resource usage.

### 1. The Engine (Host)
Only **one** instance of Surge runs at a time (the "Host"). This instance holds the lock file (`~/.surge/surge.lock`) and manages all downloads.
- **TUI Mode**: Starts the interactive dashboard.
- **Headless Mode**: Starts as a background daemon (`surge --headless`).

### 2. The Client (CLI)
If you run `surge` or `surge get` while another instance is running, it automatically acts as a **Client**.
- It detects the running Host.
- Offloads the download request to the Host via HTTP.
- Exits immediately (fire-and-forget) or waits if acting as a temporary host.

This means you can open multiple terminals and queue downloads freely; they will all be managed by the single active engine.
</details>

---

## Usage

### Interactive TUI

Start the visual dashboard.

```bash
surge

```

### Headless Server

Start the daemon in the background.

```bash
# Start server (default port)
surge --headless

# Start with specific settings
surge --headless --port 8090 -o ~/Downloads/Surge

```

> **Note:** Set port to `0` to have Surge automatically assign a free port.

### CLI & Remote Control

Send downloads to a running Surge instance (TUI or Headless) or download directly.

```bash
# 1. Send to a running instance (Remote)
# Does not block your terminal; hands off the task to the server.
surge get <URL> --port 8090

# 2. Standalone Download (Direct)
# Blocks the terminal until finished (like wget). No server required.
surge get <URL>

# 3. Override Output Directory
surge get <URL> -o /tmp/downloads

# 4. Batch Download
# Reads a file with one URL per line
surge get --batch urls.txt

```

---

## Features

* **High-speed Downloads** with multi-connection support
* **Beautiful TUI** built with Bubble Tea & Lipgloss
* **Pause/Resume** downloads seamlessly
* **Real-time Progress** with speed graphs and ETA
* **Auto-retry** on connection failures
* **Batch Downloads**
* **Browser Extension** integration
* **Clipboard Integration**

---

<details>
  <summary><h2> How it Works? </h2></summary>

A standard browser usually opens a single HTTP connection to the server. However, servers typically limit the bandwidth allocated to a single connection to ensure fairness for all users.

Download managers (like Surge) open multiple requests simultaneously (e.g., 32 in Surge). They use this method to split the file into many small parts and download those parts individually.

Not all connections are created equal; there are fast connections and slow connections due to factors like load balancers and CDNs. Download managers employ various methods to optimize these connections.

The top 3 optimizations we did in Surge are:

1. **Split the Largest Chunk:** Split the largest chunk whenever possible so that workers do not remain idle.
   
2. **Work Stealing:** Near the end, when fast workers are finished and slow workers are still processing, make the fast, idle workers "steal work" from the slow workers.
   
3. **Restart Slow Workers:** Calculate the mean speed of all workers. If a worker is performing at less than **0.3x** of the mean, restart it in the hopes that it will secure a better pathway to the server, which will be faster.
</details>

---

## Benchmarks

| Tool | Time | Speed | vs Surge |
| --- | --- | --- | --- |
| **Surge** | 28.93s | **35.40 MB/s** | — |
| aria2c | 40.04s | 25.57 MB/s | 1.38× slower |
| curl | 57.57s | 17.79 MB/s | 1.99× slower |
| wget | 61.81s | 16.57 MB/s | 2.14× slower |

<details>
<summary>Test Environment</summary>

*Results averaged over 5 runs*

|  |  |
| --- | --- |
| **File** | 1GB.bin ([link](https://sin-speed.hetzner.com/1GB.bin)) |
| **OS** | Windows 11 Pro |
| **CPU** | AMD Ryzen 5 5600X |
| **RAM** | 16 GB DDR4 |
| **Network** | 360 Mbps / 45 MB/s |

Run your own: `python benchmark.py -n 5`

</details>

## Browser Extension

Intercept downloads from your browser and send them directly to Surge.

### Chrome / Edge

1. Navigate to `chrome://extensions`
2. Enable **Developer mode**
3. Click **Load unpacked** and select the `extension` folder
4. Ensure Surge is running before downloading

### Firefox

1. Navigate to `about:debugging`
2. Click **This Firefox** in the sidebar
3. Click **Load Temporary Add-on...**
4. Select `manifest.json` from the `extension` folder

> **Note:** Temporary add-ons are removed when Firefox closes. For permanent installation, the extension must be signed via [addons.mozilla.org](https://addons.mozilla.org).

The extension will automatically intercept downloads and send them to a running instance of Surge.

## Contributing

Contributions are welcome! Feel free to fork, make changes, and submit a pull request.

## License

If you find Surge useful, please consider giving it a ⭐ it helps others discover the project!

This project is licensed under the MIT License. See the [LICENSE](https://www.google.com/search?q=LICENSE) file for details.
