"""Точка входа: polling."""
import asyncio
import logging
from aiogram import Bot, Dispatcher
from aiogram.enums import ParseMode
from aiogram.client.default import DefaultBotProperties

from bot.config import config
from bot.db import init_db
from bot.handlers import basic, ingest
from bot.scheduler import setup_scheduler


async def main():
    logging.basicConfig(level=logging.INFO)
    if not config.bot_token or "put-your" in config.bot_token:
        raise SystemExit("Впиши BOT_TOKEN в .env (см. .env.example)")
    await init_db(config.db_path)
    bot = Bot(token=config.bot_token, default=DefaultBotProperties(parse_mode=ParseMode.HTML))
    dp = Dispatcher()
    dp.include_router(basic.router)
    dp.include_router(ingest.router)
    setup_scheduler(bot)
    mode = "GPT+Whisper" if config.llm_enabled else "heuristic (без OPENAI_API_KEY)"
    print(f"Bot started in {mode} mode, db={config.db_path}")
    await dp.start_polling(bot)


if __name__ == "__main__":
    asyncio.run(main())
