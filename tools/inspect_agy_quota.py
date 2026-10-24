"""Read-only Mach-O / Go symbol inventory for the private quota contract audit."""

import argparse
import hashlib
import json
import struct
from pathlib import Path


def inspect(path: Path) -> dict:
    data = path.read_bytes()
    if data[:4] != b"\xcf\xfa\xed\xfe":
        raise ValueError("expected a little-endian Mach-O 64-bit executable")
    sections = {}
    offset = 32
    for _ in range(struct.unpack_from("<I", data, 16)[0]):
        command, size = struct.unpack_from("<II", data, offset)
        if size < 8:
            raise ValueError("invalid load command")
        if command == 0x19:
            count = struct.unpack_from("<I", data, offset + 64)[0]
            for index in range(count):
                start = offset + 72 + index * 80
                name = data[start:start + 16].split(b"\0")[0].decode()
                address, length, file_offset = struct.unpack_from("<QQI", data, start + 32)
                sections[name] = (address, length, file_offset)
        offset += size
    _, _, pcln = sections["__lrodata_gopcln"]
    if data[pcln:pcln + 4] != b"\xf1\xff\xff\xff":
        raise ValueError("unsupported Go pclntab version")
    count, _, _, names, _, _, _, table = struct.unpack_from("<8Q", data, pcln + 8)
    text_base = sections["__text"][0]
    symbols = []
    wanted = ("extractErrorInfoReasonFromHTTPError", "extractRetryInfoFromHTTPError",
              "ExtractRetryDelayFromError", "classifyGenAIAPIError", "TriggerQuotaRefresh",
              "fetchRetrieveUserQuotaSummary")
    for index in range(count):
        entry, metadata = struct.unpack_from("<II", data, pcln + table + index * 8)
        name_offset = struct.unpack_from("<i", data, pcln + table + metadata + 4)[0]
        start = pcln + names + name_offset
        name = data[start:data.index(b"\0", start)].decode()
        if "/jetski/" in name and any(term in name for term in wanted):
            next_entry = struct.unpack_from("<I", data, pcln + table + (index + 1) * 8)[0]
            symbols.append({"name": name, "start": hex(text_base + entry),
                            "end": hex(text_base + next_entry)})
    return {"sha256": hashlib.sha256(data).hexdigest(), "symbols": symbols,
            "quota_summary_descriptor_present": b"\n\x12QuotaSummaryBucket" in data,
            "error_info_type_url_present": b"type.googleapis.com/google.rpc.ErrorInfo" in data,
            "retry_info_type_url_present": b"type.googleapis.com/google.rpc.RetryInfo" in data}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    args = parser.parse_args()
    print(json.dumps(inspect(args.binary), indent=2))
