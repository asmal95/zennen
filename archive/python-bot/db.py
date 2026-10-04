"""SQLite-слой MVP: entries (заметки) / blocks (аспекты) / entities / tasks / reminders / delegations."""
import aiosqlite
import os
from datetime import datetime

SCHEMA = """
CREATE TABLE IF NOT EXISTS entries(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  kind TEXT NOT NULL,
  raw_text TEXT DEFAULT '',
  transcript TEXT DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_entries_user_day ON entries(user_id, day);
CREATE TABLE IF NOT EXISTS blocks(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  entry_id INTEGER NOT NULL REFERENCES entries(id),
  user_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  aspect TEXT NOT NULL,
  content TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_blocks_user_aspect ON blocks(user_id, aspect);
CREATE TABLE IF NOT EXISTS tasks(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  entry_id INTEGER REFERENCES entries(id),
  text TEXT NOT NULL,
  due TEXT DEFAULT '',
  done INTEGER DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_user_done ON tasks(user_id, done);
CREATE TABLE IF NOT EXISTS entities(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  entry_id INTEGER NOT NULL REFERENCES entries(id),
  user_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  type TEXT NOT NULL,
  value TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_entities_user_type ON entities(user_id, type);
CREATE TABLE IF NOT EXISTS reminders(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER REFERENCES tasks(id),
  user_id INTEGER NOT NULL,
  fire_at TEXT NOT NULL,
  kind TEXT DEFAULT 'deadline',
  sent INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS delegations(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  entry_id INTEGER REFERENCES entries(id),
  title TEXT NOT NULL,
  context TEXT DEFAULT '',
  due TEXT DEFAULT '',
  agent TEXT DEFAULT 'noop',
  status TEXT DEFAULT 'proposed',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_deleg_user_status ON delegations(user_id, status);
"""


async def init_db(db_path: str):
    os.makedirs(os.path.dirname(db_path) or ".", exist_ok=True)
    async with aiosqlite.connect(db_path) as db:
        await db.executescript(SCHEMA)
        await db.commit()


async def add_entry(db_path, user_id, day, kind, raw_text, transcript) -> int:
    async with aiosqlite.connect(db_path) as db:
        cur = await db.execute(
            "INSERT INTO entries(user_id, day, kind, raw_text, transcript, created_at)"
            " VALUES(?,?,?,?,?,?)",
            (user_id, day, kind, raw_text, transcript, datetime.now().isoformat()),
        )
        await db.commit()
        return cur.lastrowid


async def add_blocks(db_path, entry_id, user_id, day, blocks: list[dict]):
    async with aiosqlite.connect(db_path) as db:
        for b in blocks:
            await db.execute(
                "INSERT INTO blocks(entry_id, user_id, day, aspect, content) VALUES(?,?,?,?,?)",
                (entry_id, user_id, day, b["aspect"], b["content"]),
            )
        await db.commit()


async def add_task(db_path, user_id, entry_id, text, due="") -> int:
    async with aiosqlite.connect(db_path) as db:
        cur = await db.execute(
            "INSERT INTO tasks(user_id, entry_id, text, due, done, created_at)"
            " VALUES(?,?,?,?,0,?)",
            (user_id, entry_id, text, due, datetime.now().isoformat()),
        )
        await db.commit()
        return cur.lastrowid


async def day_blocks(db_path, user_id, day):
    async with aiosqlite.connect(db_path) as db:
        db.row_factory = aiosqlite.Row
        async with db.execute(
            "SELECT aspect, content FROM blocks WHERE user_id=? AND day=? ORDER BY id",
            (user_id, day),
        ) as cur:
            return [dict(r) for r in await cur.fetchall()]


async def week_blocks(db_path, user_id, days: list[str]):
    async with aiosqlite.connect(db_path) as db:
        db.row_factory = aiosqlite.Row
        q = f"SELECT day, aspect, content FROM blocks WHERE user_id=? AND day IN ({','.join('?'*len(days))}) ORDER BY day, id"
        async with db.execute(q, (user_id, *days)) as cur:
            return [dict(r) for r in await cur.fetchall()]


async def open_tasks(db_path, user_id):
    async with aiosqlite.connect(db_path) as db:
        db.row_factory = aiosqlite.Row
        async with db.execute(
            "SELECT id, text, due FROM tasks WHERE user_id=? AND done=0 ORDER BY id",
            (user_id,),
        ) as cur:
            return [dict(r) for r in await cur.fetchall()]


async def close_task(db_path, user_id, task_id) -> bool:
    async with aiosqlite.connect(db_path) as db:
        cur = await db.execute(
            "UPDATE tasks SET done=1 WHERE id=? AND user_id=?", (task_id, user_id)
        )
        await db.commit()
        return cur.rowcount > 0


async def blocks_by_aspect(db_path, user_id, aspect, limit=20):
    async with aiosqlite.connect(db_path) as db:
        db.row_factory = aiosqlite.Row
        async with db.execute(
            "SELECT day, content FROM blocks WHERE user_id=? AND aspect=? ORDER BY id DESC LIMIT ?",
            (user_id, aspect, limit),
        ) as cur:
            return [dict(r) for r in await cur.fetchall()]


async def search_blocks(db_path, user_id, query, limit=20):
    async with aiosqlite.connect(db_path) as db:
        db.row_factory = aiosqlite.Row
        async with db.execute(
            "SELECT day, aspect, content FROM blocks WHERE user_id=? AND content LIKE ? ORDER BY id DESC LIMIT ?",
            (user_id, f"%{query}%", limit),
        ) as cur:
            return [dict(r) for r in await cur.fetchall()]


async def distinct_users(db_path) -> list[int]:
    async with aiosqlite.connect(db_path) as db:
        async with db.execute("SELECT DISTINCT user_id FROM entries") as cur:
            return [r[0] for r in await cur.fetchall()]


async def add_entities(db_path, entry_id, user_id, day, entities: list[dict]):
    if not entities:
        return
    async with aiosqlite.connect(db_path) as db:
        for e in entities:
            await db.execute(
                "INSERT INTO entities(entry_id, user_id, day, type, value) VALUES(?,?,?,?,?)",
                (entry_id, user_id, day, e["type"], e["value"]),
            )
        await db.commit()


async def entities_by_type(db_path, user_id, etype, limit=20):
    async with aiosqlite.connect(db_path) as db:
        db.row_factory = aiosqlite.Row
        async with db.execute(
            "SELECT day, value FROM entities WHERE user_id=? AND type=? ORDER BY id DESC LIMIT ?",
            (user_id, etype, limit),
        ) as cur:
            return [dict(r) for r in await cur.fetchall()]


async def add_delegation(db_path, user_id, entry_id, title, context="", due="", agent="noop") -> int:
    async with aiosqlite.connect(db_path) as db:
        cur = await db.execute(
            "INSERT INTO delegations(user_id, entry_id, title, context, due, agent, status, created_at)"
            " VALUES(?,?,?,?,?,?,?,?)",
            (user_id, entry_id, title, context, due, agent, "proposed", datetime.now().isoformat()),
        )
        await db.commit()
        return cur.lastrowid


async def list_delegations(db_path, user_id, limit=20):
    async with aiosqlite.connect(db_path) as db:
        db.row_factory = aiosqlite.Row
        async with db.execute(
            "SELECT id, title, agent, status, due FROM delegations WHERE user_id=? ORDER BY id DESC LIMIT ?",
            (user_id, limit),
        ) as cur:
            return [dict(r) for r in await cur.fetchall()]


async def recent_notes(db_path, user_id, limit=10):
    """Лента заметок: последние entries с первым блоком-превью."""
    async with aiosqlite.connect(db_path) as db:
        db.row_factory = aiosqlite.Row
        async with db.execute(
            "SELECT id, day, kind, transcript, created_at FROM entries"
            " WHERE user_id=? ORDER BY id DESC LIMIT ?",
            (user_id, limit),
        ) as cur:
            return [dict(r) for r in await cur.fetchall()]
