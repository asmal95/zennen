"""Разбор текста по аспектам: GPT (если есть ключ) иначе heuristic.

Возвращает: list[{"aspect": ..., "content": ...}]
Аспекты: fact, thought, plan, task, idea, emotion, health, work, people, money, gratitude, decision, other
"""
from __future__ import annotations
import json
import re

ASPECTS = ["fact", "thought", "plan", "task", "idea", "emotion",
           "health", "work", "people", "money", "gratitude", "decision", "other"]

SYSTEM = (
    "Ты — структуризатор личного дневника. Разбей текст пользователя на блоки. "
    "Верни ТОЛЬКО JSON-массив: [{\"aspect\": \"...\", \"content\": \"...\"}]. "
    f"aspect строго из списка: {', '.join(ASPECTS)}. "
    "Правила: task — конкретное действие с датой/сроком; plan — намерение без даты; "
    "idea — озарение; emotion — чувства/состояние; fact — события; thought — размышления; "
    "health/work/people/money/gratitude/decision — по смыслу; other — остальное. "
    "Не выдумывай, сохраняй смысл, каждый блок 1-2 предложения."
)

TASK_RE = re.compile(r"(надо|нужно|сделать|позвонить|напомни|завтра|сегодня|к \d|до \d|купить|отправить|сдать|встретиться|записаться)", re.I)
IDEA_RE = re.compile(r"(идея|а что если|придумал|инсайт|мысль: сделать|стартап)", re.I)
EMO_RE = re.compile(r"(тревож|груст|рад|устал|злюсь|счастлив|стресс|энерги|настроени|выгоран|апати)", re.I)
HEALTH_RE = re.compile(r"(спал|сон|спорт|пробеж|трениров|болит|врач|таблет|съел|вес )", re.I)
MONEY_RE = re.compile(r"(потратил|купил за|зарплат|перевёл|оплатил|руб|бюджет|долг)", re.I)
GRAT_RE = re.compile(r"(спасибо|благодар)", re.I)
PLAN_RE = re.compile(r"(хочу|планирую|собираюсь|на выходных|в следующем|мечтаю|намерен)", re.I)
WORK_RE = re.compile(r"(созвон|проект|заказчик|дедлайн|отчёт|коллега|#\w+)", re.I)


def heuristic_split(text: str) -> list[dict]:
    parts = [p.strip() for p in re.split(r"[.\n!;]+", text) if p.strip()]
    out = []
    for p in parts:
        low = p.lower()
        if TASK_RE.search(p):
            aspect = "task"
        elif IDEA_RE.search(p):
            aspect = "idea"
        elif EMO_RE.search(p):
            aspect = "emotion"
        elif HEALTH_RE.search(p):
            aspect = "health"
        elif MONEY_RE.search(p):
            aspect = "money"
        elif GRAT_RE.search(p):
            aspect = "gratitude"
        elif PLAN_RE.search(p):
            aspect = "plan"
        elif WORK_RE.search(p):
            aspect = "work"
        elif re.search(r"(решил|решение|вывод)", low):
            aspect = "decision"
        elif re.search(r"(мама|папа|жена|муж|друг|коллега|@\w+)", low):
            aspect = "people"
        elif re.search(r"(думаю|кажется|похоже|размышляю)", low):
            aspect = "thought"
        else:
            aspect = "fact"
        out.append({"aspect": aspect, "content": p})
    return out or [{"aspect": "other", "content": text}]


async def analyze_text(text: str, api_key: str = "", model: str = "gpt-4o-mini") -> list[dict]:
    text = text.strip()
    if not text:
        return []
    if not api_key:
        return heuristic_split(text)
    try:
        from openai import AsyncOpenAI

        client = AsyncOpenAI(api_key=api_key)
        resp = await client.chat.completions.create(
            model=model,
            messages=[
                {"role": "system", "content": SYSTEM},
                {"role": "user", "content": text[:4000]},
            ],
            temperature=0.2,
        )
        raw = resp.choices[0].message.content.strip()
        raw = re.sub(r"^```json|^```|```$", "", raw.strip(), flags=re.M)
        data = json.loads(raw)
        out = []
        for b in data:
            a = b.get("aspect", "other")
            c = b.get("content", "").strip()
            if not c:
                continue
            if a not in ASPECTS:
                a = "other"
            out.append({"aspect": a, "content": c})
        return out or heuristic_split(text)
    except Exception:
        return heuristic_split(text)
