#!/usr/bin/env python3
"""
Benchmark script to compare Surge against other download tools:
- aria2c
- wget
- curl
"""

import os
import platform
import shutil
import subprocess
import sys
import tempfile
import time
import random
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

# =============================================================================
# PLATFORM DETECTION
# =============================================================================
IS_WINDOWS = platform.system() == "Windows"
EXE_SUFFIX = ".exe" if IS_WINDOWS else ""

# =============================================================================
# CONFIGURATION
# =============================================================================
# Default test file URL (test file)
TEST_URL = "https://sin-speed.hetzner.com/1GB.bin"
MB = 1024 * 1024

MOTRIX_CONFIG = """
###############################
# Motrix Linux Aria2 config file
#
# @see https://aria2.github.io/manual/en/html/aria2c.html
#
###############################


################ RPC ################
# Enable JSON-RPC/XML-RPC server.
enable-rpc=true
# Add Access-Control-Allow-Origin header field with value * to the RPC response.
rpc-allow-origin-all=true
# Listen incoming JSON-RPC/XML-RPC requests on all network interfaces.
rpc-listen-all=true


################ File system ################
# Save a control file(*.aria2) every SEC seconds.
auto-save-interval=10
# Enable disk cache.
disk-cache=64M
# Specify file allocation method.
file-allocation=trunc
# No file allocation is made for files whose size is smaller than SIZE
no-file-allocation-limit=64M
# Save error/unfinished downloads to a file specified by --save-session option every SEC seconds.
save-session-interval=10


################ Task ################
# Exclude seed only downloads when counting concurrent active downloads
bt-detach-seed-only=true
# Verify the peer using certificates specified in --ca-certificate option.
check-certificate=false
# If aria2 receives "file not found" status from the remote HTTP/FTP servers NUM times
# without getting a single byte, then force the download to fail.
max-file-not-found=10
# Set number of tries.
max-tries=0
# Set the seconds to wait between retries. When SEC > 0, aria2 will retry downloads when the HTTP server returns a 503 response.
retry-wait=10
# Set the connect timeout in seconds to establish connection to HTTP/FTP/proxy server. After the connection is established, this option makes no effect and --timeout option is used instead.
connect-timeout=10
# Set timeout in seconds.
timeout=10
# aria2 does not split less than 2*SIZE byte range.
min-split-size=1M
# Send Accept: deflate, gzip request header.
http-accept-gzip=true
# Retrieve timestamp of the remote file from the remote HTTP/FTP server and if it is available, apply it to the local file.
remote-time=true
# Set interval in seconds to output download progress summary. Setting 0 suppresses the output.
summary-interval=0
# Handle quoted string in Content-Disposition header as UTF-8 instead of ISO-8859-1, for example, the filename parameter, but not the extended version filename*.
content-disposition-default-utf8=true


################ BT Task ################
# Enable Local Peer Discovery.
bt-enable-lpd=true
# Requires BitTorrent message payload encryption with arc4.
# bt-force-encryption=true
# If true is given, after hash check using --check-integrity option and file is complete, continue to seed file.
bt-hash-check-seed=true
# Specify the maximum number of peers per torrent.
bt-max-peers=128
# Try to download first and last pieces of each file first. This is useful for previewing files.
bt-prioritize-piece=head
# Removes the unselected files when download is completed in BitTorrent.
bt-remove-unselected-file=true
# Seed previously downloaded files without verifying piece hashes.
bt-seed-unverified=false
# Set the connect timeout in seconds to establish connection to tracker. After the connection is established, this option makes no effect and --bt-tracker-timeout option is used instead.
bt-tracker-connect-timeout=10
# Set timeout in seconds.
bt-tracker-timeout=10
# Set host and port as an entry point to IPv4 DHT network.
dht-entry-point=dht.transmissionbt.com:6881
# Set host and port as an entry point to IPv6 DHT network.
dht-entry-point6=dht.transmissionbt.com:6881
# Enable IPv4 DHT functionality. It also enables UDP tracker support.
enable-dht=true
# Enable IPv6 DHT functionality.
enable-dht6=true
# Enable Peer Exchange extension.
enable-peer-exchange=true
# Specify the string used during the bitorrent extended handshake for the peer's client version.
peer-agent=Transmission/3.00
# Specify the prefix of peer ID.
peer-id-prefix=-TR3000-
"""

# =============================================================================
# DATA CLASSES
# =============================================================================
@dataclass
class BenchmarkResult:
    """Result of a single benchmark run."""
    tool: str
    success: bool
    elapsed_seconds: float
    file_size_bytes: int
    error: Optional[str] = None
    iter_results: Optional[list[float]] = None  # List of elapsed times for each iteration

    @property
    def speed_mbps(self) -> float:
        if self.elapsed_seconds <= 0:
            return 0.0
        return (self.file_size_bytes / MB) / self.elapsed_seconds


# =============================================================================
# UTILITY FUNCTIONS
# =============================================================================
def run_command(cmd: list[str], cwd: Optional[str] = None, timeout: int = 600) -> tuple[bool, str]:
    """Run a command and return (success, output)."""
    try:
        # On Windows, use shell=True to find executables in PATH
        # and handle .exe extensions properly
        result = subprocess.run(
            cmd,
            cwd=cwd,
            capture_output=True,
            text=True,
            timeout=timeout,
            shell=IS_WINDOWS,  # Needed for Windows PATH resolution
        )
        output = result.stdout + result.stderr
        return result.returncode == 0, output
    except subprocess.TimeoutExpired:
        return False, "Command timed out"
    except FileNotFoundError as e:
        return False, f"Command not found: {e}"
    except Exception as e:
        return False, str(e)


def which(cmd: str) -> Optional[str]:
    """Return the path to a command, or None if not found."""
    return shutil.which(cmd)


def get_file_size(path: Path) -> int:
    """Get the size of a file in bytes."""
    if path.exists():
        return path.stat().st_size
    return 0


def cleanup_file(path: Path):
    """Remove a file if it exists."""
    try:
        if path.exists():
            path.unlink()
    except Exception:
        pass


# =============================================================================
# SETUP FUNCTIONS
# =============================================================================
def build_surge(project_dir: Path) -> bool:
    """Build surge from source."""
    print("  Building surge...")
    output_name = f"surge{EXE_SUFFIX}"
    success, output = run_command(["go", "build", "-o", output_name, "."], cwd=str(project_dir))
    if not success:
        print(f"    [X] Failed to build surge: {output}")
        return False
    print("    [OK] Surge built successfully")
    return True


def check_wget() -> bool:
    """Check if wget is installed."""
    if which("wget"):
        print("    [OK] wget found")
        return True
    print("    [X] wget not found")
    return False


def check_curl() -> bool:
    """Check if curl is installed."""
    if which("curl"):
        print("    [OK] curl found")
        return True
    print("    [X] curl not found")
    return False

def check_aria2c() -> bool:
    """Check if aria2c is installed."""
    if which("aria2c"):
        print("    [OK] aria2c found")
        return True
    print("    [X] aria2c not found (install aria2)")
    return False


# =============================================================================
# BENCHMARK FUNCTIONS
# =============================================================================
def benchmark_surge(executable: Path, url: str, output_dir: Path, label: str = "surge") -> BenchmarkResult:
    """Benchmark surge downloader using a specific executable."""
    if not executable.exists():
        return BenchmarkResult(label, False, 0, 0, f"Binary not found: {executable}")
    
    start = time.perf_counter()
    success, output = run_command([
        str(executable), "server", "start", "--exit-when-done", url,
        "--output", str(output_dir),  # Download directory
    ], timeout=600)
    elapsed = time.perf_counter() - start
    
    # Try to parse the actual download time from Surge output (excluding probing)
    # Output format: "Complete: 1.0 GB in 5.2s (196.34 MB/s)" OR "... in 500ms ..."
    import re
    actual_time = elapsed
    match = re.search(r"in ([\d\.]+)(m?s)", output)
    if match:
        try:
            val = float(match.group(1))
            unit = match.group(2)
            if unit == "ms":
                val /= 1000.0
            actual_time = val
        except ValueError:
            pass

    # Find downloaded file (surge uses original filename)
    downloaded_files = list(output_dir.glob("*.bin")) + list(output_dir.glob("*MB*")) + list(output_dir.glob("*.zip"))
    file_size = 0
    for f in downloaded_files:
        if f.is_file() and "surge" not in f.name:
            file_size = max(file_size, get_file_size(f))
            cleanup_file(f)
    
    if not success:
        return BenchmarkResult(label, False, actual_time, file_size, output[:200])
    
    return BenchmarkResult(label, True, actual_time, file_size)


def benchmark_aria2(url: str, output_dir: Path) -> BenchmarkResult:
    """Benchmark aria2c downloader."""
    output_file = output_dir / "aria2_download"
    cleanup_file(output_file)
    
    if not which("aria2c"):
        return BenchmarkResult("aria2c", False, 0, 0, "aria2c not installed")
    
    cmd = [
        "aria2c",
        "-x", "16", "-s", "16",  # 16 connections (aria2c compiled max)
        "-o", output_file.name,
        "-d", str(output_dir),
        "--allow-overwrite=true",
        "--console-log-level=warn",
        url
    ]
    
    start = time.perf_counter()
    success, output = run_command(cmd, timeout=600)
    elapsed = time.perf_counter() - start
    
    file_size = get_file_size(output_file)
    cleanup_file(output_file)
    
    if not success:
        return BenchmarkResult("aria2c", False, elapsed, file_size, output[:200])
    
    return BenchmarkResult("aria2c", True, elapsed, file_size)


def benchmark_wget(url: str, output_dir: Path) -> BenchmarkResult:
    """Benchmark wget downloader."""
    output_file = output_dir / "wget_download"
    cleanup_file(output_file)
    
    wget_bin = which("wget")
    if not wget_bin:
        return BenchmarkResult("wget", False, 0, 0, "wget not installed")
    
    start = time.perf_counter()
    success, output = run_command([
        wget_bin, "-q", "-O", str(output_file), url
    ], timeout=600)
    elapsed = time.perf_counter() - start
    
    file_size = get_file_size(output_file)
    cleanup_file(output_file)
    
    if not success:
        return BenchmarkResult("wget", False, elapsed, file_size, output[:200])
    
    return BenchmarkResult("wget", True, elapsed, file_size)


def benchmark_curl(url: str, output_dir: Path) -> BenchmarkResult:
    """Benchmark curl downloader."""
    output_file = output_dir / "curl_download"
    cleanup_file(output_file)
    
    curl_bin = which("curl")
    if not curl_bin:
        return BenchmarkResult("curl", False, 0, 0, "curl not installed")
    
    start = time.perf_counter()
    success, output = run_command([
        curl_bin, "-s", "-L", "-o", str(output_file), url
    ], timeout=600)
    elapsed = time.perf_counter() - start
    
    file_size = get_file_size(output_file)
    cleanup_file(output_file)
    
    if not success:
        return BenchmarkResult("curl", False, elapsed, file_size, output[:200])
    
    return BenchmarkResult("curl", True, elapsed, file_size)


def benchmark_motrix(url: str, output_dir: Path) -> BenchmarkResult:
    """Benchmark Motrix configuration (using aria2c)."""
    
    # Check for custom binary in benchmark/ directory
    script_dir = Path(__file__).parent.resolve()
    custom_bin = script_dir / "benchmark" / "aria2c"
    
    aria2_exec = "aria2c"
    max_conns = 16 # System default limit
    
    if custom_bin.exists():
        aria2_exec = str(custom_bin.resolve())
        max_conns = 64 # Custom binary limit
    elif not which("aria2c"):
        return BenchmarkResult("motrix", False, 0, 0, "aria2c not installed")

    # Check for custom config in benchmark/ directory
    custom_conf = script_dir / "benchmark" / "aria2.conf"
    
    # Prepare config path
    if custom_conf.exists():
        config_path = custom_conf.resolve()
    else:
        # Fallback to internal config
        config_path = output_dir / "motrix.conf"
        try:
            config_path.write_text(MOTRIX_CONFIG)
        except Exception as e:
            return BenchmarkResult("motrix", False, 0, 0, f"Failed to write config: {e}")

    output_file = output_dir / "motrix_download"
    cleanup_file(output_file)

    # Motrix execution
    # IMPORTANT: We MUST override enable-rpc to false to ensure exit.
    cmd = [
        aria2_exec,
        f"--conf-path={config_path}",
        "--enable-rpc=false", 
        "-x", str(max_conns), "-s", str(max_conns), # Adjust connections based on binary capability
        "-o", output_file.name,
        "-d", str(output_dir),
        "--allow-overwrite=true", 
        "--console-log-level=warn",
        url
    ]

    start = time.perf_counter()
    success, output = run_command(cmd, timeout=600)
    elapsed = time.perf_counter() - start

    file_size = get_file_size(output_file)
    cleanup_file(output_file)
    
    # Only cleanup config if we created it
    if not custom_conf.exists():
        cleanup_file(config_path)

    if not success:
        return BenchmarkResult("motrix", False, elapsed, file_size, output[:200])

    return BenchmarkResult("motrix", True, elapsed, file_size)


# =============================================================================
# REPORTING
# =============================================================================
def print_results(results: list[BenchmarkResult]):
    """Print benchmark results in a formatted table."""
    print("\n" + "=" * 70)
    print("  BENCHMARK RESULTS")
    print("=" * 70)
    
    # Header
    print(f"\n  {'Tool':<20} │ {'Status':<8} │ {'Avg Time':<10} │ {'Avg Speed':<12} │ {'Size':<10}")
    print(f"  {'─'*20}─┼─{'─'*8}─┼─{'─'*10}─┼─{'─'*12}─┼─{'─'*10}")
    
    for r in results:
        status = "OK" if r.success else "X"
        time_str = f"{r.elapsed_seconds:.2f}s" if r.elapsed_seconds > 0 else "N/A"
        speed_str = f"{r.speed_mbps:.2f} MB/s" if r.success and r.speed_mbps > 0 else "N/A"
        size_str = f"{r.file_size_bytes / MB:.1f} MB" if r.file_size_bytes > 0 else "N/A"
        
        print(f"  {r.tool:<20} │ {status:<8} │ {time_str:<10} │ {speed_str:<12} │ {size_str:<10}")
        if r.iter_results and len(r.iter_results) > 1:
            print(f"    └─ Runs: {', '.join([f'{t:.2f}s' for t in r.iter_results])}")
        
        if not r.success and r.error:
            print(f"    └─ Error: {r.error[:60]}...")
    
    print("\n" + "=" * 70)
    
    print("\n" + "=" * 70)
    
    # Find winner
    successful = [r for r in results if r.success and r.speed_mbps > 0]
    if successful:
        winner = max(successful, key=lambda r: r.speed_mbps)
        print(f"\n  WINNER: {winner.tool} @ {winner.speed_mbps:.2f} MB/s")
    
    print()
    print_histogram(results)


def print_histogram(results: list[BenchmarkResult]):
    """Print a text-based histogram of download speeds."""
    successful = [r for r in results if r.success and r.speed_mbps > 0]
    if not successful:
        return
        
    print("\n  SPEED COMPARISON")
    print("  " + "-" * 50)
    
    # Sort by speed descending
    sorted_results = sorted(successful, key=lambda r: r.speed_mbps, reverse=True)
    max_speed = sorted_results[0].speed_mbps
    width = 50
    
    for r in sorted_results:
        bar_len = int((r.speed_mbps / max_speed) * width)
        bar = "█" * bar_len
        print(f"  {r.tool:<20} │ {bar:<50} {r.speed_mbps:.2f} MB/s")
    print()


def run_speedtest() -> Optional[str]:
    """Run speedtest-cli and return formatted result string."""
    if not which("speedtest-cli"):
        return "speedtest-cli not found"
        
    print("\n  Running network speedtest (speedtest-cli)...", end="", flush=True)
    success, output = run_command(["speedtest-cli", "--simple"], timeout=60)
    
    if not success:
        print(" Failed")
        return f"Speedtest failed: {output[:50]}..."
        
    print(" Done")
    # Output format:
    # Ping: 12.34 ms
    # Download: 123.45 Mbit/s
    # Upload: 12.34 Mbit/s
    return output.strip()


# =============================================================================
# MAIN
# =============================================================================
def main():
    print("\nSurge Benchmark Suite")
    print("=" * 40)
    
    # Parse arguments
    import argparse
    parser = argparse.ArgumentParser(description="Surge Benchmark Suite")
    parser.add_argument("url", nargs="?", default=TEST_URL, help="URL to download for benchmarking")
    parser.add_argument("-n", "--iterations", type=int, default=1, help="Number of iterations to run (default: 1)")
    
    # Service flags
    parser.add_argument("--surge", action="store_true", help="Run Surge benchmark (default build)")
    parser.add_argument("--aria2", action="store_true", help="Run aria2c benchmark")
    parser.add_argument("--wget", action="store_true", help="Run wget benchmark")
    parser.add_argument("--curl", action="store_true", help="Run curl benchmark")
    parser.add_argument("--motrix", action="store_true", help="Run Motrix benchmark (aria2c + config)")
    
    # Executable flags
    parser.add_argument("--surge-exec", type=Path, help="Path to specific Surge executable to test")
    parser.add_argument("--surge-baseline", type=Path, help="Path to baseline Surge executable for comparison")
    
    # Feature flags
    parser.add_argument("--speedtest", action="store_true", help="Run network speedtest")

    args = parser.parse_args()
    
    test_url = args.url
    num_iterations = args.iterations
    
    # helper to check if any specific service was requested
    specific_service_requested = any([args.surge, args.aria2, args.wget, args.curl, args.motrix, args.surge_exec, args.surge_baseline])
    
    print(f"\n  Test URL:   {test_url}")
    print(f"  Iterations: {num_iterations}")
    
    # Determine project directory
    project_dir = Path(__file__).parent.resolve()
    print(f"  Project:  {project_dir}")
    
    # Create temp directory for downloads
    temp_dir = Path(tempfile.mkdtemp(prefix="surge_bench_"))
    download_dir = temp_dir / "downloads"
    download_dir.mkdir(parents=True, exist_ok=True)
    
    print(f"  Temp Dir: {temp_dir}")
    
    try:
        # Setup phase
        print("\nSETUP")
        print("-" * 40)
        
        # Check speedtest if requested
        if args.speedtest:
            if which("speedtest-cli"):
                print("  [OK] speedtest-cli found")
            else:
                print("  [X] speedtest-cli not found (install speedtest-cli)")

        run_all = not specific_service_requested

        # Initialize all to False
        surge_ok, aria2_ok, wget_ok, curl_ok, motrix_ok = False, False, False, False, False
        surge_exec = None
        surge_baseline_exec = None
        
        # --- Go dependent tools ---
        
        # 1. Main Surge Executable
        if args.surge_exec:
            if args.surge_exec.exists():
                print(f"  [OK] Using provided surge exec: {args.surge_exec}")
                surge_exec = args.surge_exec.resolve()
                surge_ok = True
            else:
                 print(f"  [X] Provided surge exec not found: {args.surge_exec}")
        elif run_all or args.surge:
            if not which("go"):
                print("  [X] Go is not installed. `surge` (local) benchmark will be skipped.")
            else:
                print("  [OK] Go found")
                if build_surge(project_dir):
                    surge_exec = project_dir / f"surge{EXE_SUFFIX}"
                    surge_ok = True
        
        # 2. Baseline Surge Executable
        if args.surge_baseline:
             if args.surge_baseline.exists():
                print(f"  [OK] Using baseline surge exec: {args.surge_baseline}")
                surge_baseline_exec = args.surge_baseline.resolve()
             else:
                print(f"  [X] Baseline surge exec not found: {args.surge_baseline}")


        # --- Aria2 ---
        if run_all or args.aria2:
            aria2_ok = check_aria2c()
        
        if run_all or args.motrix:
             # Motrix uses aria2c, so check for it if we haven't already
             if not aria2_ok: # Optimization: don't check twice if aria2 also selected
                motrix_ok = check_aria2c()
             else:
                motrix_ok = True
        
        # --- Other tools ---
        if run_all or args.wget:
            wget_ok = check_wget()
        
        if run_all or args.curl:
            curl_ok = check_curl()
        
        # Define benchmarks to run
        tasks = []
        
        # Surge Main
        if surge_ok:
            tasks.append({"name": "surge (current)", "func": benchmark_surge, "args": (surge_exec, test_url, download_dir, "surge (current)")})
        
        # Surge Baseline
        if surge_baseline_exec:
             tasks.append({"name": "surge (baseline)", "func": benchmark_surge, "args": (surge_baseline_exec, test_url, download_dir, "surge (baseline)")})
        
        # aria2c
        if aria2_ok and (run_all or args.aria2):
            tasks.append({"name": "aria2c", "func": benchmark_aria2, "args": (test_url, download_dir)})
        
        # wget
        if wget_ok and (run_all or args.wget):
            tasks.append({"name": "wget", "func": benchmark_wget, "args": (test_url, download_dir)})
        
        # curl
        if curl_ok and (run_all or args.curl):
            tasks.append({"name": "curl", "func": benchmark_curl, "args": (test_url, download_dir)})

        # Motrix
        if motrix_ok and (run_all or args.motrix):
            tasks.append({"name": "motrix", "func": benchmark_motrix, "args": (test_url, download_dir)})

        # Initialize results storage
        # Map: tool_name -> list of BenchmarkResult
        raw_results: dict[str, list[BenchmarkResult]] = {task["name"]: [] for task in tasks}

        if not tasks:
            print("No benchmarks to run.")
            return

        # Benchmark phase
        print("\nBENCHMARKING")
        print("-" * 40)
        
        # Run speedtest first if requested
        if args.speedtest:
            st_result = run_speedtest()
            print("\n  Speedtest Results:")
            if st_result:
                print("  " + "\n  ".join(st_result.splitlines()))
            print("-" * 40)

        print(f"  Downloading: {test_url}")
        print(f"  Exec Order:  Interlaced ({len(tasks)} tools x {num_iterations} runs)\n")
        
        for i in range(num_iterations):
            print(f"\n  [ Iteration {i+1}/{num_iterations} ]")
            
            iteration_tasks = tasks.copy()
            random.shuffle(iteration_tasks)

            for task in iteration_tasks:
                name = task["name"]
                func = task["func"]
                task_args = task["args"]
                
                print(f"    Running {name}...", end="", flush=True)
                res = func(*task_args)
                
                raw_results[name].append(res)
                
                if res.success:
                    print(f" {res.elapsed_seconds:.2f}s")
                else:
                    print(" Failed")
                
                # Increase sleep to allow SSD buffer flush and server rate-limit reset
                time.sleep(5)

        # Aggregate results
        final_results: list[BenchmarkResult] = []
        
        for task in tasks:
            name = task["name"]
            runs = raw_results[name]
            
            # Filter successful runs for time averaging
            successful_runs = [r for r in runs if r.success]
            
            if not successful_runs:
                # All failed, grab the last error
                last_error = runs[-1].error if runs else "No runs"
                final_results.append(BenchmarkResult(name, False, 0, 0, last_error))
                continue

            times = [r.elapsed_seconds for r in successful_runs]
            
            # Outlier filtering: Take middle 60% of runs
            # If we have N runs, sort them, and drop top 20% and bottom 20%.
            # This requires at least enough runs to make sense, but for N=1 it will keep 1.
            n = len(times)
            if n > 2:
                sorted_times = sorted(times)
                trim_count = int(n * 0.2) # 20% from each side
                # Slice from trim_count to n - trim_count
                # e.g. n=10, trim=2, slice [2:8] -> indices 2,3,4,5,6,7 (6 items)
                # e.g. n=5, trim=1, slice [1:4] -> indices 1,2,3 (3 items)
                
                # Ensure we don't trim everything (shouldn't happen with logic above if n > 2)
                if trim_count > 0:
                    filtered_times = sorted_times[trim_count : n - trim_count]
                    if filtered_times:
                        times = filtered_times

            avg_time = sum(times) / len(times)
            
            # Use the size from the first successful run (should be constant)
            file_size = successful_runs[0].file_size_bytes
            
            final_results.append(BenchmarkResult(
                tool=name,
                success=True,
                elapsed_seconds=avg_time,
                file_size_bytes=file_size,
                iter_results=times
            ))

        # Print results
        print_results(final_results)

        
    finally:
        # Cleanup
        print("Cleaning up temp directory...")
        shutil.rmtree(temp_dir, ignore_errors=True)
        print("  Done.")


if __name__ == "__main__":
    main()
