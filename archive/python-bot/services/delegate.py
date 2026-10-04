"""Делегирование внешнему агенту-исполнителю — только через плагин.

Ядро (диктофон-дневник) НЕ выполняет задачи само. Оно готовит DelegationCard
и складывает её в БД со статусом proposed. Внешний агент подключается позже
через BaseAgentPlugin без изменения ядра.

Статусы: proposed → approved → done / rejected
"""
from __future__ import annotations
import re
from dataclasses import dataclass, field

DELEGATE_RE = re.compile(
    r"(поручи|передай|отдай|делегируй).{0,20}(агент|календар|кодер|боту|ассистент)|"
    r"(пусть|попроси).{0,30}(сделает|забронирует|найдёт|найдет|напишет|создаст)",
    re.I,
)


@dataclass
class DelegationCard:
    title: str
    context: str = ""
    due: str = ""
    entities: dict = field(default_factory=dict)
    source_entry_ids: list[int] = field(default_factory=list)


def detect_delegation_intent(text: str) -> DelegationCard | None:
    """Эвристика MVP: если в голосовом звучит 'поручи/передай агенту' — собрать карточку."""
    if not DELEGATE_RE.search(text or ""):
        return None
    title = text.strip()[:200]
    return DelegationCard(title=title, context=text.strip()[:1000])


class BaseAgentPlugin:
    """Интерфейс внешнего исполнителя. Реализуй в отдельном пакете и зарегистрируй."""

    name: str = "base"

    async def can_handle(self, card: DelegationCard) -> bool:
        return False

    async def handle(self, card: DelegationCard) -> str:
        raise NotImplementedError


class NoOpPlugin(BaseAgentPlugin):
    """Заглушка по умолчанию: ничего не делает, только фиксирует факт."""

    name: str = "noop"

    async def can_handle(self, card: DelegationCard) -> bool:
        return True

    async def handle(self, card: DelegationCard) -> str:
        return f"[{self.name}] Карточка «{card.title[:80]}» принята в очередь. Реальный агент не подключён."


class ExampleCalendarPlugin(BaseAgentPlugin):
    """Пример будущего плагина: 'отдай агенту-календарю'."""

    name: str = "calendar"

    async def can_handle(self, card: DelegationCard) -> bool:
        t = (card.title + card.context).lower()
        return any(w in t for w in ("встреч", "созвон", "календар", "напомни", "запланируй"))

    async def handle(self, card: DelegationCard) -> str:
        # TODO: здесь будет реальный вызов внешнего агента (Calendar API)
        return f"[{self.name}] (stub) Создал бы встречу: «{card.title[:80]}» срок: {card.due or '—'}"


REGISTRY: list[BaseAgentPlugin] = [ExampleCalendarPlugin(), NoOpPlugin()]


async def route_to_plugin(card: DelegationCard) -> tuple[str, str]:
    """Выбрать первый подходящий плагин. Возвращает (agent_name, report)."""
    for p in REGISTRY:
        if await p.can_handle(card):
            return p.name, await p.handle(card)
    return "noop", "Нет подходящего плагина."
