"""Оркестрация пайплайна: input → entry → blocks → entities → tasks → delegation-proposal.

Ядро только структурирует и предлагает. Исполнение — через плагины (delegate.py).
"""
from __future__ import annotations
from datetime import datetime
import re

from bot import db
from bot.services.analyze import analyze_text
from bot.services.entities import extract_entities
from bot.services.delegate import detect_delegation_intent, route_to_plugin

DUE_RE = re.compile(r"(завтра|сегодня|в понедельник|во вторник|в среду|в четверг|в пятницу|утром|вечером|к \d+|до \d+)", re.I)


def today_str() -> str:
    return datetime.now().strftime("%Y-%m-%d")


def guess_due(content: str) -> str:
    m = DUE_RE.search(content)
    return m.group(0) if m else ""


async def ingest_text(cfg, user_id: int, kind: str, raw: str, transcript: str) -> dict:
    """Полный цикл. Возвращает dict с entry_id, blocks, entities, task_ids, delegation."""
    text = (transcript or raw).strip()
    day = today_str()
    entry_id = await db.add_entry(cfg.db_path, user_id, day, kind, raw, transcript)

    blocks = await analyze_text(text, cfg.openai_api_key, cfg.openai_model)
    await db.add_blocks(cfg.db_path, entry_id, user_id, day, blocks)

    entities = extract_entities(text)
    await db.add_entities(cfg.db_path, entry_id, user_id, day, entities)

    task_ids = []
    for b in blocks:
        if b["aspect"] == "task":
            tid = await db.add_task(cfg.db_path, user_id, entry_id, b["content"], guess_due(b["content"]))
            task_ids.append(tid)
        elif b["aspect"] == "plan" and guess_due(b["content"]):
            tid = await db.add_task(cfg.db_path, user_id, entry_id, b["content"], guess_due(b["content"]))
            task_ids.append(tid)

    # Делегирование: только карточка + stub-маршрутизация, без реального исполнения
    delegation = None
    card = detect_delegation_intent(text)
    if card:
        card.context = text[:1000]
        card.source_entry_ids = [entry_id]
        # due пробуем вытащить из текста
        card.due = guess_due(text)
        agent_name, report = await route_to_plugin(card)
        did = await db.add_delegation(cfg.db_path, user_id, entry_id, card.title, card.context, card.due, agent_name)
        delegation = {"id": did, "agent": agent_name, "report": report, "title": card.title}

    return {
        "entry_id": entry_id,
        "blocks": blocks,
        "entities": entities,
        "task_ids": task_ids,
        "delegation": delegation,
    }
