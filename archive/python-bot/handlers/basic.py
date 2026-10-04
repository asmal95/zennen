"""Команды просмотра: start/help/today/week/tasks/ideas/search/done/notes/delegations."""
from aiogram import Router
from aiogram.filters import Command
from aiogram.types import Message
from datetime import datetime, timedelta

from bot import db
from bot.config import config
from bot.services.render import render_day, render_tasks

router = Router()

HELP = (
    "🎙 <b>ИИ-Диктофон Дневника</b> (voice-first, текст тоже ок)\n\n"
    "Каждый день излагай мысли <b>голосом или текстом</b> — как удобно. "
    "Голос — основной способ, текст — равноправный. "
    "Я сам веду журнал: заметки, сущности (люди/проекты/даты), "
    "📌 планы, ✅ задачи, ⏰ напоминания.\n\n"
    "/today — сегодня\n"
    "/notes — последние заметки\n"
    "/week — 7 дней\n"
    "/tasks — открытые задачи\n"
    "/done &lt;id&gt; — закрыть задачу\n"
    "/ideas — банк идей\n"
    "/delegations — карточки для внешнего агента\n"
    "/search &lt;запрос&gt; — поиск\n"
    "/help — это сообщение\n\n"
    "<i>Чтобы что-то поручить агенту-исполнителю, напиши или скажи: "
    "«поручи агенту …» — я сохраню карточку, исполнение подключится позже плагином.</i>"
)


@router.message(Command("start", "help"))
async def start(m: Message):
    await m.answer(HELP)


@router.message(Command("today"))
async def today(m: Message):
    day = datetime.now().strftime("%Y-%m-%d")
    blocks = await db.day_blocks(config.db_path, m.from_user.id, day)
    await m.answer(render_day(day, blocks))


@router.message(Command("notes"))
async def notes(m: Message):
    rows = await db.recent_notes(config.db_path, m.from_user.id, 10)
    if not rows:
        await m.answer("📝 Заметок пока нет. Наговори первое голосовое 🎙")
        return
    lines = ["📝 <b>Последние заметки:</b>"]
    for r in rows:
        preview = (r["transcript"] or "")[:120] or "(голосовое без расшифровки)"
        lines.append(f"#{r['id']} <i>{r['day']}</i> [{r['kind']}] {preview}")
    await m.answer("\n".join(lines))


@router.message(Command("week"))
async def week(m: Message):
    days = [(datetime.now() - timedelta(days=i)).strftime("%Y-%m-%d") for i in range(6, -1, -1)]
    rows = await db.week_blocks(config.db_path, m.from_user.id, days)
    if not rows:
        await m.answer("📅 За неделю пусто. Начни с одного голосового сегодня.")
        return
    from collections import Counter
    c = Counter(r["aspect"] for r in rows)
    lines = [f"📅 <b>Неделя</b>: {len(rows)} блоков"]
    for aspect, n in c.most_common():
        lines.append(f"• {aspect}: {n}")
    lines.append("\nПоследнее:")
    for r in rows[-10:]:
        lines.append(f"<i>{r['day']}</i> [{r['aspect']}] {r['content'][:120]}")
    await m.answer("\n".join(lines))


@router.message(Command("tasks"))
async def tasks(m: Message):
    await m.answer(render_tasks(await db.open_tasks(config.db_path, m.from_user.id)))


@router.message(Command("done"))
async def done(m: Message):
    parts = (m.text or "").split()
    if len(parts) < 2 or not parts[1].isdigit():
        await m.answer("Использование: /done &lt;id&gt; (см. /tasks)")
        return
    ok = await db.close_task(config.db_path, m.from_user.id, int(parts[1]))
    await m.answer("✅ Закрыта!" if ok else "❌ Не нашёл такую задачу.")


@router.message(Command("ideas"))
async def ideas(m: Message):
    rows = await db.blocks_by_aspect(config.db_path, m.from_user.id, "idea")
    if not rows:
        await m.answer("💡 Идей пока нет. Поделись первой!")
        return
    await m.answer("💡 <b>Банк идей:</b>\n" + "\n".join(f"<i>{r['day']}</i> — {r['content']}" for r in rows))


@router.message(Command("delegations"))
async def delegations(m: Message):
    rows = await db.list_delegations(config.db_path, m.from_user.id)
    if not rows:
        await m.answer(
            "🤖 Карточек делегирования нет.\n"
            "Скажи голосом «поручи агенту …» — я сохраню карточку здесь. "
            "Реальное исполнение подключится позже отдельным плагином."
        )
        return
    lines = ["🤖 <b>Делегирование (только карточки, исполнения нет):</b>"]
    for r in rows:
        lines.append(f"#{r['id']} [{r['status']}/{r['agent']}] {r['title'][:120]}")
    await m.answer("\n".join(lines))


@router.message(Command("search"))
async def search(m: Message):
    q = (m.text or "").partition(" ")[2].strip()
    if not q:
        await m.answer("Использование: /search &lt;запрос&gt;")
        return
    rows = await db.search_blocks(config.db_path, m.from_user.id, q)
    if not rows:
        await m.answer(f"🔍 По «{q}» ничего не нашёл.")
        return
    await m.answer(f"🔍 <b>{q}</b>:\n" + "\n".join(f"<i>{r['day']}</i> [{r['aspect']}] {r['content'][:150]}" for r in rows))
