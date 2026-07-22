#!/usr/bin/env python3
"""Export GPU/node inventory and target exceptions from Prometheus."""

from __future__ import annotations

import argparse
import csv
import json
import urllib.parse
import urllib.request
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path


JOBS = ("dcgm_exporter", "gpu_exporter", "node_exporter", "ipmi_exporter", "blackbox-exporter")


def api(base: str, path: str, params: dict[str, str] | None = None) -> dict:
    url = f"{base.rstrip('/')}{path}"
    if params:
        url += "?" + urllib.parse.urlencode(params)
    with urllib.request.urlopen(url, timeout=120) as response:
        payload = json.load(response)
    if payload.get("status") != "success":
        raise RuntimeError(payload)
    return payload["data"]


def instance_ip(instance: str) -> str:
    return instance.rsplit(":", 1)[0] if instance.count(":") == 1 else instance


def target_maps(target_data: dict) -> tuple[dict[str, dict[str, dict]], list[dict]]:
    maps: dict[str, dict[str, dict]] = {job: {} for job in JOBS}
    targets = target_data.get("activeTargets", [])
    for target in targets:
        job = target.get("labels", {}).get("job", "")
        if job not in maps:
            continue
        instance = target.get("labels", {}).get("instance", "")
        maps[job][instance_ip(instance)] = {
            "health": target.get("health", "unknown"),
            "error": target.get("lastError", ""),
            "instance": instance,
            "scrape_url": target.get("scrapeUrl", ""),
        }
    return maps, targets


def status_for(mapping: dict[str, dict], ip: str) -> str:
    target = mapping.get(ip)
    return target["health"].upper() if target else "MISSING"


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--prometheus", default="http://10.111.201.1:9090")
    parser.add_argument("--days", type=int, default=365)
    parser.add_argument("--output-dir", default="docs/assets")
    parser.add_argument("--asset-file", default="docs/assets/asset-monitor-compare-2026-07-17.csv")
    parser.add_argument("--reachable", action="append", default=[], help="Host confirmed reachable outside Prometheus")
    parser.add_argument("--unreachable", action="append", default=[], help="Host confirmed unreachable outside Prometheus")
    args = parser.parse_args()

    collected_at = datetime.now(timezone.utc).astimezone()
    stamp = collected_at.strftime("%Y-%m-%d")
    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)

    maps, _ = target_maps(api(args.prometheus, "/api/v1/targets"))

    # The operator-maintained asset document is authoritative for GPU scope:
    # only hostnames containing "gpu" (case-insensitive) belong here.
    asset_path = Path(args.asset_file)
    if not asset_path.exists():
        raise FileNotFoundError(f"GPU asset source not found: {asset_path}")
    with asset_path.open(encoding="utf-8-sig") as handle:
        asset_rows = list(csv.DictReader(handle))
    gpu_hostnames = {
        str(row.get("node_ip", "")).strip(): str(row.get("name", "")).strip()
        for row in asset_rows
        if "gpu" in str(row.get("name", "")).strip().lower()
    }
    gpu_nodes = sorted(gpu_hostnames, key=lambda value: tuple(int(part) for part in value.split(".")))

    current_series = api(args.prometheus, "/api/v1/query", {"query": "DCGM_FI_DEV_GPU_UTIL"}).get("result", [])
    history_hosts = [node for node in gpu_nodes if status_for(maps["dcgm_exporter"], node) != "UP"]
    history_pattern = "|".join(history_hosts)
    historical_series = api(
        args.prometheus,
        "/api/v1/query",
        {"query": f'last_over_time(DCGM_FI_DEV_GPU_UTIL{{instance=~"{history_pattern}"}}[{args.days}d])'},
    ).get("result", [])
    latest_slot: dict[tuple[str, str], dict] = {}
    all_uuid: dict[str, dict] = {}
    # Current samples always win. History only fills slots whose target is now
    # absent/down; label variants in last_over_time must not replace live data.
    for source, series in (("history", historical_series), ("current", current_series)):
        for row in series:
            metric = row.get("metric", {})
            host_ip = metric.get("host_ip") or instance_ip(metric.get("instance", ""))
            gpu = metric.get("gpu", "")
            uuid = metric.get("UUID", "")
            if not host_ip.startswith("10.114.4.") or not gpu or not uuid:
                continue
            normalized = {"metric": metric, "source": source}
            all_uuid[uuid] = normalized
            key = (host_ip, gpu)
            if key not in latest_slot or source == "current":
                latest_slot[key] = normalized

    rows_by_node: dict[str, list[dict]] = defaultdict(list)
    for (host_ip, gpu), row in latest_slot.items():
        rows_by_node[host_ip].append(row)
    for rows in rows_by_node.values():
        rows.sort(key=lambda row: int(row["metric"].get("gpu", 999)))

    node_rows: list[dict[str, str | int]] = []
    gpu_rows: list[dict[str, str | int]] = []
    down_nodes: set[str] = set()
    for host_ip in gpu_nodes:
        node_target = status_for(maps["node_exporter"], host_ip)
        if host_ip in args.unreachable:
            node_state = "DOWN"
            down_nodes.add(host_ip)
        elif node_target == "UP":
            node_state = "UP"
        elif host_ip in args.reachable:
            node_state = "REACHABLE"
        else:
            node_state = "UNKNOWN"

        slot_rows = rows_by_node.get(host_ip, [])
        metrics = [row["metric"] for row in slot_rows]
        hostnames = [gpu_hostnames[host_ip]]
        models = sorted({metric.get("modelName", "") for metric in metrics if metric.get("modelName")})
        bmc_ip = f"10.114.1.{host_ip.split('.')[-1]}"
        component_status = {
            "node_target": node_target,
            "dcgm_target": status_for(maps["dcgm_exporter"], host_ip),
            "gpu_target": status_for(maps["gpu_exporter"], host_ip),
            "ipmi_target": status_for(maps["ipmi_exporter"], bmc_ip),
        }
        notes = []
        if node_state == "DOWN":
            notes.append("NODE_DOWN; component target failures ignored")
        elif node_state == "REACHABLE":
            notes.append("ICMP_REACHABLE; exporter targets require confirmation")
        if len(slot_rows) != 8:
            notes.append(f"GPU_SLOT_COUNT={len(slot_rows)}")
        node_rows.append({
            "node_ip": host_ip,
            "hostname": ",".join(hostnames),
            "node_status": node_state,
            "gpu_count": len(slot_rows),
            "gpu_models": ",".join(models),
            "node_target": component_status["node_target"],
            "dcgm_target": component_status["dcgm_target"],
            "gpu_target": component_status["gpu_target"],
            "bmc_ip": bmc_ip,
            "ipmi_target": component_status["ipmi_target"],
            "notes": "; ".join(notes),
        })
        for row in slot_rows:
            metric = row["metric"]
            gpu_rows.append({
                "node_ip": host_ip,
                "hostname": metric.get("Hostname", ""),
                "node_status": node_state,
                "gpu_index": metric.get("gpu", ""),
                "device": metric.get("device", ""),
                "gpu_uuid": metric.get("UUID", ""),
                "model": metric.get("model", ""),
                "model_name": metric.get("modelName", ""),
                "pci_bus_id": metric.get("pci_bus_id", ""),
                "host_serial": metric.get("sn", ""),
                "driver_version": metric.get("DCGM_FI_DRIVER_VERSION", ""),
                "sample_state": row["source"],
                "collected_at": collected_at.isoformat(timespec="seconds"),
                **component_status,
            })

    replacements = []
    selected_uuids = {row["gpu_uuid"] for row in gpu_rows}
    for uuid, row in all_uuid.items():
        if uuid in selected_uuids:
            continue
        metric = row["metric"]
        replacements.append({
            "node_ip": metric.get("host_ip", instance_ip(metric.get("instance", ""))),
            "gpu_index": metric.get("gpu", ""),
            "old_gpu_uuid": uuid,
            "model_name": metric.get("modelName", ""),
            "sample_state": row["source"],
            "reason": "older UUID for same node/GPU slot; possible replacement or label history",
        })

    def write_csv(path: Path, rows: list[dict], fieldnames: list[str] | None = None) -> None:
        columns = fieldnames or (list(rows[0]) if rows else [])
        with path.open("w", newline="", encoding="utf-8-sig") as handle:
            writer = csv.DictWriter(handle, fieldnames=columns)
            writer.writeheader()
            writer.writerows(rows)

    node_csv = output_dir / f"gpu-node-inventory-{stamp}.csv"
    gpu_csv = output_dir / f"gpu-uuid-inventory-{stamp}.csv"
    replacement_csv = output_dir / f"gpu-uuid-history-{stamp}.csv"
    report_md = output_dir / f"gpu-target-down-report-{stamp}.md"
    write_csv(node_csv, node_rows)
    write_csv(gpu_csv, gpu_rows)
    write_csv(replacement_csv, replacements, ["node_ip", "gpu_index", "old_gpu_uuid", "model_name", "sample_state", "reason"])

    non_node_down: list[tuple[str, str, str, str]] = []
    ignored: list[tuple[str, str, str]] = []
    for node in node_rows:
        host_ip = str(node["node_ip"])
        checks = [
            ("DCGM", "dcgm_exporter", host_ip, str(node["dcgm_target"])),
            ("GPU", "gpu_exporter", host_ip, str(node["gpu_target"])),
            ("Node", "node_exporter", host_ip, str(node["node_target"])),
            ("IPMI", "ipmi_exporter", str(node["bmc_ip"]), str(node["ipmi_target"])),
        ]
        for label, job, target_ip, state in checks:
            if state == "UP":
                continue
            target = maps[job].get(target_ip, {})
            reason = target.get("error", "target absent from active target list")
            if node["node_status"] == "DOWN":
                ignored.append((host_ip, label, reason))
            else:
                non_node_down.append((host_ip, label, state, reason))

    model_counts: dict[str, int] = defaultdict(int)
    for row in gpu_rows:
        model_counts[str(row["model_name"])] += 1
    with report_md.open("w", encoding="utf-8") as handle:
        handle.write(f"# GPU 资产与 DOWN Target 核查（{stamp}）\n\n")
        handle.write(f"- 采集时间：{collected_at.isoformat(timespec='seconds')}\n")
        handle.write(f"- 数据源：`{args.prometheus}`\n")
        handle.write(f"- GPU 节点：{len(node_rows)}\n- GPU UUID/槽位：{len(gpu_rows)}\n- 节点 DOWN：{len(down_nodes)}\n")
        handle.write(f"- 历史窗口：{args.days} 天；同一节点/卡号保留最后出现的 UUID\n\n")
        handle.write("## 型号统计\n\n| 型号 | 数量 |\n|---|---:|\n")
        for model, count in sorted(model_counts.items()):
            handle.write(f"| {model or 'UNKNOWN'} | {count} |\n")
        handle.write("\n## 节点 DOWN\n\n| 节点 | 主机名 | BMC | UUID 数量 |\n|---|---|---|---:|\n")
        for node in node_rows:
            if node["node_status"] == "DOWN":
                handle.write(f"| {node['node_ip']} | {node['hostname']} | {node['bmc_ip']} ({node['ipmi_target']}) | {node['gpu_count']} |\n")
        handle.write("\n## 非节点 DOWN：需要确认的采集组件\n\n| 节点 | 组件 | 状态 | Prometheus 原因 |\n|---|---|---|---|\n")
        for host_ip, label, state, reason in non_node_down:
            handle.write(f"| {host_ip} | {label} | {state} | {reason.replace('|', '/')} |\n")
        handle.write("\n## 因节点 DOWN 忽略\n\n| 节点 | 组件 | 原因 |\n|---|---|---|\n")
        for host_ip, label, reason in ignored:
            handle.write(f"| {host_ip} | {label} | {reason.replace('|', '/')} |\n")
        handle.write("\n## 文件\n\n")
        handle.write(f"- `{node_csv.name}`：节点资产与四类 target 状态\n")
        handle.write(f"- `{gpu_csv.name}`：GPU UUID 明细\n")
        handle.write(f"- `{replacement_csv.name}`：同一槽位历史旧 UUID\n")

    print(json.dumps({
        "nodes": len(node_rows), "gpus": len(gpu_rows), "down_nodes": sorted(down_nodes),
        "non_node_down": len(non_node_down), "historical_uuids": len(all_uuid),
        "historical_replacements": len(replacements), "files": [str(node_csv), str(gpu_csv), str(replacement_csv), str(report_md)],
    }, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
