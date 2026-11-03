#!/usr/bin/env python3
"""Legacy normalization shim. Inputs with opt-in must already be minimized."""

import argparse
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "_shared"))
import deepseek_workflow as writer


def degraded_fallback(minimized_text: str = "") -> dict:
    return {
        "language": "other", "task_type": "other", "intent": "", "goal": "",
        "entities": [], "tech_hint": "unspecified",
        "constraints": [writer.MINIMIZED_NOTE] if minimized_text else [],
        "ambiguities": ["Local normalization required; external writer not used successfully"],
        "urgency": "unspecified", "risk_level": "unknown",
        "requires_code_change": False, "raw_input": minimized_text,
        "normalization_degraded": True,
    }


def normalize(raw_text: str, *, allow_external: bool = False) -> dict:
    if not allow_external:
        print(writer.LOCAL_FALLBACK, file=sys.stderr)
        return degraded_fallback()
    try:
        return writer.generate_artifact("structured-request", raw_text, allow_external=True, minimized_request=raw_text)
    except Exception:
        print(writer.LOCAL_FALLBACK, file=sys.stderr)
        return degraded_fallback(raw_text)


def main() -> int:
    class SafeParser(argparse.ArgumentParser):
        def error(self, message):
            self.exit(2, writer.LOCAL_FALLBACK + "\n")

    parser = SafeParser(description=__doc__)
    parser.add_argument("raw", nargs="?")
    parser.add_argument("--input-file")
    parser.add_argument("--allow-external", action="store_true")
    args = parser.parse_args()
    if not args.allow_external:
        result = normalize("")
    else:
        try:
            if args.raw is not None and args.input_file:
                raise ValueError
            text = Path(args.input_file).read_text(encoding="utf-8") if args.input_file else args.raw or ""
            result = normalize(text, allow_external=True)
        except Exception:
            print(writer.LOCAL_FALLBACK, file=sys.stderr)
            result = degraded_fallback()
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
