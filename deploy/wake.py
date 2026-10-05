#!/usr/bin/env python3
"""Speech-to-words for SSAU wake words. Reads s16le 16 kHz mono from stdin,
prints recognized phrases, one per line; matching against user words is done
by the bbb worker (internal/wake)."""
from __future__ import annotations

import argparse
import json
import os
import sys

# Распознаём без закрытого словаря. С грамматикой из нескольких слов Vosk
# подгонял под них любую речь: десятки «тест»/«контрольная» за пару, которых
# никто не произносил, причём с уверенностью до 1.0 — порогом это не лечилось.
# Слова ниже порога в свободном режиме — обычно шум, их не отдаём.
MIN_CONF = float(os.environ.get("WAKE_MIN_CONF", "0.5"))


def emit(text: str) -> None:
    text = " ".join(text.split())
    if text:
        sys.stdout.write(text + "\n")
        sys.stdout.flush()


def confident(res: dict) -> str:
    words = res.get("result")
    if words is None:
        return str(res.get("text") or "")
    out = []
    for w in words:
        word = str(w.get("word", ""))
        if word and float(w.get("conf", 0)) >= MIN_CONF:
            out.append(word)
    return " ".join(out)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", required=True)
    ap.add_argument("--vocab", default="[]")  # не используется; оставлен для старых вызовов
    ap.add_argument("--rate", type=int, default=16000)
    args = ap.parse_args()

    try:
        from vosk import KaldiRecognizer, Model, SetLogLevel
    except ImportError as e:
        print("vosk import: %s" % e, file=sys.stderr)
        return 1

    SetLogLevel(-1)
    rec = KaldiRecognizer(Model(args.model), args.rate)
    rec.SetWords(True)

    buf = sys.stdin.buffer
    while True:
        data = buf.read(4000)
        if not data:
            emit(confident(json.loads(rec.FinalResult())))
            break
        # Только законченные фразы: partial в свободном режиме ещё меняется.
        if rec.AcceptWaveform(data):
            emit(confident(json.loads(rec.Result())))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
