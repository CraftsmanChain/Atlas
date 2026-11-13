#!/usr/bin/env python3
"""Generate paired, source-audited architecture diagrams (SVG and PNG).
Requires rsvg-convert for PNG export. No network access.
"""
from html import escape
from pathlib import Path
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / 'web/public'
LAYERS = [
    ('sources', ('证据来源', 'SOURCES'), '#eff6ff', [
        ('assets', ('在用资产与范围', 'In-use assets & scope'), [('运维平台 OAuth · 双源对账', 'Operations OAuth · reconciliation'), ('节点身份与生命周期', 'Node identity & lifecycle')]),
        ('telemetry', ('监控与历史时序', 'Telemetry & history'), [('Prometheus 兼容源', 'Prometheus-compatible sources'), ('DCGM · gpu_exporter', 'DCGM · gpu_exporter')]),
        ('evidence', ('受控只读取证', 'Controlled read-only evidence'), [('日志 · BMC · SMART / EDAC', 'Logs · BMC · SMART / EDAC'), ('注册命令 · 离线证据包', 'Registered commands · evidence files')]),
    ]),
    ('foundation', ('数据基础', 'FOUNDATION'), '#f0fdf4', [
        ('identity', ('身份与数据治理', 'Identity & data quality'), [('资产映射 · 代次 · 适用范围', 'Asset mapping · generations · scope'), ('缺失与来源冲突保持显式', 'Explicit missing or conflicting data')]),
        ('history', ('历史与特征', 'History & features'), [('观测窗口 · 连续性 · 时间边界', 'Observation windows · time boundaries'), ('来源审计 · 受控历史回放', 'Source audits · controlled replay')]),
        ('storage', ('PostgreSQL 持久化', 'PostgreSQL persistence'), [('资产 · 评分 · 事件 · 人工反馈', 'Assets · scores · events · feedback'), ('证据引用 · 版本 · SHA256', 'Evidence references · versions · SHA256')]),
    ]),
    ('assessment', ('评估与发现', 'ASSESSMENT'), '#faf5ff', [
        ('quality', ('数据质量发现', 'Data quality detection'), [('采集可用性 · 指标连续性', 'Collection availability · continuity'), ('身份偏差 · 覆盖缺口', 'Identity drift · coverage gaps')]),
        ('gpu', ('GPU 健康与相对风险', 'GPU health & relative risk'), [('确定性规则 · 可解释健康分', 'Deterministic rules · health scores'), ('影子排名 · 独立效果验证', 'Shadow ranking · separate validation')]),
        ('disk', ('物理盘健康', 'Physical disk health'), [('NVMe · 有限型号 SATA · 历史', 'NVMe · selected SATA profiles · history'), ('分维度评估 · 身份与证据', 'Health dimensions · identity · evidence')]),
    ]),
    ('workspace', ('产品工作台', 'WORKSPACE'), '#fff7ed', [
        ('product', ('React 工作台 + Go API', 'React workbench + Go API'), [('总览 · 资产 · 健康 · 数据质量', 'Overview · assets · health · quality'), ('中英文界面 · 实例配置', 'Chinese / English UI · configuration')]),
        ('workflow', ('事件与人工处置', 'Issues & human resolution'), [('告警详情 · 处置记录 · 反馈', 'Alert details · resolution · feedback'), ('维修复测流程：持续建设', 'Repair validation: evolving workflow')]),
        ('research', ('离线训练与评估', 'Offline training & evaluation'), [('固定数据集 · 候选模型 · 对照', 'Pinned datasets · candidates · baselines'), ('数据与效果门通过后才晋级', 'Promotion requires data & effect gates')]),
    ]),
]


def render(lang):
    pick = lambda pair: pair[0 if lang == 'zh' else 1]
    lines = []
    def add(s): lines.append(s)
    def text(x, y, value, size=16, weight=400, color='#475569'):
        add(f'<text x="{x}" y="{y}" font-size="{size}" font-weight="{weight}" fill="{color}">{escape(value)}</text>')
    add(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 1060" role="img" aria-labelledby="title desc" lang="{lang}">')
    add(f'<title id="title">{pick(("ATLAS 平台架构", "ATLAS Platform Architecture"))}</title>')
    add(f'<desc id="desc">{pick(("证据来源、数据基础、评估发现、工作台及受门禁控制的离线研究。", "Evidence sources, data foundation, assessment, workspace and gated offline research."))}</desc>')
    add('<style>text {font-family:"Helvetica Neue",Arial,"PingFang SC","Microsoft YaHei",sans-serif}</style>')
    add('<defs><marker id="arrow" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto"><path d="M0 0 L10 3.5 L0 7Z" fill="#2563eb"/></marker><marker id="research-arrow" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto"><path d="M0 0 L10 3.5 L0 7Z" fill="#9333ea"/></marker></defs>')
    add('<rect width="1200" height="1060" fill="#ffffff"/>')
    text(40, 46, pick(('ATLAS · 基础设施硬件可靠性工作台', 'ATLAS · Infrastructure Hardware Reliability')), 28, 700, '#111827')
    text(40, 76, pick(('真实证据驱动评估，人工掌握处置权', 'Traceable evidence informs assessment; people control consequential actions')), 17)
    for index, (layer_id, name, tint, nodes) in enumerate(LAYERS):
        y = 108 + index * 200
        add(f'<g id="{layer_id}"><rect x="40" y="{y}" width="1120" height="156" rx="12" fill="{tint}" stroke="#cbd5e1"/>')
        text(58, y + 30, f'0{index+1}  {pick(name)}', 16, 700)
        for col, (node_id, title, subtitles) in enumerate(nodes):
            x = 56 + col * 372
            dashed = ' stroke-dasharray="6 4"' if node_id == 'research' else ''
            add(f'<g id="{node_id}"><rect x="{x}" y="{y+46}" width="344" height="94" rx="8" fill="#ffffff" stroke="#d1d5db"{dashed}/>')
            text(x + 16, y + 74, pick(title), 19, 600, '#111827')
            for row, sub in enumerate(subtitles): text(x + 16, y + 99 + row * 22, pick(sub), 15)
            add('</g>')
        add('</g>')
    # Same-column ingestion, followed by shared foundation / assessment buses.
    for x in (228, 600, 972):
        add(f'<path id="ingest-{x}" d="M{x} 248 V354" stroke="#2563eb" stroke-width="2" fill="none" marker-end="url(#arrow)"/>')
    for top, bus, bottom, prefix in [(448, 482, 554, 'assess'), (648, 682, 754, 'present')]:
        for x in (228, 600, 972): add(f'<path id="{prefix}-source-{x}" d="M{x} {top} V{bus}" stroke="#2563eb" stroke-width="2" fill="none"/>')
        add(f'<path id="{prefix}-bus" d="M228 {bus} H972" stroke="#2563eb" stroke-width="2" fill="none"/>')
        for x in (228, 600, 972):
            optional = prefix == 'present' and x == 972
            color, marker = ('#9333ea', 'research-arrow') if optional else ('#2563eb', 'arrow')
            dash = ' stroke-dasharray="5 4"' if optional else ''
            add(f'<path id="{prefix}-target-{x}" d="M{x} {bus} V{bottom}" stroke="{color}" stroke-width="2" fill="none" marker-end="url(#{marker})"{dash}/>')
    add('<rect x="40" y="930" width="1120" height="82" rx="10" fill="#f8fafc" stroke="#cbd5e1"/>')
    text(58, 960, pick(('共同边界：来源与证据可追溯 · 未知不补零 · 人工确认关键操作', 'Shared boundaries: provenance · explicit unknowns · human-controlled operations')), 18, 600)
    text(58, 989, pick(('相对风险不等于故障概率；研究与影子输出不自动触发隔离或重启。', 'Relative risk is not failure probability; research and shadow output do not trigger isolation or restart.')), 15)
    add('<path d="M48 1034 H90" stroke="#2563eb" stroke-width="2" marker-end="url(#arrow)"/>')
    text(104, 1040, pick(('证据 / 数据流', 'Evidence / data flow')), 14)
    add('<path d="M360 1034 H402" stroke="#9333ea" stroke-width="2" stroke-dasharray="5 4" marker-end="url(#research-arrow)"/>')
    text(416, 1040, pick(('受门禁控制的研究路径', 'Gated research path')), 14)
    add('</svg>')
    return '\n'.join(lines) + '\n'


def main():
    for lang in ('en', 'zh'):
        svg = OUT / f'atlas-platform-architecture.{lang}.svg'
        svg.write_text(render(lang), encoding='utf-8')
        subprocess.run(['rsvg-convert', '-w', '1920', str(svg), '-o', str(svg.with_suffix('.png'))], check=True)
    # Preserve old links while keeping all distributed images current.
    for suffix in ('svg', 'png'):
        shutil.copyfile(OUT / f'atlas-platform-architecture.en.{suffix}', OUT / f'atlas-platform-architecture.{suffix}')

if __name__ == '__main__':
    main()
