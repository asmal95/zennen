"""Приём voice/text → пайплайн → короткий ответ. Voice-first: текст — fallback."""
import os
import tempfile
from aiogram import Router, F, Bot
from aiogram.types import Message

from bot.config import config
from bot.services.pipeline import ingest_text
from bot.services.stt import transcribe_ogg
from bot.services.render import render_blocks

router = Router()

ENTITY_EMOJI = {"person": "👥", "project": "#️⃣", "place": "📍", "date": "📅", "money": "💰", "promise": "🤝"}


def render_result(prefix: str, res: dict) -> str:
    blocks = res["blocks"]
    entities = res["entities"]
    task_ids = res["task_ids"]
    out = f"{prefix}\n\n{render_blocks(blocks)}" if blocks else prefix + "\n\n(пусто)"
    if entities:
        ents = " ".join(f"{ENTITY_EMOJI.get(e['type'], '•')}{e['value']}" for e in entities[:8])
        out += f"\n\n🔎 Сущности: {ents}"
    if task_ids:
        out += f"\n\n✅ В задачи: {len(task_ids)} (см. /tasks)"
    d = res.get("delegation")
    if d:
        out += (
            f"\n\n🤖 Похоже, это можно <b>поручить агенту</b> ({d['agent']}): «{d['title'][:100]}»\n"
            f"Карточка #{d['id']} сохранена со статусом proposed. "
            "Подтверди голосом «да, поручи» — и в будущем её заберёт внешний агент. "
            "Список: /delegations"
        )
    return out


@router.message(F.voice | F.audio | F.video_note)
async def on_voice(m: Message, bot: Bot):
    media = m.voice or m.audio or m.video_note
    with tempfile.NamedTemporaryFile(suffix=".ogg", delete=False) as tmp:
        path = tmp.name
    try:
        await bot.download(media.file_id, destination=path)
        transcript = await transcribe_ogg(path, config.openai_api_key, config.stt_model)
    finally:
        if os.path.exists(path):
            os.remove(path)
    if not transcript:
        await m.answer(
            "🎙 Получил голосовое, но расшифровать не смог "
            "(нет OPENAI_API_KEY или ошибка STT).\n"
            "Пришли то же текстом — я всё равно разложу по аспектам."
        )
        return
    res = await ingest_text(config, m.from_user.id, "voice", "", transcript)
    await m.answer(render_result(f"🎧 <b>Расшифровка:</b> {transcript[:500]}", res))


@router.message(F.text & ~F.text.startswith("/"))
async def on_text(m: Message):
    res = await ingest_text(config, m.from_user.id, "text", m.text, m.text)
    await m.answer(render_result("📝 Разложил:", res))
