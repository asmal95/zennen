"""Выделение сущностей из расшифровки (heuristic MVP, позже LLM+NER).

Сущности — клей между днями: люди, проекты, места, даты, суммы, обещания.
"""
from __future__ import annotations
import re

HASHTAG_RE = re.compile(r"#([\w\-а-яёА-ЯЁ]+)", re.U)
MENTION_RE = re.compile(r"@([\w\-а-яёА-ЯЁ]+)", re.U)
MONEY_RE = re.compile(r"(\d[\d\s]*\s?(руб|рублей|₽|\$|€|доллар|евро))", re.I)
DATE_RE = re.compile(
    r"(сегодня|завтра|послезавтра|в понедельник|во вторник|в среду|в четверг|в пятницу|в субботу|в воскресенье|"
    r"утром|днём|вечером|ночью|к \d{1,2}(:\d{2})?|до \d{1,2}|через \d+ (час|день|дня|дней|недел)|на выходных|на следующей неделе)",
    re.I,
)
# грубые эвристики для MVP
PEOPLE_HINT_RE = re.compile(r"(мама|папа|жена|муж|сын|дочь|брат|сестра|друг|подруга|коллега|начальник|созвон с|встреча с|позвонить)\s+([А-ЯЁ][а-яё]+)", re.U)
PLACE_HINT_RE = re.compile(r"(в|на|из|у|к)\s+([А-ЯЁ][а-яё]{3,}(?:\s+[А-ЯЁ][а-яё]+)?)", re.U)
PROMISE_RE = re.compile(r"(обещал[а-я]*|договорил[а-яйся]*|пообещал[а-я]*)\s+(.+?)(?=[.!]|$)", re.I)


def extract_entities(text: str) -> list[dict]:
    """Возвращает [{"type": ..., "value": ...}]. Дубликаты убираются."""
    out: list[dict] = []
    seen = set()

    def add(etype: str, value: str):
        v = value.strip().strip(",. ")[:120]
        if not v or len(v) < 2:
            return
        key = (etype, v.lower())
        if key in seen:
            return
        seen.add(key)
        out.append({"type": etype, "value": v})

    for m in HASHTAG_RE.finditer(text):
        add("project", "#" + m.group(1))
    for m in MENTION_RE.finditer(text):
        add("person", "@" + m.group(1))
    for m in MONEY_RE.finditer(text):
        add("money", m.group(1))
    for m in DATE_RE.finditer(text):
        add("date", m.group(1))
    for m in PEOPLE_HINT_RE.finditer(text):
        add("person", m.group(2))
    for m in PROMISE_RE.finditer(text):
        add("promise", m.group(0)[:120])
    # места — только с осторожностью: берём первые 2 кандидата, чтобы не шуметь
    for m in list(PLACE_HINT_RE.finditer(text))[:2]:
        cand = m.group(2)
        if cand.lower() not in ("понедельник", "вторник", "среду", "четверг", "пятницу"):
            add("place", cand)
    return out
