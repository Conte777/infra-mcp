# Автоодобрение MCP-инструментов в Claude Code

Исследование по issue [Conte777/infra-mcp#3](https://github.com/Conte777/infra-mcp/issues/3).

**Вопрос**: как задать автоодобрение так, чтобы read-инструменты вызывались без подтверждения, а всё остальное спрашивало.

**Дата проверки**: 2026-08-20. Проверено на Claude Code CLI `2.1.237` (`~/.local/share/claude/versions/2.1.237`).
Основной первоисточник — <https://code.claude.com/docs/en/permissions.md> (страница «Configure permissions»).

**Короткий ответ**: wildcard по префиксу имени инструмента поддерживается и документирован. Одна строка `mcp__<server>__pg_read_*` в `permissions.allow` решает задачу.

---

## 1. Синтаксис правил для MCP-инструментов

### Базовые формы

Раздел «MCP» в permissions.md, дословно
(<https://code.claude.com/docs/en/permissions.md>):

> MCP rules use the server name as configured in Claude Code, optionally followed by the name of a tool from that server.
>
> * `mcp__puppeteer` matches any tool provided by the `puppeteer` server
> * `mcp__puppeteer__*` uses wildcard syntax and also matches all tools from the `puppeteer` server
> * `mcp__puppeteer__puppeteer_navigate` matches the `puppeteer_navigate` tool provided by the `puppeteer` server

### Wildcard по префиксу имени инструмента — ПОДТВЕРЖДЁН

Раздел «Tool name wildcards», дословно
(<https://code.claude.com/docs/en/permissions.md>):

> Allow rules accept tool-name globs only after a literal `mcp__<server>__` prefix. The server segment must be glob-free so the rule names a specific server you configured. `mcp__puppeteer__*` matches every tool from the `puppeteer` server, and **`mcp__github__get_*` matches its `get_` tools**. An unanchored allow glob such as `"*"`, `"B*"`, or `"mcp__*"` is skipped with a warning and doesn't auto-approve anything.

То есть в `allow`:

| Форма | Работает | Комментарий |
|---|---|---|
| `mcp__pg__pg_read_query` | да | точное имя |
| `mcp__pg` | да | весь сервер |
| `mcp__pg__*` | да | весь сервер, через glob |
| **`mcp__pg__pg_read_*`** | **да** | **префиксный glob по имени инструмента — то, что нужно** |
| `mcp__*` | нет | «unanchored» — игнорируется с предупреждением |
| `mcp__pg*__read_*` | нет | сегмент сервера обязан быть без glob |

Подтверждение из второго независимого источника: строки валидатора внутри самого бинаря CLI
(`strings ~/.local/share/claude/versions/2.1.237`) содержат в блоке `suggestion`/`examples`
ровно эти два примера — `mcp__puppeteer__*` и `mcp__github__get_*` — рядом с сообщениями
`Wildcard tool name "…" is not supported in allow rules` и
`MCP rules do not support patterns in parentheses` /
`Either specify a pattern or use just "…" without parentheses, or use "mcp__…__*" for all tools`.

### Чего в MCP-правилах нельзя

Из тех же строк бинаря (сообщения валидации, точный текст):
`MCP rules do not support patterns in parentheses`. То есть форма `mcp__pg(select *)` невалидна —
скобочный синтаксис (как у `Bash(git *)`) к MCP не применяется. Отбирать по аргументам вызова
(например, «только SELECT») правилами permissions нельзя — это должно быть внутри самого сервера.

### `deny` и `ask` — правила шире

Дословно (<https://code.claude.com/docs/en/permissions.md>, «Tool name wildcards»):

> Deny and ask rules also accept glob patterns in the tool-name position. The pattern must match the full tool name: `"*"` matches every tool, and `"mcp__*"` matches every MCP tool across all servers.

Важное следствие для `deny` (там же):

> A tool matched by a bare-name glob deny rule is removed from Claude's context, the same as a bare tool name

То есть `deny` не просто блокирует вызов, а **убирает инструмент из контекста** — модель его не видит.

### Живой пример синтаксиса из локального конфига

`~/.claude/settings.json` (существующий рабочий конфиг пользователя) уже использует ровно эту форму:

```json
{
  "permissions": {
    "allow": ["mcp__context7__*", "mcp__db-mcp-dev__query", "mcp__gitlab__*"],
    "ask": ["mcp__gitlab__create_*", "mcp__grafana-dev__alerting_manage_*"]
  }
}
```

Замечание: в `/Users/conte/Projects/mcp-atlassian/.claude/settings.json` ключи `allow`/`defaultMode`
лежат на верхнем уровне, **вне** объекта `permissions`. По документированной схеме это неверно —
правила должны быть внутри `permissions`. Этот файл как образец синтаксиса брать нельзя.

### Отдельно: имена инструментов у серверов, поставляемых плагином

Критично для этого проекта, так как серверы ставятся плагином. Дословно
(<https://code.claude.com/docs/en/mcp.md>, «Plugin MCP tool names»):

> Tools from a plugin-bundled MCP server include both the plugin name and the server key in their callable name. The full form is `mcp__plugin_<plugin-name>_<server-name>__<tool-name>`, where any character outside `A-Z`, `a-z`, `0-9`, `_`, and `-` is replaced with `_`.
>
> ```
> mcp__plugin_my-plugin_database-tools__query
> ```
>
> Use this full name when referencing the tool in permission rules, a skill's `allowed-tools` list, a subagent's `tools` field, or a hook matcher. A hook matcher written against the bare server key, such as `mcp__database-tools__.*`, never fires for a plugin-bundled server.

Сервер при этом регистрируется под именем `plugin:<plugin-name>:<server-name>`.

---

## 2. Уровни настроек и приоритет

Из <https://code.claude.com/docs/en/settings.md>, дословно:

> * **User settings** are defined in `~/.claude/settings.json` and apply to all projects.
> * **Project settings** are saved in your project directory:
>   * `.claude/settings.json` for settings that are checked into source control and shared with your team
>   * `.claude/settings.local.json` for settings that are not checked in
> * **Managed settings**: For organizations that need centralized control … All use the same JSON format and cannot be overridden by user or project settings

Порядок приоритета, дословно (там же, «Settings precedence»):

> When the same setting appears in multiple scopes, Claude Code applies them in priority order:
>
> 1. **Managed** (highest): can't be overridden by any other scope, apart from the exceptions to managed settings precedence
> 2. **Command line arguments**: temporary session overrides
> 3. **Local**: overrides project and user settings
> 4. **Project**: overrides user settings
> 5. **User** (lowest): applies when nothing else specifies the setting

### Permissions складываются, а не перекрывают

Ключевое отличие permissions от остальных настроек, дословно (<https://code.claude.com/docs/en/settings.md>):

> For example, if your user settings set `spinnerTipsEnabled` to `true` and project settings set it to `false`, the project value applies. **Permission rules merge across scopes instead**, and a few security-sensitive keys are exceptions.

И из <https://code.claude.com/docs/en/permissions.md> («Settings precedence»):

> Permission rules follow the same settings precedence as all other Claude Code settings, with managed settings highest: no other level, including command line arguments, can override a managed permission rule.
>
> If a tool is denied at any level, no other level can allow it. For example, a managed settings deny can't be overridden by `--allowedTools`, and `--disallowedTools` can add restrictions beyond what managed settings define.
>
> The same holds across settings scopes: if user settings allow a permission and project settings deny it, the deny rule blocks it. The reverse is also true: a user-level deny blocks a project-level allow, because deny rules from any scope are evaluated before allow rules.

### Порядок вычисления правил внутри слитого набора

Дословно (<https://code.claude.com/docs/en/permissions.md>, «Manage permissions»):

> Rules are evaluated in order: deny, then ask, then allow. The first match in that order determines the outcome, and **rule specificity doesn't change the order**.
>
> A broad deny rule like `Bash(aws *)` blocks every matching call, including calls that also match a narrower allow rule like `Bash(aws s3 ls)`, so a deny rule can't carry allowlist exceptions. The same precedence applies between ask and allow: a matching ask rule prompts even when a more specific allow rule also matches the same call.

Практически: попытка написать «`ask` на весь сервер + `allow` на read-инструменты» **не сработает** —
`ask` выиграет у более узкого `allow`. Нужен только `allow` на read, остальное спросит по умолчанию.

### Workspace trust для проектных allow-правил

Дословно (<https://code.claude.com/docs/en/permissions.md>, «Project allow rules and workspace trust»):

> `permissions.allow` rules and `permissions.additionalDirectories` entries in a project's `.claude/settings.json` grant capability, so Claude Code applies them only after you accept the workspace trust dialog for that folder. … `deny` and `ask` rules aren't affected, since they only restrict.

---

## 3. Поведение по умолчанию для инструмента без правила

В режиме `default` (Manual) без совпавшего allow-правила инструмент **спрашивает подтверждение**.
Дословно (<https://code.claude.com/docs/en/permission-modes.md>, таблица режимов):

> | `default` | Reads only | Reviewing every action yourself, sensitive work |

Строка «Reads only» = «что выполняется без запроса». MCP-инструменты в число встроенных read-only
не входят (в таблице permissions.md read-only — это «File reads, Grep»), поэтому по умолчанию они спрашивают.

Описание режима из permissions.md, дословно:

> `default` — Prompts for permission on first use of each tool.

**Вывод: явный `ask` или `deny` для остальных инструментов сервера не нужен.** Достаточно перечислить
read-инструменты в `allow`; всё, что не совпало, попадёт под запрос по умолчанию.

Оговорка: это верно для режима `default`/`plan`. В `auto`, `acceptEdits`, `bypassPermissions` картина
другая — см. раздел 5.

---

## 4. Может ли плагин поставить готовый allow-список

**Нет.** Дословно (<https://code.claude.com/docs/en/plugins.md>, «Ship default settings with your plugin»):

> Plugins can include a `settings.json` file at the plugin root to apply default configuration when the plugin is enabled. **Currently, only the `agent` and `subagentStatusLine` keys are supported.**
> …
> Settings from `settings.json` take priority over `settings` declared in `plugin.json`. **Unknown keys are silently ignored.**

То есть положить `permissions.allow` в `settings.json` плагина можно физически, но ключ будет
молча проигнорирован. Никакого документированного механизма поставки permission-правил плагином нет.

Таблица директорий плагина (там же) перечисляет, что плагин может нести:
`.claude-plugin/plugin.json`, `skills/`, `commands/`, `agents/`, `hooks/` (`hooks.json`),
`.mcp.json`, `.lsp.json`, `monitors/`, `bin/`, `settings.json`. Permissions в списке нет.

### Что плагин может вместо этого

**(а) Собственный сервер помечает write-инструменты как всегда требующие подтверждения.**
Это работает без участия пользователя и не отменяется никакими allow-правилами.
Дословно (<https://code.claude.com/docs/en/mcp.md>, «Require approval for a specific tool»):

> If you're building an MCP server, you can mark a tool as requiring explicit approval on every call by setting `_meta["anthropic/requiresUserInteraction"]` to `true` in the tool's `tools/list` response entry. The value must be the JSON boolean `true`; any other value is ignored.
>
> Claude Code shows that tool's permission prompt on every call, even in `acceptEdits`, `auto`, and `bypassPermissions` permission modes, and doesn't offer a "don't ask again" option for it. **Allow rules that match the tool don't skip the prompt either.** In `dontAsk` mode, which never prompts, Claude Code denies the call instead.
> …
> **Other tools from the same server keep their normal permission behavior.**

```json
{
  "_meta": {
    "anthropic/requiresUserInteraction": true
  }
}
```

Требует Claude Code v2.1.199+; более ранние версии аннотацию игнорируют (там же).

Ограничение (там же): в неинтерактивном режиме с `--permission-prompt-tool` результат `allow`
для такого инструмента конвертируется в deny с сообщением
`MCP tool requires user interaction; not supported via --permission-prompt-tool`.

**(б) Плагин может нести `hooks/hooks.json` с `PreToolUse`-хуком**, который возвращает
`permissionDecision: "allow"` для read-инструментов. Это документированный путь автоодобрения
из плагина. Матчеры хуков — regex, и для плагинных серверов нужно полное имя
(<https://code.claude.com/docs/en/hooks.md>):

> To match every tool from a server, append `.*` to the server prefix. The `.*` is required: a matcher like `mcp__memory` … is compared as an exact string and matches no tool.
>
> * `mcp__memory__.*` matches all tools from the `memory` server
> * `mcp__.*__write.*` matches any tool whose name starts with `write` from any server

Но `allow` от хука не обходит deny/ask-правила, дословно (<https://code.claude.com/docs/en/permissions.md>):

> Hook decisions don't bypass permission rules. Claude Code evaluates deny and ask rules regardless of what a PreToolUse hook returns: a matching deny rule blocks the call, and a matching ask rule still prompts even when the hook returned `"allow"` or `"ask"`.

**(в) Просто задокументировать одну строку в README плагина**, чтобы пользователь вставил её сам.

---

## 5. Взаимодействие с режимами разрешений

Таблица режимов, дословно (<https://code.claude.com/docs/en/permission-modes.md>):

> | Mode | What runs without asking |
> | `default` | Reads only |
> | `acceptEdits` | Reads, file edits, and common filesystem commands (`mkdir`, `touch`, `mv`, `cp`, etc.) |
> | `plan` | Reads, plus classifier-approved commands when auto mode is available |
> | `auto` | Everything, with background safety checks |
> | `dontAsk` | Only pre-approved tools |
> | `bypassPermissions` | Everything |

Как режимы соотносятся с правилами, дословно (там же):

> Modes set the baseline. Layer permission rules on top to pre-approve or block specific tools. **Deny rules block in every mode, including `bypassPermissions`.** … **Allow rules have no effect in `bypassPermissions`.**

Разбор по режимам:

- **`acceptEdits`** — про MCP ничего не сказано. Описание режима перечисляет ровно файловые правки и
  фиксированный набор filesystem-команд Bash; MCP-инструменты не упомянуты. Логично заключить, что они
  ведут себя как в `default`, **но явного утверждения в документации нет — это вывод, а не цитата**.

- **`plan`** — дословно: «Plan mode tells Claude to research and propose changes without making them.
  Claude reads files, runs shell commands to explore, and writes a plan, but does not edit your source.»
  Речь только про правки исходников и shell. **Блокирует ли plan-режим MCP write-инструменты — в
  документации явно не сказано. Не подтверждено.**

- **`bypassPermissions`** — allow-правила не действуют (не нужны), deny-правила действуют.
  MCP-инструменты с `requiresUserInteraction` спрашивают даже здесь.

- **`dontAsk`** — дословно: «Auto-denies tools unless pre-approved via `/permissions` or
  `permissions.allow` rules. `AskUserQuestion`, connector tools your organization set to `ask`, and
  MCP tools marked `requiresUserInteraction` are denied even if you've allowed them». Это ровно тот
  режим, где схема «allow на read, deny по умолчанию на всё прочее» даёт нужное поведение в CI.

- **`auto`** — классификатор проверяет всё, что не разрешено правилами. Дословно:
  «Actions matching your allow, ask, or deny rules resolve immediately. … Connector tools your
  organization set to `ask` and MCP tools marked `requiresUserInteraction` prompt you directly even
  when an allow rule matches. … Everything else goes to the classifier».

Список действий, которые не автоодобряются ни в одном режиме, дословно
(<https://code.claude.com/docs/en/permission-modes.md>, «Actions no mode auto-approves»):

> * Tools matched by an explicit ask rule
> * Connector tools your organization set to `ask`
> * Tools that require user interaction: the built-in `AskUserQuestion` tool and MCP tools marked `requiresUserInteraction`
> * `rm` and `rmdir` removals targeting a critical path
> * The cross-session messaging safeguards

---

## Что это значит для проекта

### Одна строка с wildcard — достаточно

Генерируемый поимённый список read-инструментов **не нужен**. Форма
`mcp__<server>__<prefix>_*` в `permissions.allow` документирована и поддерживается.

Для одного сервера:

```json
{
  "permissions": {
    "allow": ["mcp__postgres__pg_read_*"]
  }
}
```

Для всех шести источников — шесть строк, по одной на сервер, а не по одной на инструмент.

**Но**: серверы поставляются плагином, а у плагинных серверов имя инструмента — полное
`mcp__plugin_<plugin-name>_<server-name>__<tool-name>`. Значит реальные правила выглядят так
(имена плагина и сервера подставить фактические):

```json
{
  "permissions": {
    "allow": [
      "mcp__plugin_infra-mcp_postgres__pg_read_*",
      "mcp__plugin_infra-mcp_k8s__k8s_read_*"
    ]
  }
}
```

Это стоит проверить эмпирически после первой сборки плагина: посмотреть фактические имена в `/mcp`
или в списке инструментов и сверить с шаблоном.

### Влияние на схему именования `pg_read_*` / `pg_write_*`

Схема из `CONTEXT.md` **работает как задумано и является правильным выбором** — именно потому,
что allow-glob умеет только префикс имени инструмента. Уточнения:

1. **Префикс `read` должен быть в начале имени, сразу после префикса источника, и быть буквальным.**
   `pg_read_query` → `mcp__…__pg_read_*` матчится. Если бы схема была `pg_query_read`, одной строки
   бы не вышло. Текущая схема этому требованию удовлетворяет.

2. **Никаких read-инструментов вне префикса.** Любой диагностический инструмент
   (`pg_ping`, `pg_diagnose` для деградированного старта), который хочется автоодобрять,
   придётся либо переименовать в `pg_read_*`, либо добавить отдельной строкой в allow.
   Дешевле — назвать `pg_read_diagnose`.

3. **Префикс источника внутри имени инструмента (`pg_`) для permissions избыточен** — сервер уже
   назван в правиле. Но он не мешает и полезен для читаемости в диалоге подтверждения.
   Менять смысла нет.

4. **`pg_write_*` не требует никаких правил.** Отсутствие правила = запрос подтверждения
   в `default`. Писать `"ask": ["mcp__…__pg_write_*"]` не только не нужно, но и опасно:
   `ask` побеждает `allow` вне зависимости от специфичности, так что случайное пересечение
   шаблонов сломает автоодобрение read.

5. **Сильная гарантия для write — на стороне сервера, а не пользователя.** Пометить write-инструменты
   `_meta["anthropic/requiresUserInteraction"]: true` в ответе `tools/list`. Тогда подтверждение
   спросят даже в `auto` и `bypassPermissions`, и даже если пользователь по ошибке напишет
   `mcp__…__*` в allow. Это единственный способ выполнить требование CONTEXT.md
   «Write-инструмент … всегда требует подтверждения пользователя» без опоры на дисциплину пользователя.
   Требует Claude Code v2.1.199+.

6. **Плагин не может поставить allow-список.** Варианты: (а) `requiresUserInteraction` на write +
   строка в README для read; (б) `hooks/hooks.json` с `PreToolUse`, автоодобряющим
   `mcp__plugin_infra-mcp_.*__.*_read_.*`. Вариант (а) проще и честнее — пользователь видит,
   что именно он разрешил, в `/permissions`.

---

## Не подтверждено

- Влияет ли `acceptEdits` на MCP-инструменты. В документации режима MCP не упомянут; предположение,
  что они ведут себя как в `default`, — вывод по умолчанию, а не цитата.
- Блокирует ли `plan`-режим MCP write-инструменты. Документация говорит только про правки исходников
  и shell-команды; про MCP молчит.
- Фактическое имя инструмента у сервера, поставляемого этим конкретным плагином
  (`mcp__plugin_<plugin>_<server>__<tool>`) — формула документирована, но подставленные значения
  надо сверить эмпирически после сборки плагина.
- Порядок между двумя allow-правилами разной специфичности внутри одного скоупа — вопрос не возникает,
  так как результат один и тот же (allow).
