"""Форматирование ответов."""
from collections import Counter

EMOJI = {
    "fact": "🕒", "thought": "💭", "plan": "📌", "task": "✅", "idea": "💡",
    "emotion": "❤️", "health": "🏃", "work": "💼", "people": "👥",
    "money": "💰", "gratitude": "🙏", "decision": "⚖️", "other": "📝",
}

NAMES = {
    "fact": "Факты", "thought": "Мысли", "plan": "Планы", "task": "Задачи",
    "idea": "Идеи", "emotion": "Состояние", "health": "Здоровье", "work": "Работа",
    "people": "Люди", "money": "Финансы", "gratitude": "Благодарности",
    "decision": "Решения", "other": "Прочее",
}


def render_blocks(blocks: list[dict]) -> str:
    return "\n".join(f"{EMOJI.get(b['aspect'], '•')} <b>{NAMES.get(b['aspect'], b['aspect'])}</b>: {b['content']}" for b in blocks)


def render_day(day: str, blocks: list[dict]) -> str:
    if not blocks:
        return f"📅 <b>{day}</b>\nПока пусто. Пришли голосовое — начнём историю дня."
    c = Counter(b["aspect"] for b in blocks)
    header = f"📅 <b>{day}</b> — записей: {len(blocks)} ({', '.join(f'{EMOJI[k]} {v}' for k, v in c.most_common(4))})"
    return header + "\n\n" + render_blocks(blocks[-30:])


def render_tasks(tasks: list[dict]) -> str:
    if not tasks:
        return "✅ Открытых задач нет. Так держать!"
    lines = [f"#{t['id']} — {t['text']}" + (f" <i>({t['due']})</i>" if t.get("due") else "") for t in tasks]
    return "✅ <b>Открытые задачи:</b>\n" + "\n".join(lines) + "\n\nЗакрыть: /done &lt;id&gt;"
