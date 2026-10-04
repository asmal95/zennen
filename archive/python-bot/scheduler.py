"""Утренние/вечерние напоминания через APScheduler."""
from apscheduler.schedulers.asyncio import AsyncIOScheduler
from aiogram import Bot
from datetime import datetime

from bot import db
from bot.config import config
from bot.services.render import render_tasks


async def morning(bot: Bot):
    for uid in await db.distinct_users(config.db_path):
        try:
            day = datetime.now().strftime("%Y-%m-%d")
            blocks = await db.day_blocks(config.db_path, uid, day)
            tasks = await db.open_tasks(config.db_path, uid)
            n_today = len([b for b in blocks if b["aspect"] in ("task", "plan")])
            await bot.send_message(
                uid,
                f"☀️ Доброе утро! Сегодня уже {len(blocks)} записей.\n\n"
                + render_tasks(tasks)
                + "\n\nНаговори планы на день голосом 🎙",
            )
        except Exception:
            continue


async def evening(bot: Bot):
    for uid in await db.distinct_users(config.db_path):
        try:
            day = datetime.now().strftime("%Y-%m-%d")
            blocks = await db.day_blocks(config.db_path, uid, day)
            open_n = len(await db.open_tasks(config.db_path, uid))
            await bot.send_message(
                uid,
                f"🌙 Вечер. Сегодня {len(blocks)} записей, открытых задач: {open_n}.\n"
                "Что было главным? Наговори 1–2 минуты — я сохраню как рефлексию дня.",
            )
        except Exception:
            continue


def setup_scheduler(bot: Bot) -> AsyncIOScheduler:
    s = AsyncIOScheduler(timezone=config.tz)
    s.add_job(morning, "cron", hour=config.morning_hour, minute=0, args=[bot])
    s.add_job(evening, "cron", hour=config.evening_hour, minute=0, args=[bot])
    s.start()
    return s
