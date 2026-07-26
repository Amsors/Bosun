#!/usr/bin/env python3
"""
CPU + memory stress tool.

Only Python's standard library is used.  CPU load is produced by one process per
requested logical core.  Memory is allocated in the parent process and every
page is touched so that the allocation becomes resident instead of remaining a
purely virtual mapping.
"""

from __future__ import annotations

import argparse
import multiprocessing as mp
import os
import re
import signal
import sys
import time
from dataclasses import dataclass
from typing import Iterable


_MEMORY_RE = re.compile(
    r"^\s*(?P<number>(?:\d+(?:\.\d*)?|\.\d+))\s*"
    r"(?P<unit>%|[kmgtpe](?:i?b)?|b)?\s*$",
    re.IGNORECASE,
)
_DURATION_RE = re.compile(
    r"^\s*(?P<number>(?:\d+(?:\.\d*)?|\.\d+))\s*"
    r"(?P<unit>ms|s|m|h)?\s*$",
    re.IGNORECASE,
)


@dataclass(frozen=True)
class MemoryInfo:
    total: int
    available: int | None


def _read_memory_info() -> MemoryInfo:
    """Return physical memory information, preferring Linux /proc data."""
    values: dict[str, int] = {}
    try:
        with open("/proc/meminfo", "r", encoding="ascii") as meminfo:
            for line in meminfo:
                key, raw_value = line.split(":", 1)
                fields = raw_value.strip().split()
                if fields:
                    values[key] = int(fields[0]) * 1024
    except (OSError, ValueError):
        pass

    if "MemTotal" in values:
        return MemoryInfo(values["MemTotal"], values.get("MemAvailable"))

    try:
        page_size = os.sysconf("SC_PAGE_SIZE")
        page_count = os.sysconf("SC_PHYS_PAGES")
        available_pages = os.sysconf("SC_AVPHYS_PAGES")
        return MemoryInfo(page_size * page_count, page_size * available_pages)
    except (AttributeError, OSError, ValueError):
        raise RuntimeError("无法读取系统物理内存大小") from None


def parse_duration(value: str) -> float:
    """Parse a duration such as 2.5, 500ms, 30s, 5m, or 1h."""
    match = _DURATION_RE.fullmatch(value)
    if not match:
        raise argparse.ArgumentTypeError(
            f"无效时间 {value!r}；示例：30、500ms、30s、5m、1h"
        )
    number = float(match.group("number"))
    unit = (match.group("unit") or "s").lower()
    multiplier = {"ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}[unit]
    return number * multiplier


def parse_memory(value: str, total_memory: int) -> int:
    """
    Parse bytes, an IEC-style size, or a percentage of total physical memory.

    K/M/G/T/P/E and KB/MB/... intentionally use powers of 1024, matching the
    convention commonly used by system stress tools. KiB/MiB forms are also
    accepted.
    """
    match = _MEMORY_RE.fullmatch(value)
    if not match:
        raise argparse.ArgumentTypeError(
            f"无效内存量 {value!r}；示例：512M、2G、1073741824、20%"
        )

    number = float(match.group("number"))
    unit = (match.group("unit") or "b").lower()
    if unit == "%":
        if number > 100:
            raise argparse.ArgumentTypeError("内存百分比必须在 0% 到 100% 之间")
        return int(total_memory * number / 100.0)

    normalized = unit.removesuffix("b").removesuffix("i")
    powers = {"": 0, "k": 1, "m": 2, "g": 3, "t": 4, "p": 5, "e": 6}
    result = int(number * (1024 ** powers[normalized]))
    if result < 0:
        raise argparse.ArgumentTypeError("内存量不能为负数")
    return result


def format_bytes(value: int) -> str:
    units = ("B", "KiB", "MiB", "GiB", "TiB", "PiB")
    amount = float(value)
    for unit in units:
        if abs(amount) < 1024.0 or unit == units[-1]:
            if unit == "B":
                return f"{int(amount)} {unit}"
            return f"{amount:.2f} {unit}"
        amount /= 1024.0
    return f"{value} B"


def _allowed_cpu_ids() -> list[int]:
    try:
        return sorted(os.sched_getaffinity(0))
    except AttributeError:
        return list(range(os.cpu_count() or 1))


def _cpu_worker(stop_event: "mp.synchronize.Event", cpu_id: int | None) -> None:
    """Keep one logical CPU busy until the parent asks this process to stop."""
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    if cpu_id is not None:
        try:
            os.sched_setaffinity(0, {cpu_id})
        except (AttributeError, OSError):
            pass

    value = (os.getpid() << 16) | 1
    mask = (1 << 64) - 1
    while not stop_event.is_set():
        # Check the event outside this hot loop to keep CPU utilization high.
        for _ in range(100_000):
            value = (value * 6_364_136_223_846_793_005 + 1) & mask


def _touch_pages(block: bytearray, page_size: int) -> None:
    """Write to every page (and the final byte) to force physical commitment."""
    for offset in range(0, len(block), page_size):
        block[offset] = 1
    if block:
        block[-1] = 1


def _current_rss() -> int | None:
    try:
        with open("/proc/self/statm", "r", encoding="ascii") as statm:
            resident_pages = int(statm.read().split()[1])
        return resident_pages * os.sysconf("SC_PAGE_SIZE")
    except (OSError, ValueError, IndexError, AttributeError):
        return None


def _status_line(
    elapsed: float,
    duration: float,
    allocated: int,
    target: int,
    cpu_cores: int,
) -> str:
    progress = 100.0 if target == 0 else allocated * 100.0 / target
    rss = _current_rss()
    rss_text = f"，进程 RSS {format_bytes(rss)}" if rss is not None else ""
    return (
        f"进度 {min(elapsed, duration):.1f}/{duration:.1f}s，"
        f"CPU {cpu_cores} 核，已申请 {format_bytes(allocated)}/"
        f"{format_bytes(target)} ({progress:.1f}%){rss_text}"
    )


def _stop_processes(
    processes: Iterable[mp.Process], stop_event: "mp.synchronize.Event"
) -> None:
    stop_event.set()
    process_list = list(processes)
    for process in process_list:
        process.join(timeout=2.0)
    for process in process_list:
        if process.is_alive():
            process.terminate()
    for process in process_list:
        process.join(timeout=1.0)


def run_stress(
    cpu_cores: int,
    memory_bytes: int,
    duration: float,
    ramp_up: float,
    *,
    pin_cpu: bool = True,
    status_interval: float = 1.0,
    quiet: bool = False,
) -> int:
    if duration <= 0:
        raise ValueError("测试时间必须大于 0")
    if ramp_up < 0:
        raise ValueError("内存爬升时间不能为负数")
    if ramp_up > duration:
        raise ValueError("内存爬升时间不能大于总测试时间")
    if cpu_cores < 0:
        raise ValueError("CPU 核心数量不能为负数")
    if memory_bytes < 0:
        raise ValueError("内存量不能为负数")
    if cpu_cores == 0 and memory_bytes == 0:
        raise ValueError("CPU 核心数量和内存量不能同时为 0")

    allowed_cpus = _allowed_cpu_ids()
    if cpu_cores > len(allowed_cpus):
        raise ValueError(
            f"请求 {cpu_cores} 个 CPU 核心，但当前进程仅可使用 "
            f"{len(allowed_cpus)} 个逻辑核心"
        )

    # Python 3.14 defaults to "forkserver" on POSIX. Some containers disallow
    # the Unix socket it needs, while this program starts workers before doing
    # any threaded work and can safely use a direct fork.
    context = mp.get_context("fork" if hasattr(os, "fork") else "spawn")
    stop_event = context.Event()
    processes: list[mp.Process] = []
    blocks: list[bytearray] = []
    allocated = 0
    interrupted = False
    allocation_failed = False
    page_size = getattr(os, "getpagesize", lambda: 4096)()

    try:
        for index in range(cpu_cores):
            cpu_id = allowed_cpus[index] if pin_cpu else None
            process = context.Process(
                target=_cpu_worker,
                args=(stop_event, cpu_id),
                name=f"cpu-stress-{index}",
            )
            process.start()
            processes.append(process)
    except BaseException:
        _stop_processes(processes, stop_event)
        raise

    def request_stop(_signum: int, _frame: object) -> None:
        nonlocal interrupted
        interrupted = True
        stop_event.set()

    old_sigint = signal.signal(signal.SIGINT, request_stop)
    old_sigterm = signal.signal(signal.SIGTERM, request_stop)
    start = time.monotonic()
    deadline = start + duration
    next_status = start

    if not quiet:
        mode = "一次性" if ramp_up == 0 else f"{ramp_up:.2f}s 线性爬升"
        print(
            f"开始压测：CPU {cpu_cores} 核，内存 {format_bytes(memory_bytes)}"
            f"（{mode}），总时间 {duration:.2f}s",
            flush=True,
        )

    try:
        while not stop_event.is_set():
            now = time.monotonic()
            elapsed = now - start

            if ramp_up == 0:
                desired = memory_bytes
            else:
                desired = int(memory_bytes * min(elapsed / ramp_up, 1.0))

            if desired > allocated:
                amount = desired - allocated
                try:
                    block = bytearray(amount)
                    _touch_pages(block, page_size)
                except (MemoryError, OverflowError):
                    print(
                        f"\n内存申请失败：已申请 {format_bytes(allocated)}，"
                        f"目标 {format_bytes(memory_bytes)}",
                        file=sys.stderr,
                        flush=True,
                    )
                    allocation_failed = True
                    return 2
                blocks.append(block)
                allocated += amount

            # Allocate the final ramp step before leaving. This matters when
            # ramp_up equals duration: the target reaches exactly 100% at the
            # deadline and is then released.
            if now >= deadline:
                break

            if not quiet and now >= next_status:
                line = _status_line(
                    elapsed, duration, allocated, memory_bytes, cpu_cores
                )
                if sys.stdout.isatty():
                    print(f"\r{line:<100}", end="", flush=True)
                else:
                    print(line, flush=True)
                next_status = now + max(status_interval, 0.1)

            # A short wait keeps ramp updates smooth and reacts quickly to signals.
            stop_event.wait(min(0.05, max(0.0, deadline - time.monotonic())))
    finally:
        elapsed = time.monotonic() - start
        _stop_processes(processes, stop_event)
        blocks.clear()
        signal.signal(signal.SIGINT, old_sigint)
        signal.signal(signal.SIGTERM, old_sigterm)
        if not quiet:
            if sys.stdout.isatty():
                print()
            if allocation_failed:
                state = "失败"
            elif interrupted:
                state = "已中断"
            else:
                state = "已完成"
            print(
                f"{state}：运行 {elapsed:.2f}s，峰值申请 "
                f"{format_bytes(allocated)}，内存已释放",
                flush=True,
            )

    return 130 if interrupted else 0


def build_parser() -> argparse.ArgumentParser:
    memory_info = _read_memory_info()
    parser = argparse.ArgumentParser(
        description="同时进行 CPU 和内存压测（仅使用 Python 标准库）",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "示例：\n"
            "  %(prog)s --cpu-cores 4 --memory 2G --duration 60s "
            "--ramp-up 10s\n"
            "  %(prog)s -c 2 -m 20%% -t 5m -r 30s\n"
            "  %(prog)s -c 0 -m 512M -t 30s -r 0\n\n"
            "说明：百分比内存以系统物理总内存为基准；ramp-up=0 表示"
            "一次性申请。\n"
            f"当前可用逻辑核心：{len(_allowed_cpu_ids())}；"
            f"物理内存：{format_bytes(memory_info.total)}"
        ),
    )
    parser.add_argument(
        "-c",
        "--cpu-cores",
        type=int,
        required=True,
        metavar="N",
        help="用于压测的逻辑 CPU 核心数量；设为 0 可仅压测内存",
    )
    parser.add_argument(
        "-m",
        "--memory",
        required=True,
        metavar="SIZE",
        help="内存申请目标，如 512M、2G、1073741824、20%%；可设为 0",
    )
    parser.add_argument(
        "-t",
        "--duration",
        type=parse_duration,
        required=True,
        metavar="TIME",
        help="总测试时间，如 30、30s、5m、1h",
    )
    parser.add_argument(
        "-r",
        "--ramp-up",
        type=parse_duration,
        required=True,
        metavar="TIME",
        help="内存从 0 线性申请到目标的时间；0 表示一次性申请",
    )
    parser.add_argument(
        "--no-affinity",
        action="store_true",
        help="不把各 CPU 压测进程绑定到固定逻辑核心",
    )
    parser.add_argument(
        "--status-interval",
        type=parse_duration,
        default=1.0,
        metavar="TIME",
        help="状态输出间隔（默认：1s）",
    )
    parser.add_argument(
        "-q",
        "--quiet",
        action="store_true",
        help="除错误外不输出状态",
    )
    parser.set_defaults(_memory_info=memory_info)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    try:
        memory_bytes = parse_memory(args.memory, args._memory_info.total)
    except argparse.ArgumentTypeError as exc:
        parser.error(str(exc))

    if args.cpu_cores < 0:
        parser.error("--cpu-cores 不能为负数")
    if args.duration <= 0:
        parser.error("--duration 必须大于 0")
    if args.ramp_up < 0:
        parser.error("--ramp-up 不能为负数")
    if args.ramp_up > args.duration:
        parser.error("--ramp-up 不能大于 --duration")
    if args.status_interval <= 0:
        parser.error("--status-interval 必须大于 0")
    if args.cpu_cores == 0 and memory_bytes == 0:
        parser.error("--cpu-cores 和 --memory 不能同时为 0")

    available = args._memory_info.available
    if available is not None and memory_bytes > available:
        print(
            f"警告：目标内存 {format_bytes(memory_bytes)} 大于当前系统可用内存 "
            f"{format_bytes(available)}，可能触发 OOM。",
            file=sys.stderr,
            flush=True,
        )

    try:
        return run_stress(
            args.cpu_cores,
            memory_bytes,
            args.duration,
            args.ramp_up,
            pin_cpu=not args.no_affinity,
            status_interval=args.status_interval,
            quiet=args.quiet,
        )
    except ValueError as exc:
        parser.error(str(exc))
    return 2


if __name__ == "__main__":
    mp.freeze_support()
    raise SystemExit(main())
