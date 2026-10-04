"""Транскрибация: Whisper API, иначе заглушка."""
from __future__ import annotations


async def transcribe_ogg(path: str, api_key: str = "", model: str = "whisper-1") -> str:
    if not api_key:
        return ""
    try:
        from openai import AsyncOpenAI

        client = AsyncOpenAI(api_key=api_key)
        with open(path, "rb") as f:
            resp = await client.audio.transcriptions.create(model=model, file=f, language="ru")
        return resp.text.strip()
    except Exception:
        return ""
