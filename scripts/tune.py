#!/usr/bin/env python3
"""
Tuning script for Surge downloader.
Uses Hill Climbing (Discrete Gradient Descent) to find optimal constants.
"""

import os
import re
import sys
import time
import shutil
import random
import subprocess
import argparse
import json
from pathlib import Path
from dataclasses import dataclass
from typing import Any, List, Dict, Optional, Tuple

# =============================================================================
# CONFIGURATION
# =============================================================================

# Define the search space (Ordered lists for Hill Climbing)
SEARCH_SPACE = {
    "MinChunk":     ["512 * KB", "1 * MB", "2 * MB", "4 * MB"],
    "MaxChunk":     ["8 * MB", "16 * MB", "32 * MB", "64 * MB"],
    "TargetChunk":  ["4 * MB", "8 * MB", "16 * MB", "32 * MB"],
    "WorkerBuffer": ["32 * KB", "64 * KB", "128 * KB", "256 * KB", "512 * KB"],
    "TasksPerWorker": ["2", "4", "8", "16"],
    "PerHostMax":   ["8", "16", "32", "64", "128"],
}

# Mapping to regex patterns in config.go
REGEX_MAP = {
    "MinChunk":     r"(MinChunk\s*=\s*)(.*)(  // Minimum chunk size)",
    "MaxChunk":     r"(MaxChunk\s*=\s*)(.*)( // Maximum chunk size)",
    "TargetChunk":  r"(TargetChunk\s*=\s*)(.*)(  // Target chunk size)",
    "WorkerBuffer": r"(WorkerBuffer\s*=\s*)(.*)",
    "TasksPerWorker": r"(TasksPerWorker\s*=\s*)(.*)( // Target tasks per connection)",
    "PerHostMax":   r"(PerHostMax\s*=\s*)(.*)( // Max concurrent connections per host)",
}

CONFIG_FILE = Path("internal/download/types/config.go").resolve()
BENCHMARK_SCRIPT = Path("benchmark.py").resolve()
PROJECT_ROOT = Path(__file__).parent.parent.resolve()

@dataclass
class Configuration:
    values: Dict[str, int] # Map of key -> index in SEARCH_SPACE

    def get_value_str(self, key: str) -> str:
        return SEARCH_SPACE[key][self.values[key]]

    def __hash__(self):
        return hash(tuple(sorted(self.values.items())))

# =============================================================================
# UTILS
# =============================================================================

def run_command(cmd: List[str], cwd: Path = PROJECT_ROOT, timeout: int = 600) -> Tuple[bool, str]:
    try:
        result = subprocess.run(
            cmd,
            cwd=str(cwd),
            capture_output=True,
            text=True,
            timeout=timeout
        )
        return result.returncode == 0, result.stdout + result.stderr
    except Exception as e:
        return False, str(e)

def backup_config():
    if CONFIG_FILE.exists():
        shutil.copy(CONFIG_FILE, str(CONFIG_FILE) + ".bak")

def restore_config():
    bak = Path(str(CONFIG_FILE) + ".bak")
    if bak.exists():
        shutil.copy(bak, CONFIG_FILE)
        bak.unlink()

def apply_config(config: Configuration):
    """Reads config.go, regex replaces values, writes back."""
    if not CONFIG_FILE.exists():
        raise FileNotFoundError(f"Config file not found: {CONFIG_FILE}")

    content = CONFIG_FILE.read_text()
    
    for key, search_list in SEARCH_SPACE.items():
        if key not in config.values:
            continue
            
        new_val = config.get_value_str(key)
        pattern = REGEX_MAP.get(key)
        
        if not pattern:
            print(f"Warning: No regex pattern for {key}")
            continue

        # Handle WorkerBuffer specially as it might not have a comment suffix in my regex map
        # actually my regex map for WorkerBuffer is (WorkerBuffer\s*=\s*)(.*) which consumes the rest of line
        # Use simple replacement
        
        # Regex replacement: Group 1 is prefix, Group 2 is old value, Group 3 is suffix (optional)
        # We replace with \g<1>NEW_VAL\g<3>
        
        # Special check for WorkerBuffer to preserve potentially missing comment group
        if key == "WorkerBuffer":
             content = re.sub(r"(WorkerBuffer\s*=\s*)(.*)", f"\\g<1>{new_val}", content)
        else:
             content = re.sub(pattern, f"\\g<1>{new_val}\\g<3>", content)

    CONFIG_FILE.write_text(content)

def compile_surge() -> bool:
    print("  Compiling...", end="", flush=True)
    success, out = run_command(["go", "build", "-o", "surge-tuned", "."])
    if success:
        print(" OK")
    else:
        print("\n  [Build Failed]")
        print(out[:200]) # Print start of error
    return success

def run_benchmark(iterations: int = 3) -> float:
    """Runs benchmark.py and returns average speed in MB/s."""
    print(f"  Benchmarking ({iterations} runs)...", end="", flush=True)
    
    # We use the existing benchmark.py but point it to our tuned binary
    cmd = [
        sys.executable,
        str(BENCHMARK_SCRIPT),
        "--surge-exec", str(PROJECT_ROOT / "surge-tuned"),
        "-n", str(iterations),
        "--surge" # Only run surge
    ]
    
    success, output = run_command(cmd)
    if not success:
        print(" Failed")
        print(output)
        return 0.0

    # Parse output for average speed
    # Output format from benchmark.py:
    #   surge (current)      │ OK       │ 1.23s      │ 450.50 MB/s  │ 100.0 MB
    
    match = re.search(r"surge \(current\).*?│\s*([\d\.]+)\s*MB/s", output)
    if match:
        speed = float(match.group(1))
        print(f" {speed:.2f} MB/s")
        return speed
    
    print(" Parse Error")
    return 0.0

# =============================================================================
# ALGORITHMS
# =============================================================================

def get_default_config() -> Configuration:
    """Returns indices corresponding to current hardcoded defaults or closest match."""
    # Based on user request/file:
    # MinChunk = 2MB (Index 2)
    # MaxChunk = 16MB (Index 1)
    # TargetChunk = 8MB (Index 1)
    # WorkerBuffer = 512KB (Index 4)
    # TasksPerWorker = 4 (Index 1)
    # PerHostMax = 64 (Index 3)
    
    return Configuration({
        "MinChunk": 2,      # 2 MB
        "MaxChunk": 1,      # 16 MB
        "TargetChunk": 1,   # 8 MB
        "WorkerBuffer": 4,  # 512 KB
        "TasksPerWorker": 1, # 4
        "PerHostMax": 3,    # 64
    })

def hill_climbing(iterations: int = 10):
    current_config = get_default_config()
    best_config = current_config
    best_speed = 0.0
    
    history = [] # List of (config, speed)
    visited = set()

    iteration_count = iterations
    
    print("\n--- Starting Hill Climbing ---")
    
    # 1. Measure Baseline
    backup_config()
    try:
        apply_config(current_config)
        if compile_surge():
            best_speed = run_benchmark(iterations=iteration_count)
        else:
            print("Baseline build failed! Aborting.")
            restore_config()
            return
    finally:
        restore_config()

    print(f"Baseline Speed: {best_speed:.2f} MB/s")
    history.append((current_config, best_speed))
    visited.add(current_config)

    # 2. Iterate
    # We iterate through each parameter and try neighbors.
    # If a neighbor improves, we move there immediately (Greedy).
    # We repeat until no improvement is found in a full pass.
    
    keys = list(SEARCH_SPACE.keys())
    
    improved = True
    while improved:
        improved = False
        print("\n[Pass Start]")
        
        for key in keys:
            current_idx = current_config.values[key]
            
            # Identify neighbors
            neighbors = []
            if current_idx > 0:
                vals = current_config.values.copy()
                vals[key] = current_idx - 1
                neighbors.append(Configuration(vals))
            if current_idx < len(SEARCH_SPACE[key]) - 1:
                vals = current_config.values.copy()
                vals[key] = current_idx + 1
                neighbors.append(Configuration(vals))
            
            # Test neighbors
            for neighbor in neighbors:
                if neighbor in visited:
                    continue
                
                # Validity check: MaxChunk >= MinChunk
                # Convert string values to simple comparison integers for check
                # Actually, let's just assume list order implies size order for now (which it does)
                min_idx = neighbor.values["MinChunk"]
                max_idx = neighbor.values["MaxChunk"]
                # MinChunk options: 512K, 1M, 2M, 4M
                # MaxChunk options: 8M, 16M, 32M, 64M
                # Even largest min (4M) is < smallest max (8M), so this safety check is trivially satisfied 
                # given the current search space. Good!
                
                print(f"\nTesting Change: {key} -> {neighbor.get_value_str(key)}")
                
                backup_config()
                try:
                    apply_config(neighbor)
                    if compile_surge():
                        speed = run_benchmark(iterations=iteration_count)
                        visited.add(neighbor)
                        history.append((neighbor, speed))
                        
                        if speed > best_speed * 1.01: # 1% improvement threshold
                            print(f"  >>> FOUND BETTER: {speed:.2f} MB/s (was {best_speed:.2f} MB/s)")
                            best_speed = speed
                            best_config = neighbor
                            current_config = neighbor
                            improved = True
                            # Start next parameter from this new base? 
                            # Continue internal loop to see if other param changes help even more?
                            # Standard hill climbing: Move immediately.
                            break 
                        else:
                            print(f"  No significant improvement ({speed:.2f} MB/s)")
                finally:
                    restore_config()
            
            if improved:
                break # Restart loop from new best point
        
        if not improved:
            print("\n[Converged] No neighbors offered improvement.")

    # Report
    print("\n" + "="*50)
    print(f"Optimization Complete.")
    print(f"Best Speed: {best_speed:.2f} MB/s")
    print("Best Configuration:")
    for key in keys:
        print(f"  {key}: {best_config.get_value_str(key)}")
    print("="*50)

    # Save Results
    with open("tuning_results.csv", "w") as f:
        f.write("run_id,speed_mbps," + ",".join(keys) + "\n")
        for i, (conf, speed) in enumerate(history):
            vals = [conf.get_value_str(k) for k in keys]
            f.write(f"{i},{speed:.2f}," + ",".join(vals) + "\n")
            
def main():
    parser = argparse.ArgumentParser(description="Tune Surge Constants")
    parser.add_argument("--iterations", type=int, default=3, help="Runs per benchmark")
    args = parser.parse_args()

    # Ensure we are in root
    os.chdir(PROJECT_ROOT)
    
    try:
        hill_climbing(args.iterations)
    except KeyboardInterrupt:
        print("\nInterrupted! Restoring config...")
        restore_config()
        sys.exit(1)
    except Exception as e:
        print(f"\nError: {e}")
        restore_config()
        sys.exit(1)

if __name__ == "__main__":
    main()
