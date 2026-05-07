# Resuming work — what to type into a fresh Claude session

This file is for Pavel: copy one of the prompts below into a new
Claude conversation to pick up where we left off without re-orienting
from scratch.

## Default — start the next big chunk (N8 server-side re-implementation)

```
Продолжаю работу с wgturn-core. Прочитай /home/user/VPN/CLAUDE.md
по порядку (он указан в файле), особенно docs/N8-SERVER-PLAN.md —
там детальный план следующего шага.

Сегодня делаем N8: re-implement server-side как pkg/wgturnsrv под
Apache-2.0, clean-room (НЕ копировать GPL код из slovn/wgturn-server).
Бюджет ~6 часов кода, без deploy на is-01 — только до зелёного pair
test'а. is-01 переключаем отдельной сессией с 24-часовым soak'ом по
плану S9.

Старт: S1 (factor-out internal/framing). Затем S2-S5 по плану.
Останавливайся после каждого подшага: make test && make lint
зелёные, коммит, продолжаем дальше.
```

## Short version (если хочется минимума слов)

```
Делаем N8 по docs/N8-SERVER-PLAN.md. Начни с S1, дальше по порядку.
```

## Status check (просто посмотреть состояние, без работы)

```
Прочитай /home/user/VPN/CLAUDE.md и docs/ROADMAP.md, скажи что
сейчас в работе, что в планах, что можно делать дальше. Без
изменения кода.
```

## Operational — деплой N8 на is-01 (только когда S1-S8 закрыты)

```
N8 готова и зелёная локально (pair test green, CI green). Сегодня
делаем S9 — параллельный деплой wgturn-cli serve на is-01:56001 и
24-часовой soak. Подробности: docs/N8-SERVER-PLAN.md S9.

Не трогай :56000 (там продакшен `slovn/wgturn-server`). Только :56001
параллельно. После soak'а Pavel даст отдельную команду на switch.
```

## Quick fixes / hotfixes

```
Хотфикс по wgturn-core: <опиши проблему>. CLAUDE.md уже прочитан в
прошлой сессии — освежи статус из docs/ROADMAP.md и docs/HANDBOOK.md
если нужно.
```

## Бэклог приоритетов (после N8)

В порядке предпочтения, см. `docs/ROADMAP.md`:

1. **N8** — re-impl сервера (детальный план в `docs/N8-SERVER-PLAN.md`).
2. **N1.5** — macOS / Windows host-side network setup. Аналогично
   текущему `cmd/wgturn-cli/hostsetup.go` Linux-пути, только
   `ifconfig` + `route` для macOS, `netsh interface` для Windows.
3. **N6** — gomobile bindings (`pkg/wgturn/mobile/`) для Android
   `.aar` / iOS `.xcframework`. Открывает мобильные приложения.
4. **N3** — pure-Go slider captcha solver (убирает Chromium как
   зависимость; альтернатива embedded Chromium на ~80 MB).
5. **N5** — 2captcha API solver (1 час работы, fallback для users
   без Chrome).

## What NOT to do without explicit Pavel approval

- Деплой чего-либо на is-01 (`93.95.226.167`) — там единственный
  emergency tunnel, поломка стоит реального доступа к сети.
- Force push в main любого репо.
- Удаление веток / тегов в Forgejo или GitHub.
- Любые изменения в `slovn/wgturn-server` (legacy GPL fork) — ждём
  пока N8 закроется и тогда репо архивируется целиком.
- Запуск `wgturn-cli connect` на .142 без явной просьбы — каждый
  запуск тратит VK captcha quota из лимита ~5 за 10 мин.

## Хорошо знать про инфраструктуру

- `claude-chat` контейнер на `192.168.0.200` — тут живёт Claude.
  SSH-ключ `~/.ssh/id_ed25519` авторизован на .207 / .236 / .142 /
  Forgejo / GitHub PavelLizunov.
- `192.168.0.207` — Forgejo + Go 1.25 toolchain (`~/go125/go/bin/`)
  + handoff bundle (`~/wgturn-handoff/`).
- `192.168.0.142` — Chrome на `:9222`, эталонный wgturn-cli клиент
  для smoke-тестов против реального VK.
- `93.95.226.167` (is-01) — wgturn-server (Iceland VPS). Доступ
  через `ssh user@.207 'ssh root@93.95.226.167 ...'` (proxy hop).

Все детали в `docs/HANDBOOK.md` "Infrastructure map".
