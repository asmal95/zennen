"""Конфиг из .env."""
import os
from dataclasses import dataclass
from dotenv import load_dotenv

load_dotenv()


@dataclass
class Config:
    bot_token: str = os.getenv("BOT_TOKEN", "")
    openai_api_key: str = os.getenv("OPENAI_API_KEY", "")
    openai_model: str = os.getenv("OPENAI_MODEL", "gpt-4o-mini")
    stt_model: str = os.getenv("OPENAI_STT_MODEL", "whisper-1")
    db_path: str = os.getenv("DB_PATH", "data/diary.db")
    tz: str = os.getenv("TZ", "Europe/Moscow")
    morning_hour: int = int(os.getenv("MORNING_HOUR", "9"))
    evening_hour: int = int(os.getenv("EVENING_HOUR", "21"))

    @property
    def llm_enabled(self) -> bool:
        return bool(self.openai_api_key)


config = Config()
