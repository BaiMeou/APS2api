"""Unified proxy node store and health scoring.

This module keeps proxy-node concerns small and local:
- fetched subscription nodes are persisted into one unified candidate set
- runtime health is stored separately from user subscription config
- parallel requests choose nodes by cooldown + score + weighted randomness
"""

from __future__ import annotations

import json
import math
import os
import random
import time
from pathlib import Path
from typing import Any

from src.utils.logger import get_logger

logger = get_logger(__name__)

_ROOT_DIR = Path(__file__).parent.parent.parent
NODES_FILE = _ROOT_DIR / "config" / "nodes.json"
NODE_HEALTH_FILE = _ROOT_DIR / "config" / "node_health.json"


def _read_json(path: Path, default: Any) -> Any:
    try:
        if not path.exists():
            return default if not isinstance(default, dict) else dict(default)
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)
    except Exception as e:
        logger.warning(f"读取节点存储失败: {path} {e}")
        return default if not isinstance(default, dict) else dict(default)


def _write_json(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
    os.replace(tmp, path)


def node_key(raw_uri: str) -> str:
    return raw_uri.strip()


def load_nodes() -> list[dict[str, Any]]:
    data = _read_json(NODES_FILE, {"nodes": []})
    nodes = data.get("nodes", []) if isinstance(data, dict) else []
    return [n for n in nodes if isinstance(n, dict) and str(n.get("raw_uri", "")).strip()]


def _clean_node(node: dict[str, Any], source: str, now: int) -> dict[str, Any] | None:
    raw_uri = str(node.get("raw_uri") or "").strip()
    if not raw_uri:
        return None
    item = dict(node)
    item["raw_uri"] = raw_uri
    item.setdefault("name", raw_uri[:40])
    item.setdefault("source", source)
    item["disabled"] = bool(item.get("disabled", False))
    item.setdefault("disabled_at", 0)
    item.setdefault("disabled_reason", "")
    item["updated_at"] = now
    return item


def _merge_node_metadata(existing: dict[str, Any], incoming: dict[str, Any]) -> dict[str, Any]:
    """Merge imported metadata without accidentally re-enabling a disabled node."""
    merged = {**existing, **incoming}
    imported_enabled_default = not incoming.get("disabled") and not incoming.get("disabled_at") and not incoming.get("disabled_reason")
    if existing.get("disabled") and imported_enabled_default:
        merged["disabled"] = True
        merged["disabled_at"] = existing.get("disabled_at", 0)
        merged["disabled_reason"] = existing.get("disabled_reason", "")
    return merged


def _write_nodes(nodes: list[dict[str, Any]], now: int) -> None:
    _write_json(NODES_FILE, {"updated_at": now, "count": len(nodes), "nodes": nodes})


def _prune_health(active_keys: set[str]) -> None:
    health = load_health()
    kept = {k: v for k, v in health.items() if k in active_keys}
    if kept != health:
        save_health(kept)


def replace_nodes(nodes: list[dict[str, Any]], source: str = "subscriptions", prune_health: bool = True) -> dict[str, int]:
    """Replace the unified node set after normalizing duplicate raw_uri keys."""
    existing_by_key = {node_key(str(n.get("raw_uri") or "")): n for n in load_nodes()}
    cleaned: list[dict[str, Any]] = []
    positions: dict[str, int] = {}
    now = int(time.time())
    imported_count = 0
    duplicate_count = 0
    for node in nodes:
        item = _clean_node(node, source, now)
        if not item:
            continue
        imported_count += 1
        key = node_key(item["raw_uri"])
        existing = existing_by_key.get(key)
        if existing:
            item = _merge_node_metadata(existing, item)
        if key in positions:
            duplicate_count += 1
            cleaned[positions[key]] = _merge_node_metadata(cleaned[positions[key]], item)
            continue
        positions[key] = len(cleaned)
        cleaned.append(item)
    _write_nodes(cleaned, now)
    if prune_health:
        _prune_health(set(positions))
    return {
        "imported_count": imported_count,
        "added_count": len(cleaned),
        "updated_count": duplicate_count,
        "duplicate_count": duplicate_count,
        "total": len(cleaned),
    }


def merge_nodes(nodes: list[dict[str, Any]], source: str = "import") -> dict[str, int]:
    """Merge imported nodes into the existing set, updating duplicate metadata in place."""
    now = int(time.time())
    merged: list[dict[str, Any]] = []
    positions: dict[str, int] = {}

    for node in load_nodes():
        item = _clean_node(node, str(node.get("source") or source), now)
        if not item:
            continue
        key = node_key(item["raw_uri"])
        if key in positions:
            merged[positions[key]] = _merge_node_metadata(merged[positions[key]], item)
            continue
        positions[key] = len(merged)
        merged.append(item)

    imported_count = 0
    added_count = 0
    updated_count = 0
    duplicate_count = 0
    for node in nodes:
        raw_uri = str(node.get("raw_uri") or "").strip()
        if not raw_uri:
            continue
        imported_count += 1
        item = _clean_node(node, source, now)
        if not item:
            continue
        key = node_key(item["raw_uri"])
        if key in positions:
            merged[positions[key]] = _merge_node_metadata(merged[positions[key]], item)
            updated_count += 1
            duplicate_count += 1
            continue
        positions[key] = len(merged)
        merged.append(item)
        added_count += 1

    _write_nodes(merged, now)
    return {
        "imported_count": imported_count,
        "added_count": added_count,
        "updated_count": updated_count,
        "duplicate_count": duplicate_count,
        "total": len(merged),
    }


def delete_node(raw_uri: str) -> dict[str, int | bool]:
    key = node_key(raw_uri)
    if not key:
        return {"deleted": False, "total": len(load_nodes())}
    nodes = load_nodes()
    kept = [node for node in nodes if node_key(str(node.get("raw_uri") or "")) != key]
    if len(kept) == len(nodes):
        return {"deleted": False, "total": len(nodes)}
    now = int(time.time())
    _write_nodes(kept, now)
    health = load_health()
    if key in health:
        health.pop(key, None)
        save_health(health)
    return {"deleted": True, "total": len(kept)}


def set_node_disabled(raw_uri: str, disabled: bool, reason: str = "") -> dict[str, Any]:
    key = node_key(raw_uri)
    nodes = load_nodes()
    now = int(time.time())
    changed = False
    updated_node: dict[str, Any] | None = None
    for node in nodes:
        if node_key(str(node.get("raw_uri") or "")) != key:
            continue
        node["disabled"] = bool(disabled)
        node["disabled_at"] = now if disabled else 0
        node["disabled_reason"] = str(reason or "")[:500] if disabled else ""
        node["updated_at"] = now
        updated_node = dict(node)
        changed = True
        break
    if changed:
        _write_nodes(nodes, now)
    return {"updated": changed, "node": updated_node, "total": len(nodes)}


def delete_disabled_nodes() -> dict[str, Any]:
    nodes = load_nodes()
    disabled_keys = {node_key(str(n.get("raw_uri") or "")) for n in nodes if n.get("disabled")}
    if not disabled_keys:
        return {"deleted_count": 0, "deleted_keys": [], "total": len(nodes)}
    kept = [node for node in nodes if node_key(str(node.get("raw_uri") or "")) not in disabled_keys]
    now = int(time.time())
    _write_nodes(kept, now)
    health = load_health()
    kept_health = {k: v for k, v in health.items() if k not in disabled_keys}
    if kept_health != health:
        save_health(kept_health)
    return {"deleted_count": len(nodes) - len(kept), "deleted_keys": sorted(disabled_keys), "total": len(kept)}


def node_counts(nodes: list[dict[str, Any]] | None = None) -> dict[str, int]:
    items = load_nodes() if nodes is None else nodes
    disabled_count = sum(1 for node in items if node.get("disabled"))
    return {"total": len(items), "enabled_count": len(items) - disabled_count, "disabled_count": disabled_count}


def save_nodes(nodes: list[dict[str, Any]], source: str = "subscriptions") -> None:
    replace_nodes(nodes, source=source)


def load_health() -> dict[str, dict[str, Any]]:
    data = _read_json(NODE_HEALTH_FILE, {})
    if not isinstance(data, dict):
        return {}
    return {str(k): v for k, v in data.items() if isinstance(v, dict)}


def save_health(health: dict[str, dict[str, Any]]) -> None:
    _write_json(NODE_HEALTH_FILE, health)


def _health_for(health: dict[str, dict[str, Any]], raw_uri: str) -> dict[str, Any]:
    key = node_key(raw_uri)
    item = health.setdefault(key, {})
    item.setdefault("success_count", 0)
    item.setdefault("fail_count", 0)
    item.setdefault("consecutive_failures", 0)
    item.setdefault("avg_first_chunk_ms", 0.0)
    item.setdefault("last_success_at", 0)
    item.setdefault("last_fail_at", 0)
    item.setdefault("cooldown_until", 0)
    return item


def node_score(node: dict[str, Any], health: dict[str, Any], now: float | None = None) -> float:
    now = now or time.time()
    cooldown_until = float(health.get("cooldown_until") or 0)
    if cooldown_until > now:
        return float("-inf")

    success_count = int(health.get("success_count") or 0)
    fail_count = int(health.get("fail_count") or 0)
    consecutive_failures = int(health.get("consecutive_failures") or 0)
    avg_first_chunk_ms = float(health.get("avg_first_chunk_ms") or 0)
    last_success_at = float(health.get("last_success_at") or 0)
    last_fail_at = float(health.get("last_fail_at") or 0)
    last_seen = max(last_success_at, last_fail_at)

    score = 100.0
    score += min(success_count, 100) * 3.0
    score -= min(fail_count, 100) * 4.0
    score -= consecutive_failures * 25.0
    if avg_first_chunk_ms > 0:
        score -= min(avg_first_chunk_ms / 1000.0, 30.0)
    if not last_seen:
        score += 20.0
    elif now - last_seen > 3600:
        score += 10.0
    if str(node.get("usable_as_proxy", "")).lower() == "true" or node.get("usable_as_proxy") is True:
        score += 2.0
    return score


def select_nodes_for_parallel(cfg: dict[str, Any], requested_count: int) -> list[tuple[int, dict[str, Any]]]:
    nodes = [node for node in load_nodes() if not node.get("disabled")]
    if not nodes:
        return []

    health = load_health()
    now = time.time()
    top_k = int(cfg.get("parallel_node_top_k", 80) or 80)
    top_k = max(requested_count, min(top_k, len(nodes)))

    scored: list[tuple[float, int, dict[str, Any]]] = []
    cooled: list[tuple[float, int, dict[str, Any]]] = []
    for idx, node in enumerate(nodes):
        raw_uri = str(node.get("raw_uri") or "")
        h = _health_for(health, raw_uri)
        score = node_score(node, h, now)
        if math.isinf(score) and score < 0:
            cooled.append((score, idx, node))
        else:
            scored.append((score, idx, node))

    scored.sort(key=lambda x: x[0], reverse=True)
    candidates = scored[:top_k]
    if len(candidates) < requested_count:
        # 全部都在冷却时，允许少量冷却节点回补，避免彻底无节点可用。
        candidates.extend(cooled[:requested_count - len(candidates)])

    selected: list[tuple[int, dict[str, Any]]] = []
    pool = list(candidates)
    while pool and len(selected) < requested_count:
        weights = [max(1.0, item[0] + 120.0) if not math.isinf(item[0]) else 1.0 for item in pool]
        chosen = random.choices(pool, weights=weights, k=1)[0]
        pool.remove(chosen)
        _, idx, node = chosen
        selected.append((idx, dict(node)))
    return selected


def record_node_success(node: dict[str, Any], first_chunk_ms: float) -> None:
    raw_uri = str(node.get("raw_uri") or "").strip()
    if not raw_uri:
        return
    health = load_health()
    item = _health_for(health, raw_uri)
    item["success_count"] = int(item.get("success_count") or 0) + 1
    item["consecutive_failures"] = 0
    item["last_success_at"] = int(time.time())
    item["cooldown_until"] = 0
    old_avg = float(item.get("avg_first_chunk_ms") or 0)
    item["avg_first_chunk_ms"] = first_chunk_ms if old_avg <= 0 else old_avg * 0.7 + first_chunk_ms * 0.3
    save_health(health)


def record_node_failure(node: dict[str, Any], error: Exception | str) -> None:
    raw_uri = str(node.get("raw_uri") or "").strip()
    if not raw_uri:
        return
    health = load_health()
    item = _health_for(health, raw_uri)
    item["fail_count"] = int(item.get("fail_count") or 0) + 1
    item["consecutive_failures"] = int(item.get("consecutive_failures") or 0) + 1
    now = int(time.time())
    item["last_fail_at"] = now
    failures = max(1, int(item["consecutive_failures"]))
    cooldown = min(1800, 30 * (2 ** min(failures - 1, 6)))
    item["cooldown_until"] = now + cooldown
    item["last_error"] = str(error)[:500]
    save_health(health)


def _parse_node_identity(raw_uri: str) -> tuple | None:
    """从 raw_uri 提取 (scheme, uuid, server, port) 作为语义身份标识。"""
    try:
        if "://" not in raw_uri or "@" not in raw_uri:
            return None
        scheme = raw_uri.split("://", 1)[0]
        rest = raw_uri.split("://", 1)[1]
        userinfo = rest.split("@")[0]
        hostport = rest.split("@")[1].split("?")[0].split("#")[0]
        if ":" not in hostport:
            return None
        host, port_str = hostport.rsplit(":", 1)
        return (scheme, userinfo, host, int(port_str))
    except Exception:
        return None


def deduplicate_nodes() -> dict[str, int]:
    """一键去重：按 (scheme, uuid, server, port) 语义身份去重后写回。

    相同身份的多个节点只保留第一个，其余丢弃。
    同时清理已移除节点的健康记录。
    """
    nodes = load_nodes()
    if not nodes:
        return {"removed_count": 0, "total": 0}

    seen: set[Any] = set()
    deduped: list[dict[str, Any]] = []
    kept_raw_uris: set[str] = set()
    for node in nodes:
        raw_uri = str(node.get("raw_uri") or "")
        identity = _parse_node_identity(raw_uri)
        key: Any = identity if identity else node_key(raw_uri)
        if key is None or key in seen:
            continue
        seen.add(key)
        deduped.append(node)
        kept_raw_uris.add(node_key(raw_uri))

    removed = len(nodes) - len(deduped)
    now = int(time.time())
    _write_nodes(deduped, now)
    _prune_health(kept_raw_uris)
    return {"removed_count": removed, "total": len(deduped)}


def update_node_test_result(raw_uri: str, ok: bool, elapsed_ms: float, error: Exception | str = "", auto_disable: bool = True) -> dict[str, Any]:
    key = node_key(raw_uri)
    if not key:
        return {"updated": False, "node": None}

    error_text = str(error or "")[:500]
    health = load_health()
    item = _health_for(health, key)
    now = int(time.time())
    item["last_test_at"] = now
    item["last_test_ok"] = bool(ok)
    item["last_test_ms"] = round(float(elapsed_ms), 2)
    item["last_test_error"] = "" if ok else error_text
    if ok:
        item["success_count"] = int(item.get("success_count") or 0) + 1
        item["consecutive_failures"] = 0
        item["last_success_at"] = now
        item["cooldown_until"] = 0
    else:
        item["fail_count"] = int(item.get("fail_count") or 0) + 1
        item["consecutive_failures"] = int(item.get("consecutive_failures") or 0) + 1
        item["last_fail_at"] = now
        failures = max(1, int(item["consecutive_failures"]))
        item["cooldown_until"] = now + min(1800, 30 * (2 ** min(failures - 1, 6)))
        item["last_error"] = error_text
    save_health(health)

    if ok or auto_disable:
        node_update = set_node_disabled(key, False if ok else True, "" if ok else error_text)
        return {"updated": bool(node_update.get("updated")), "node": node_update.get("node"), "health": item}
    node = next((n for n in load_nodes() if node_key(str(n.get("raw_uri") or "")) == key), None)
    return {"updated": False, "node": node, "health": item}
