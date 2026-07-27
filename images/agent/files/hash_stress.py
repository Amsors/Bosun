#!/usr/bin/env python3
"""A configurable, multi-threaded CPU workload for benchmarks and containers.

The worker kernel uses hashlib.pbkdf2_hmac. CPython executes this operation in
OpenSSL while releasing the GIL, so several Python threads can consume several
CPU cores without third-party packages.
"""

from __future__ import annotations

import argparse
import hashlib
import os
import signal
import sys
import threading
import time
from dataclasses import dataclass, field
from itertools import count
from typing import Optional, Sequence


@dataclass
class SharedStats:
    """Thread-safe aggregate counters."""

    completed: int = 0
    active_workers: int = 0
    checksum: int = 0
    errors: list[str] = field(default_factory=list)
    lock: threading.Lock = field(default_factory=threading.Lock)
    finished_event: threading.Event = field(default_factory=threading.Event)

    def task_completed(self, digest: bytes) -> None:
        with self.lock:
            self.completed += 1
            # Keep a tiny observable result so the computation cannot be
            # discarded, without retaining every digest in memory.
            self.checksum ^= int.from_bytes(digest[:8], "big")

    def worker_stopped(self) -> None:
        with self.lock:
            self.active_workers -= 1
            if self.active_workers == 0:
                self.finished_event.set()

    def add_error(self, worker_id: int, exc: BaseException) -> None:
        with self.lock:
            self.errors.append(f"worker-{worker_id}: {type(exc).__name__}: {exc}")

    def snapshot(self) -> tuple[int, int, int, list[str]]:
        with self.lock:
            return (
                self.completed,
                self.active_workers,
                self.checksum,
                list(self.errors),
            )


def positive_int(value: str) -> int:
    number = int(value)
    if number <= 0:
        raise argparse.ArgumentTypeError("必须是大于 0 的整数")
    return number


def positive_float(value: str) -> float:
    number = float(value)
    if number <= 0:
        raise argparse.ArgumentTypeError("必须是大于 0 的数字")
    return number


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=(
            "多线程 CPU 压测工具（仅使用 Python 标准库）。"
            "每个工作单元执行一次 PBKDF2-HMAC-SHA256 计算。"
        ),
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument(
        "-t",
        "--threads",
        type=positive_int,
        default=os.cpu_count() or 1,
        help="工作线程数量",
    )
    parser.add_argument(
        "-i",
        "--intensity",
        type=positive_int,
        default=200_000,
        help="每个工作单元的 PBKDF2 迭代次数；越大，单个任务越重",
    )

    parser.add_argument(
        "-n",
        "--tasks",
        type=positive_int,
        default=100,
        help="需要完成的工作单元总数",
    )

    parser.add_argument(
        "--progress-interval",
        type=positive_float,
        default=1.0,
        help="进度刷新间隔（秒）",
    )
    parser.add_argument(
        "--no-progress",
        action="store_true",
        help="不打印运行中的进度，只打印最终汇总",
    )
    return parser


def worker(
    worker_id: int,
    intensity: int,
    task_limit: int,
    task_counter: count,
    stop_event: threading.Event,
    stats: SharedStats,
) -> None:
    """Run independent PBKDF2 work units until the selected limit is reached."""

    # Worker-specific values avoid every thread calculating identical inputs.
    password = hashlib.sha256(f"cpu-stress-worker-{worker_id}".encode()).digest()
    salt_prefix = hashlib.sha256(
        f"cpu-stress-salt-{worker_id}".encode()
    ).digest()[:12]
    local_sequence = 0
    try:
        while not stop_event.is_set():
            task_id = next(task_counter)
            if task_id >= task_limit:
                break

            salt = salt_prefix + local_sequence.to_bytes(8, "big")
            digest = hashlib.pbkdf2_hmac(
                "sha256",
                password,
                salt,
                intensity,
                dklen=32,
            )
            stats.task_completed(digest)
            password = digest
            local_sequence += 1
    except BaseException as exc:
        stats.add_error(worker_id, exc)
        stop_event.set()
    finally:
        stats.worker_stopped()


def progress_bar(fraction: float, width: int = 24) -> str:
    fraction = min(1.0, max(0.0, fraction))
    filled = int(fraction * width)
    return "[" + "#" * filled + "-" * (width - filled) + "]"


def format_duration(seconds: float) -> str:
    if seconds < 60:
        return f"{seconds:.1f}s"
    minutes, seconds = divmod(seconds, 60)
    if minutes < 60:
        return f"{int(minutes):02d}:{seconds:04.1f}"
    hours, minutes = divmod(minutes, 60)
    return f"{int(hours):02d}:{int(minutes):02d}:{seconds:04.1f}"


def make_progress_line(
    *,
    completed: int,
    active_workers: int,
    elapsed: float,
    interval_rate: float,
    cpu_percent: float,
    task_limit: int,
) -> str:
    fraction = completed / task_limit
    remaining = max(0, task_limit - completed)
    eta = remaining / interval_rate if interval_rate > 0 else float("inf")
    eta_text = format_duration(eta) if eta != float("inf") else "--"
    target = (
        f"{progress_bar(fraction)} {fraction * 100:6.2f}% "
        f"{completed}/{task_limit} ETA {eta_text}"
    )

    return (
        f"{target} | elapsed {format_duration(elapsed)} | "
        f"{interval_rate:7.2f} units/s | "
        f"CPU {cpu_percent:6.1f}% | workers {active_workers}"
    )


def run(args: argparse.Namespace) -> int:
    task_limit = args.tasks

    stop_event = threading.Event()
    stats = SharedStats(active_workers=args.threads)
    task_counter = count()
    start_wall = time.monotonic()
    start_cpu = time.process_time()
    interrupted = False

    def request_stop(signum: int, _frame: object) -> None:
        nonlocal interrupted
        interrupted = True
        stop_event.set()
        signal_name = signal.Signals(signum).name
        print(
            f"\n收到 {signal_name}，等待正在计算的工作单元结束……",
            file=sys.stderr,
            flush=True,
        )

    old_sigint = signal.signal(signal.SIGINT, request_stop)
    old_sigterm = signal.signal(signal.SIGTERM, request_stop)

    threads = [
        threading.Thread(
            target=worker,
            name=f"cpu-worker-{worker_id}",
            args=(
                worker_id,
                args.intensity,
                task_limit,
                task_counter,
                stop_event,
                stats,
            ),
        )
        for worker_id in range(args.threads)
    ]

    print(
        f"开始压测: threads={args.threads}, intensity={args.intensity:,}, "
        f"tasks={task_limit}",
        flush=True,
    )

    for thread in threads:
        thread.start()

    interactive = sys.stdout.isatty()
    last_wall = start_wall
    last_cpu = start_cpu
    last_completed = 0
    last_line_length = 0

    try:
        while any(thread.is_alive() for thread in threads):
            finished = stats.finished_event.wait(args.progress_interval)
            now_wall = time.monotonic()
            now_cpu = time.process_time()
            completed, active_workers, _, errors = stats.snapshot()

            interval_wall = max(now_wall - last_wall, 1e-9)
            interval_rate = (completed - last_completed) / interval_wall
            cpu_percent = (now_cpu - last_cpu) / interval_wall * 100.0

            # The summary below is a more stable final sample. Skipping this
            # last, often millisecond-long interval avoids a misleading spike
            # in the displayed rate as the final worker exits.
            if not args.no_progress and not finished:
                line = make_progress_line(
                    completed=completed,
                    active_workers=active_workers,
                    elapsed=now_wall - start_wall,
                    interval_rate=interval_rate,
                    cpu_percent=cpu_percent,
                    task_limit=task_limit,
                )
                if interactive:
                    padding = " " * max(0, last_line_length - len(line))
                    print(f"\r{line}{padding}", end="", flush=True)
                    last_line_length = len(line)
                else:
                    print(line, flush=True)

            last_wall = now_wall
            last_cpu = now_cpu
            last_completed = completed

            if errors:
                stop_event.set()
    finally:
        stop_event.set()
        for thread in threads:
            thread.join()
        signal.signal(signal.SIGINT, old_sigint)
        signal.signal(signal.SIGTERM, old_sigterm)

    if interactive and not args.no_progress:
        print()

    end_wall = time.monotonic()
    end_cpu = time.process_time()
    elapsed = max(end_wall - start_wall, 1e-9)
    cpu_time = max(end_cpu - start_cpu, 0.0)
    completed, _, checksum, errors = stats.snapshot()

    print(
        "压测结束: "
        f"completed={completed}, wall={elapsed:.3f}s, cpu_time={cpu_time:.3f}s, "
        f"avg_cpu={cpu_time / elapsed * 100:.1f}%, "
        f"throughput={completed / elapsed:.2f} units/s, "
        f"checksum={checksum:016x}",
        flush=True,
    )

    if errors:
        for error in errors:
            print(f"错误: {error}", file=sys.stderr)
        return 1
    if interrupted:
        return 130
    return 0


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return run(args)


if __name__ == "__main__":
    raise SystemExit(main())
