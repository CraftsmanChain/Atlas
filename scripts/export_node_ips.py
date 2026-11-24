"""Export in-use ATLAS inventory addresses, one OS IP per line."""
"""
python3 node-ips.py   --base-url http://10.80.192.232:7077   --category cpu   --output /tmp/hosts.txt
"""

import argparse
import ipaddress
import json
import os
from pathlib import Path
import sys
import tempfile
from urllib.parse import urlencode
from urllib.request import urlopen


def export(base_url, category, output):
    addresses = set()
    offset = 0
    skipped = 0
    expected_total = None
    while True:
        query = urlencode(dict(category=category, limit=1000, offset=offset))
        url = f"{base_url.rstrip('/')}/api/v1/inventory/reconciliation?{query}"
        with urlopen(url, timeout=60) as response:
            page = json.load(response)
        rows = page["data"]
        meta = page["meta"]
        total = meta["total"]
        if meta["offset"] != offset or (expected_total is not None and expected_total != total):
            raise ValueError("分页期间资产清单发生变化，请重新导出")
        expected_total = total
        for row in rows:
            if row["category"] != category:
                raise ValueError("返回的资产类别与请求不一致")
            value = row["ip_address"].strip()
            if value:
                addresses.add(ipaddress.ip_address(value))
            else:
                skipped += 1
        offset += len(rows)
        if offset == total:
            break
        if not rows or offset > total:
            raise ValueError("接口返回的记录数与分页总数不一致")

    with tempfile.NamedTemporaryFile(dir=output.parent, delete=False, mode="w") as tmp:
        try:
            for address in sorted(addresses, key=lambda ip: (ip.version, int(ip))):
                print(address, file=tmp)
            tmp.close()
            os.replace(tmp.name, output)
        finally:
            Path(tmp.name).unlink(missing_ok=True)
    print(f"已导出 {len(addresses)} 个 OS IP 到 {output}，缺少 IP 的记录 {skipped} 条", file=sys.stderr)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--category", required=True, choices=[
        "cpu", "gpu", "storage", "network", "firewall", "cloud", "operations", "other",
    ])
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        export(args.base_url, args.category, args.output)
    except (OSError, ValueError, KeyError) as error:
        sys.exit(f"导出失败：{error}")
