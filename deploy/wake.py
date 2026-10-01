#!/usr/bin/env python3
"""Keyword spotting for SSAU lectures. Reads s16le 16 kHz mono from stdin."""
from __future__ import annotations

import argparse
import json
import os
import sys

# Закрытая грамматика «подгоняет» шум и чужую речь под слова словаря: финальный
# результат берём только по словам с уверенностью не ниже порога, partial —
# только если слово держится в двух partial подряд.
MIN_CONF = float(os.environ.get("WAKE_MIN_CONF", "0.7"))


def emit(text: str) -> None:
    text = " ".join(text.split())
    if text:
        sys.stdout.write(text + "\n")
        sys.stdout.flush()


def confident(res: dict) -> str:
    out = []
    for w in res.get("result") or []:
        word = str(w.get("word", ""))
        if word and word != "[unk]" and float(w.get("conf", 0)) >= MIN_CONF:
            out.append(word)
    return " ".join(out)


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
    rec.SetWords(True)

    buf = sys.stdin.buffer
    prev: set[str] = set()
    sent: set[str] = set()
    while True:
        data = buf.read(4000)
        if not data:
            emit(confident(json.loads(rec.FinalResult())))
            break
        if rec.AcceptWaveform(data):
            emit(confident(json.loads(rec.Result())))
            prev, sent = set(), set()
            continue
        partial = (json.loads(rec.PartialResult()).get("partial") or "").split()
        words = {w for w in partial if w != "[unk]"}
        stable = (words & prev) - sent
        if stable:
            sent |= stable
            emit(" ".join(sorted(stable)))
        prev = words
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
