#!/usr/bin/env python3
"""
Lightweight PDF text extractor for local validation tasks.

Examples:
  python3 tools/pdf_reader.py /path/to/doc.pdf
  python3 tools/pdf_reader.py /path/to/doc.pdf --keywords 缓存 Redis TTL
  python3 tools/pdf_reader.py /path/to/doc.pdf --start-page 1 --end-page 5 --show-page-number
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import Iterable

from pypdf import PdfReader


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Extract text from PDF with optional keyword filtering.")
    parser.add_argument("pdf_path", type=Path, help="Path to PDF file")
    parser.add_argument(
        "--keywords",
        nargs="*",
        default=[],
        help="Only print pages that contain any of these keywords (case-insensitive).",
    )
    parser.add_argument("--start-page", type=int, default=1, help="1-based start page (inclusive)")
    parser.add_argument("--end-page", type=int, default=0, help="1-based end page (inclusive), 0 means last page")
    parser.add_argument("--show-page-number", action="store_true", help="Show page separators in output")
    return parser.parse_args()


def contains_any_keyword(text: str, keywords: Iterable[str]) -> bool:
    if not keywords:
        return True
    lowered = text.lower()
    return any(keyword.lower() in lowered for keyword in keywords)


def main() -> int:
    args = parse_args()
    if not args.pdf_path.exists():
        print(f"error: file not found: {args.pdf_path}", file=sys.stderr)
        return 2

    reader = PdfReader(str(args.pdf_path))
    total = len(reader.pages)
    start = max(1, args.start_page)
    end = total if args.end_page <= 0 else min(args.end_page, total)
    if start > end:
        print(f"error: invalid page range: {start}..{end}", file=sys.stderr)
        return 2

    for page_idx in range(start - 1, end):
        text = reader.pages[page_idx].extract_text() or ""
        if not contains_any_keyword(text, args.keywords):
            continue
        if args.show_page_number:
            print(f"\n===== PAGE {page_idx + 1} =====\n")
        print(text.strip())
        print()

    return 0


if __name__ == "__main__":
    raise SystemExit(main())

