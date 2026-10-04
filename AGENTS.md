# AGENTS.md — инструкция для агента (ИИ-Диктофон Дневника)

Telegram-бот «дневник-диктофон» на **Go**: пользователь каждый день излагает мысли
**голосом или текстом** (голос — основной способ, текст — равноправный, не fallback),
а бот автоматически ведёт журнал: заметки → разбор по аспектам + дата событий →
сущности → задачи/напоминания → карточки делегирования. Исполнения задач в ядре НЕТ —
только структура и память, внешние агенты подключаются позже плагинами.

## 1. Структура

```
ai-dictaphone-diary/
  cmd/bot/main.go            # точка входа: polling + планировщик (требует BOT_TOKEN и OPENAI_API_KEY, иначе exit)
  internal/
    config/config.go         # конфиг из .env (BOT_TOKEN, OPENAI_* incl. BASE_URL, TZ-дефолт, DB_PATH, ...); TZ персональный — в таблице users, не в конфиге
    db/db.go                 # SQLite (modernc.org/sqlite, pure Go): entries/blocks/entities/tasks/reminders/delegations/energy/users/web_sessions/week_cache/plans + миграции ALTER TABLE в Open()
    services/
      llm.go                 # NewLLMClient: OpenAI-совместимый клиент на OPENAI_BASE_URL
      stt.go                 # Whisper API; "" при ошибке, хендлер просит текстом
      analyze.go             # AnalyzeFull: только LLM (блоки + entry_date + target_date); ошибка → ErrAnalyze. Промпт: systemPromptBase + PastStrs/FutureStrs + weekdayTable(Future) + требование сохранять слова-даты в тексте блока. targetOverride: явные маркеры резолвит Go поверх LLM (LLM ошибался даже на «завтра» 3/3); messageAnchor: голые «вечером» и безъякорные plan/task-блоки наследуют якорь всего сообщения (ловили «вечер отдельно» + выдуманную LLM дату 10-03). Факты прошлого не притягиваем.
      entities.go            # люди/проекты/места/даты/суммы/обещания (regex, RE2!)
      remind.go              # RemindIntentRe + ParseRemindAt («через N», «в H:MM», «завтра», части дня; границы — явными классами, НЕ \b)
      pipeline.go            # IngestText (анализ → дата → AddEntry → analyzeAndStore), ReanalyzeEntry (день сохраняется), ResolveEntryDate (клямп: прошлое ≤ года, не будущее)
      review.go              # BuildWeeklyReview: данные 7 дней → 1 LLM-вызов, plain text, ErrNoData если пусто
      reconcile.go           # ReconcileDay: планы+задачи дня vs записи → JSON done/missed/report, статусы только из своих open-id
      people.go              # MatchName (стемминг падежей, префикс-не-подстрока) + EntityHistory для /person /project
      users.go               # UserLoc (зона юзера, фолбэки) + DigestDue (чистая функция диспетчера)
      energy.go              # EnergySparkline + EnergyKeyboard(день); колбэк "energy:ГГГГ-ММ-ДД:N" в handlers
      delegate.go            # DelegationCard + AgentPlugin (NoOp/Calendar-заглушки, без исполнения)
      render.go              # RenderBlocks/RenderDay/RenderTasks (HTML); EmojiFor живёт в export.go
      export.go              # EmojiFor + RenderExportMarkdown (Obsidian/Notion)
    handlers/handlers.go     # приём voice/audio/кружков/text + команды + колбэки (task:/edit:/del:/del_yes/del_no/energy:, pendingEdit в памяти со сбросом на команду/голос) + /link + /revoke (сессии в Sess store)
    scheduler/scheduler.go   # ежеминутный dispatchDigests (локальный час юзера + DigestDue + MarkDigest) + ежеминутно due-reminders + ежедневно 3:00 DailyBackup (VACUUM INTO, ротация 14)
  cmd/web/main.go              # веб-вьювер: diary.db read-only + sessions.db RW (см. WEB.md)
  internal/web/                # server.go (auth, сессии, acolor), pages.go (GET incl. timeline/tree), actions.go (POST + checkOrigin), templates/*.html (embed)
  archive/python-bot/        # первый прототип. НЕ править, только смотреть как референс.
  data/                      # diary.db + backups/ — рантайм, НЕ коммитить, не читать без нужды
  .env                       # секреты — НЕ коммитить (в .gitignore)
  CONCEPT.md / ROADMAP.md / README.md  # доки концепции, плана и пользователя
  Dockerfile / docker-compose.yml / .env.example / go.mod / diarybot(бинарь, в .gitignore)
  AGENTS.md                  # этот файл
```

Команды бота: `/today /day /notes /week /tasks /done /plans /plandone /ideas /energy /person /project /delegations /search /export /delete_day /link /revoke /tz /help` + кнопки под разбором (✅/✏️/🗑), шкала энергии 1–10, кнопки зоны (tz:). Прод: `diarybot` + `diaryweb` под systemd, nginx `nen.zenai.space` → 127.0.0.1:8070 (сертификат certbot).

## 2. Запуск и проверка (обязательно после правок)

```sh
go vet ./... && go build -o diarybot ./cmd/bot && echo OK   # бинарь — в корень проекта (в .gitignore)
```

Прод-бот крутится под systemd (`/etc/systemd/system/diarybot.service`,
`WorkingDirectory` = корень проекта, `Restart=always`, enabled):

```sh
systemctl restart diarybot   # после сборки нового бинаря
systemctl is-active diarybot && journalctl -u diarybot -n 5
```

- НЕ запускать второй экземпляр через `go run`/`nohup` рядом с сервисом — два polling-процесса на один токен конфликтуют.
- Живые проверки — временным `tmpcheck/main.go` (каталог моделей OpenRouter, AnalyzeFull, транскрипция семпла, IngestText на `/tmp/*.db`), потом **удалить `tmpcheck/`**. Токены только из `.env` через `config.Load()` — никогда в командной строке.
- `BOT_TOKEN`, `OPENAI_API_KEY` и весь `.env` никогда не коммитить и не показывать пользователю. `data/` (БД с личными записями) и бинарь тоже вне git (см. `.gitignore`).

## 3. Git

- Remote: `origin = git@github.com:asmal95/zennen.git` (SSH-ключ `~/.ssh/github_manul`, `GIT_SSH_COMMAND="ssh -i ~/.ssh/github_manul -o BatchMode=yes"`). Ветка `main`, push — только по явной просьбе пользователя.
- Коммиты: короткие, в стиле репо (см. initial commit).
- Перед коммитом: `gofmt -l` чисто, `go vet` чисто, сборка ок, `git status` — убедиться, что нет `.env`, `data/`, бинаря, `tmpcheck/`.
- Секреты в истории искать так: `grep -rni "sk-or-\|AAGT" --include="*.go" --include="*.md" .` (исключая `.env`, которого в репо нет).

## 4. Грабли (не наступать повторно)

- **Regex — только RE2**: в Go нет lookahead `(?=...)` / lookbehind. Плюс `\b` — ASCII-only и НЕ работает вокруг кириллицы (ловили на «через 1 час» и «позавтракать»): границы слов — явными классами `[^а-яёА-ЯЁa-zA-Z0-9]`.
- **`modernc.org/sqlite` собирается минутами** при первой сборке (тяжёлый кодген).
  Это норма; повторные сборки кэшируются. Не «чинить» заменой драйвера без спроса.
- Go-toolchain в окружении может быть не на PATH (ставился в `/tmp/go`). Если `go`
  не найден — искать там, прежде чем переустанавливать.
- Шаблоны `html/template`: в pipeline `{{x | f y}}` piped-значение идёт ПОСЛЕДНИМ аргументом (`f(y, x)`) — для функций с порядком (tz, time) вызывать напрямую `{{f a b}}`, иначе 500 (ловили на /reminders).
- Ответы бота — `ParseModeHTML`: пользовательский текст и вывод LLM вставлять как есть нельзя
  без экранирования там, где он может содержать `<>&` (превью и разборы сейчас вставляются
  сырыми — известное упрощение MVP, правится при жалобах на битый HTML). Ревью недели —
  всегда plain text именно поэтому.
- Сырьё (`entries`) пишется один раз; правится только явным исправлением пользователя
  (`UpdateTranscript` + `ClearDerived` + переразбор), удаляется только явным удалением
  (`DeleteEntry`/`DeleteDay` с каскадом: блоки/сущности/открытые задачи/несработавшие
  напоминания/proposed-делегации; выполненные задачи — история, не трогать).
- Делегирование: только карточка `proposed` + stub-ответ плагина. Никаких реальных
  внешних вызовов из ядра. Подтверждение «да, поручи» — отдельная задача (Фаза 4).
- Текст бота не должен обещать несделанного (ловили на «подтверди — и агент заберёт»):
  сверять формулировки с фактом кода.

## 5. Таксономия аспектов (фиксирована)

`fact thought plan task idea emotion health work people money gratitude decision other`
— менять список только вместе с `CONCEPT.md` §3 и промптом в `analyze.go`.

## 6. Доки

- Поведение меняешь → обнови `CONCEPT.md` (что) + `ROADMAP.md` (статус фазы) + `README.md` (если видно пользователю).
- `archive/python-bot/` — история, туда ничего не писать.

## 7. Стиль работы

- Отвечать пользователю по-русски, коротко, по делу.
- Проверять сборку `go vet + go build` после любых правок `.go`-файлов и писать итог по факту прогона.
- Без опроса: вопросы задавать только если без ответа нельзя двигаться; иначе — решение + обоснование.
