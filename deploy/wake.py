#!/usr/bin/env python3
"""Keyword spotting for SSAU lectures. Reads s16le 16 kHz mono from stdin."""
from __future__ import annotations

import argparse
import json
import sys


def emit(text: str) -> None:
    text = " ".join(text.split())
    if text:
        sys.stdout.write(text + "\n")
        sys.stdout.flush()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", required=True)
    ap.add_argument("--vocab", default="[]")
    ap.add_argument("--rate", type=int, default=16000)
    args = ap.parse_args()

    try:
        from vosk import KaldiRecognizer, Model, SetLogLevel
    except ImportError as e:
        print("vosk import: %s" % e, file=sys.stderr)
        return 1

    SetLogLevel(-1)
    words = []
    try:
        raw = json.loads(args.vocab)
    except json.JSONDecodeError:
        raw = []
    if isinstance(raw, list):
        for w in raw:
            s = str(w).strip().lower()
            if s:
                words.append(s)
    if not words:
        words = ["тест", "контрольная", "мудл", "moodle"]

    grammar = json.dumps(words + ["[unk]"], ensure_ascii=False)
    rec = KaldiRecognizer(Model(args.model), args.rate, grammar)

    buf = sys.stdin.buffer
    last = ""
    while True:
        data = buf.read(4000)
        if not data:
            tail = json.loads(rec.FinalResult()).get("text") or ""
            emit(tail)
            break
        if rec.AcceptWaveform(data):
            emit(json.loads(rec.Result()).get("text") or "")
            last = ""
            continue
        partial = (json.loads(rec.PartialResult()).get("partial") or "").strip()
        if partial and partial != last:
            last = partial
            emit(partial)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
