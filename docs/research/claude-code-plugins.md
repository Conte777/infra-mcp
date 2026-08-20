# Плагины Claude Code и marketplace: что доступно проекту, поставляющему несколько MCP-серверов

Исследование по issue [Conte777/infra-mcp#2](https://github.com/Conte777/infra-mcp/issues/2).

**Дата проверки:** 2026-08-20.

**Первоисточники:**

- Официальная документация Claude Code (`docs.claude.com/en/docs/claude-code/*` редиректит 301 на `code.claude.com/docs/en/*`; markdown-исходник страницы доступен по `<url>.md`):
  - [Plugins reference](https://code.claude.com/docs/en/plugins-reference)
  - [Create plugins](https://code.claude.com/docs/en/plugins)
  - [Create and distribute a plugin marketplace](https://code.claude.com/docs/en/plugin-marketplaces)
  - [Discover and install prebuilt plugins](https://code.claude.com/docs/en/discover-plugins)
  - [Connect Claude Code to tools via MCP](https://code.claude.com/docs/en/mcp)
  - [Hooks](https://code.claude.com/docs/en/hooks)
- JSON-схемы: [claude-code-plugin-manifest.json](https://www.schemastore.org/claude-code-plugin-manifest.json), [claude-code-marketplace.json](https://www.schemastore.org/claude-code-marketplace.json) (обе перечислены в [каталоге SchemaStore](https://www.schemastore.org/api/json/catalog.json)).
- Живые примеры на машине: `/Users/conte/.claude/plugins/` — клон официального маркетплейса `anthropics/claude-plugins-official` в `marketplaces/claude-plugins-official/`, установленные плагины в `cache/`, реестры `known_marketplaces.json` и `installed_plugins.json`.

> **Замечание об одном мёртвом источнике.** Официальный `marketplace.json` ссылается на `"$schema": "https://anthropic.com/claude-code/marketplace.schema.json"`, но этот URL отдаёт 404 (проверено curl'ом). Рабочая схема — на SchemaStore.

---

## 1. Структура `marketplace.json` и `plugin.json`

### 1.1 Где лежат файлы

- Манифест плагина — `.claude-plugin/plugin.json` **в корне плагина**. Все остальные каталоги (`skills/`, `agents/`, `hooks/`, `commands/`, `bin/`, `.mcp.json`, `settings.json`) лежат в корне плагина, **не** внутри `.claude-plugin/`. Источник: [Plugins reference → Plugin directory structure](https://code.claude.com/docs/en/plugins-reference#plugin-directory-structure) (блок Warning), дублируется в [Create plugins → Plugin structure overview](https://code.claude.com/docs/en/plugins#plugin-structure-overview).
- Манифест маркетплейса — `.claude-plugin/marketplace.json` в корне репозитория маркетплейса. Источник: [Plugin marketplaces → Create the marketplace file](https://code.claude.com/docs/en/plugin-marketplaces#create-the-marketplace-file).
- Относительные `source` в маркетплейсе резолвятся от **корня маркетплейса**, а не от `.claude-plugin/`; `../` использовать нельзя. Источник: [Plugin marketplaces → Relative paths](https://code.claude.com/docs/en/plugin-marketplaces#relative-paths).

### 1.2 `plugin.json`: обязательные и опциональные поля

Манифест **опционален целиком**. Если его нет, Claude Code сам находит компоненты в дефолтных местах и берёт имя плагина из имени каталога. Если манифест есть, **единственное обязательное поле — `name`** (kebab-case, без пробелов). Источник: [Plugins reference → Plugin manifest schema / Required fields](https://code.claude.com/docs/en/plugins-reference#plugin-manifest-schema); схема SchemaStore подтверждает `"required": ["name"]`.

Полная схема из документации (цитата):

```json
{
  "name": "plugin-name",
  "displayName": "Plugin Name",
  "version": "1.2.0",
  "description": "Brief plugin description",
  "author": { "name": "Author Name", "email": "author@example.com", "url": "https://github.com/author" },
  "homepage": "https://docs.example.com/plugin",
  "repository": "https://github.com/author/plugin",
  "license": "MIT",
  "keywords": ["keyword1", "keyword2"],
  "metadata": { "catalogId": "cat-123", "tier": "pro" },
  "skills": "./custom/skills/",
  "commands": ["./custom/commands/special.md"],
  "agents": ["./custom/agents/reviewer.md"],
  "hooks": "./config/hooks.json",
  "mcpServers": "./mcp-config.json",
  "outputStyles": "./styles/",
  "lspServers": "./.lsp.json",
  "experimental": { "themes": "./themes/", "monitors": "./monitors.json" },
  "dependencies": ["helper-lib", { "name": "secrets-vault", "version": "~2.1.0" }]
}
```

Дополнительно к этому списку документированы: `$schema`, `defaultEnabled` (bool, по умолчанию `true`; `false` — плагин ставится выключённым, требует Claude Code ≥ v2.1.154), `userConfig` (объект), `channels` (массив), `workflows` (пути). Источник: те же таблицы [Metadata fields](https://code.claude.com/docs/en/plugins-reference#metadata-fields) и [Component path fields](https://code.claude.com/docs/en/plugins-reference#component-path-fields).

Прочие правила:

- Нераспознанные top-level поля **игнорируются**, плагин грузится; `claude plugin validate` показывает их как warning, `--strict` превращает warning в error. Источник: [Unrecognized fields](https://code.claude.com/docs/en/plugins-reference#unrecognized-fields).
- Все пути в манифесте — относительные, начинаются с `./` (поле `skills` дополнительно принимает `"."`). Источник: [Path behavior rules](https://code.claude.com/docs/en/plugins-reference#path-behavior-rules).
- `commands`, `agents`, `workflows`, `outputStyles`, `experimental.themes`, `experimental.monitors` **заменяют** дефолтный каталог. `skills` **добавляется** к дефолтному `skills/`. У `hooks`, `mcpServers`, `lspServers` — собственные правила слияния. Там же.

Живой пример многоскилового плагина без MCP: `/Users/conte/.claude/plugins/marketplaces/mattpocock/.claude-plugin/plugin.json` — `name`, `version`, `author`, `homepage`, `repository`, `license`, `keywords` и явный массив `skills` из 25 путей.

### 1.3 `marketplace.json`

Обязательные поля: `name`, `owner` (объект, внутри обязателен `owner.name`), `plugins` (массив). Источник: [Marketplace schema → Required fields](https://code.claude.com/docs/en/plugin-marketplaces#required-fields) и `"required": ["name","owner","plugins"]` в схеме SchemaStore.

Опциональные поля маркетплейса: `$schema`, `description`, `version`, `metadata.pluginRoot`, `allowCrossMarketplaceDependenciesOn`, `renames` (карта «старое имя → новое имя или `null`», требует ≥ v2.1.193). Источник: [Optional fields](https://code.claude.com/docs/en/plugin-marketplaces#optional-fields). Схема SchemaStore добавляет ещё `forceRemoveDeletedPlugins` — **этого поля нет в документации**, поведение не описано, считать неподтверждённым.

Запись плагина (`plugins[]`): обязательны `name` и `source`. Опционально доступны любые поля из манифеста плагина плюс маркетплейс-специфичные `source`, `category`, `tags`, `strict`, `relevance`, `defaultEnabled`. Источник: [Plugin entries](https://code.claude.com/docs/en/plugin-marketplaces#plugin-entries).

Имена маркетплейсов зарезервированы под Anthropic: `claude-plugins-official`, `anthropic-plugins`, `agent-skills` и др.; имена, мимикрирующие под официальные, тоже блокируются. Источник: Note в [Required fields](https://code.claude.com/docs/en/plugin-marketplaces#required-fields).

`strict` (по умолчанию `true`): при `true` авторитет по компонентам — `plugin.json`, запись маркетплейса лишь дополняет его. При `false` запись маркетплейса — полное определение, и если у плагина есть свой `plugin.json` с компонентами, это конфликт и плагин не грузится. Источник: [Strict mode](https://code.claude.com/docs/en/plugin-marketplaces#strict-mode).

### 1.4 Источники плагина (`source`)

| Тип | Поля | Примечание |
| --- | --- | --- |
| относительный путь (строка `"./my-plugin"`) | — | каталог внутри репозитория маркетплейса |
| `github` | `repo`, `ref?`, `sha?` | |
| `url` | `url`, `ref?`, `sha?` | git-URL |
| `git-subdir` | `url`, `path`, `ref?`, `sha?` | sparse-клон подкаталога монорепо |
| `npm` | `package`, `version?`, `registry?` | |
| `archive` | `url`, `sha256?` | zip по HTTPS, ≥ v2.1.224 |
| `command` | `command`, `timeout?`, `mode?` | ≥ v2.1.229 |

Источник: [Plugin sources](https://code.claude.com/docs/en/plugin-marketplaces#plugin-sources). Если заданы и `ref`, и `sha`, действует `sha`.

Важное разграничение оттуда же: **источник маркетплейса** (откуда тянется сам `marketplace.json`) и **источник плагина** (откуда тянется плагин) — разные вещи и пиннятся независимо; git-источник маркетплейса поддерживает `ref`, но не `sha`.

### 1.5 Версионирование

Версия — это ключ кэша, по которому Claude Code решает, есть ли обновление. Для всех типов источников, кроме `command`, версия берётся из первого сработавшего:

1. `version` в `plugin.json`;
2. `version` в записи маркетплейса;
3. git-SHA коммита источника (для `github`, `url`, `git-subdir` и относительных путей в git-hosted маркетплейсе);
4. SHA-256 дайджест — для `archive` (первые 12 символов);
5. `unknown` — для `npm` и локальных каталогов вне git.

Если `version` задан в обоих местах, выигрывает `plugin.json`. Источник: [Version management](https://code.claude.com/docs/en/plugins-reference#version-management).

Следствие, названное в документации явно: с явным `version` пользователи получат обновление **только** после бампа поля — новые коммиты без бампа ничего не меняют, а `/plugin update` скажет «already at the latest version». Без `version` в обоих местах обновление приезжает на каждый новый коммит. Там же.

### 1.6 Как пользователь ставит плагин из GitHub-репозитория

```shell
# 1. Добавить маркетплейс (owner/repo — GitHub shorthand)
/plugin marketplace add Conte777/infra-mcp
# или из шелла:
claude plugin marketplace add Conte777/infra-mcp

# 2. Поставить плагин
/plugin install infra-mcp@infra-mcp
claude plugin install infra-mcp@infra-mcp            # user scope по умолчанию
claude plugin install infra-mcp@infra-mcp --scope project
```

Источники: [Add from GitHub](https://code.claude.com/docs/en/discover-plugins#add-from-github), [Install plugins](https://code.claude.com/docs/en/discover-plugins#install-plugins), [plugin install](https://code.claude.com/docs/en/plugins-reference#plugin-install), [Plugin marketplace add](https://code.claude.com/docs/en/plugin-marketplaces#plugin-marketplace-add).

Детали:

- Пин на ветку/тег: `owner/repo@ref` для GitHub-shorthand, `#ref` для git-URL. Источник: [Plugin marketplace add](https://code.claude.com/docs/en/plugin-marketplaces#plugin-marketplace-add).
- `claude plugin install` устанавливает в scope `user` по умолчанию, флаг `-s/--scope` принимает `user | project | local`. Scope определяет, в какой settings-файл пишется `enabledPlugins`. Источник: [plugin install](https://code.claude.com/docs/en/plugins-reference#plugin-install), [Plugin installation scopes](https://code.claude.com/docs/en/plugins-reference#plugin-installation-scopes).
- Установка из `/plugin` внутри сессии с v2.1.221 может активировать плагин сразу («Plugin is now active.»); иначе просит `/reload-plugins`. `claude plugin install` из шелла в открытую сессию не попадает. Источник: [Install plugins](https://code.claude.com/docs/en/discover-plugins#install-plugins).
- Команда для команды разработки: `extraKnownMarketplaces` в `.claude/settings.json` проекта добавляет маркетплейс автоматически после того, как участник доверил папку; но с v2.1.195 это **не** ставит плагины из внешних источников — Claude Code покажет команду `claude plugin install`, которую надо выполнить. Источник: [Configure team marketplaces](https://code.claude.com/docs/en/discover-plugins#configure-team-marketplaces).

---

## 2. `.mcp.json` внутри плагина

### 2.1 Где лежит и как объявляются серверы

`.mcp.json` — **в корне плагина** (не в `.claude-plugin/`), либо серверы объявляются инлайном в `plugin.json` под ключом `mcpServers`. Источник: [Plugins reference → MCP servers](https://code.claude.com/docs/en/plugins-reference#mcp-servers), [MCP → Plugin-provided MCP servers](https://code.claude.com/docs/en/mcp#plugin-provided-mcp-servers).

Документированный формат (цитата из [MCP → Plugin-provided MCP servers](https://code.claude.com/docs/en/mcp#plugin-provided-mcp-servers)):

```json
{
  "mcpServers": {
    "database-tools": {
      "command": "${CLAUDE_PLUGIN_ROOT}/servers/db-server",
      "args": ["--config", "${CLAUDE_PLUGIN_ROOT}/config.json"],
      "env": { "DB_URL": "${DB_URL}" }
    }
  }
}
```

> **Наблюдение, не подтверждённое документацией.** В официальном маркетплейсе `anthropics/claude-plugins-official` часть плагинов пишет `.mcp.json` **без обёртки `mcpServers`** — прямо картой «имя сервера → конфиг». Примеры на диске: `/Users/conte/.claude/plugins/marketplaces/claude-plugins-official/external_plugins/gitlab/.mcp.json`, `.../serena/.mcp.json`, `.../github/.mcp.json`, `.../firebase/.mcp.json`, `.../linear/.mcp.json`, `.../playwright/.mcp.json`, `.../laravel-boost/.mcp.json`, `.../greptile/.mcp.json`, а также `plugins/example-plugin/.mcp.json` авторства Anthropic. Другие плагины того же маркетплейса (`context7`, `telegram`, `discord`, `imessage`, `fakechat`) используют документированную обёртку. Вывод: обе формы, судя по всему, принимаются, но **документация описывает только форму с `mcpServers`** — на неё и следует опираться.

Инлайн-объявление в `plugin.json` (цитата оттуда же):

```json
{
  "name": "my-plugin",
  "mcpServers": {
    "plugin-api": { "command": "${CLAUDE_PLUGIN_ROOT}/servers/api-server", "args": ["--port", "8080"] }
  }
}
```

Схема SchemaStore для `plugin.json` уточняет допустимые значения `mcpServers`: либо строка-путь, обязанная начинаться с `./` и кончаться на `.json`; либо путь/URL к MCPB-файлу; либо объект «имя → конфиг сервера», где для stdio требуется `command` (плюс `args`, `env`), для `http`/`sse` — `type` и `url` (плюс `headers`, `headersHelper`, `oauth`).

Транспорты: поддерживаются stdio, SSE, HTTP и WebSocket. Источник: [Plugin MCP features](https://code.claude.com/docs/en/mcp#plugin-provided-mcp-servers).

### 2.2 Чем отличается от проектного и пользовательского `.mcp.json`

| | Плагинный `.mcp.json` | Проектный `.mcp.json` | Пользовательский / local |
| --- | --- | --- | --- |
| Расположение | корень плагина | корень проекта | `~/.claude.json` |
| Как включается | ставится/удаляется вместе с плагином, стартует автоматически при включённом плагине | требует одобрения в интерактивной сессии | добавляется `claude mcp add --scope user\|local` |
| Приоритет при совпадении | 4-й | 2-й | local — 1-й, user — 3-й |
| Как выключить точечно | `disabledMcpServers` (тумблер в `/mcp`) | `disabledMcpjsonServers` / `enabledMcpjsonServers` в settings | `disabledMcpServers` |
| Имя инструмента | `mcp__plugin_<plugin>_<server>__<tool>` | `mcp__<server>__<tool>` | `mcp__<server>__<tool>` |

Источники: [Scope hierarchy and precedence](https://code.claude.com/docs/en/mcp#scope-hierarchy-and-precedence), [Project scope](https://code.claude.com/docs/en/mcp#project-scope), [Disable a server without removing it](https://code.claude.com/docs/en/mcp#disable-a-server-without-removing-it), [Plugin MCP tool names](https://code.claude.com/docs/en/mcp#plugin-provided-mcp-servers).

Ключевые следствия из первоисточника:

- Порядок приоритета при дубликатах: local → project → user → **плагинные серверы** → коннекторы claude.ai. Три обычных scope матчатся по **имени**, а плагины и коннекторы — по **endpoint** (URL или команде). То есть плагинный сервер считается дубликатом, если совпадает URL/команда с сервером выше по приоритету. Источник: [Scope hierarchy and precedence](https://code.claude.com/docs/en/mcp#scope-hierarchy-and-precedence).
- Проектный `.mcp.json` требует явного одобрения в интерактивной сессии (`claude mcp reset-project-choices` сбрасывает решения). Плагинный `.mcp.json` такого prompt'а не имеет — он часть плагина, который пользователь уже решил поставить. Источник: [Project scope](https://code.claude.com/docs/en/mcp#project-scope), [Plugin-provided MCP servers](https://code.claude.com/docs/en/mcp#plugin-provided-mcp-servers).
  - Исключение: у **skills-directory-плагина проектного scope** (`<cwd>/.claude/skills/<name>/.claude-plugin/plugin.json`) объявленные MCP-серверы проходят «the same per-server approval as a project `.mcp.json`». Источник: [Skills-directory plugins](https://code.claude.com/docs/en/plugins-reference#skills-directory-plugins).
- Полное имя инструмента плагинного сервера — `mcp__plugin_<plugin-name>_<server-name>__<tool-name>`; сам сервер регистрируется под именем `plugin:<plugin-name>:<server-name>`. Именно эти формы надо использовать в permission-правилах, в `allowed-tools` скилла, в поле `tools` сабагента и в hook-матчерах; матчер по голому имени сервера (`mcp__database-tools__.*`) **никогда не сработает**. Источник: [Plugin MCP tool names](https://code.claude.com/docs/en/mcp#plugin-provided-mcp-servers).
- `/reload-plugins` сохраняет живые подключения тех плагинных серверов, чья конфигурация не изменилась. Источник: [Plugin MCP features](https://code.claude.com/docs/en/mcp#plugin-provided-mcp-servers).

### 2.3 Что будет, если у пользователя нет конфигурации для объявленного сервера

Документированы два случая:

1. **Удалённый сервер с пустым `url`.** Показывается как `not configured` в `/mcp`, в `claude mcp list` и в менеджере `/plugin`; подключаться Claude Code не пытается и ошибкой это не считает. Документация прямо называет это штатным приёмом: «A plugin can include a placeholder entry like this for a connector you configure later, so Claude Code doesn't report it as an error or a setup issue». Детальный вид сервера пишет `No URL configured for this server`. Требует ≥ v2.1.208. Источник: [Server status detail](https://code.claude.com/docs/en/mcp#server-status-detail).
2. **Сервер, к которому не удалось подключиться.** Статус `✘ Failed to connect`, к строке добавляется деталь ошибки (HTTP-код и текст сервера), `claude mcp get <name>` показывает её в строке `Issue:`. При включённом tool search Claude Code сообщает модели, какой сервер упал и с какой ошибкой. HTTP/SSE переподключаются с backoff'ом; **stdio-серверы — локальные процессы и автоматически не переподключаются**. Источники: [Server status](https://code.claude.com/docs/en/mcp#server-status), [Server status detail](https://code.claude.com/docs/en/mcp#server-status-detail), [Automatic reconnection](https://code.claude.com/docs/en/mcp#automatic-reconnection).

**Не подтверждено:** аналога `not configured` для **stdio**-сервера в документации нет. Что именно происходит со stdio-сервером, чей бинарь есть, но конфиг-файл источника отсутствует, документация не описывает — это целиком на стороне самого сервера (он либо стартует и отвечает ошибкой на вызов, либо падает и получает `✘ Failed to connect`). Неизвестно также, есть ли способ пометить stdio-запись как «пока не настроена», чтобы она не считалась проблемой.

Смежный документированный рычаг: `defaultEnabled: false` в `plugin.json` или в записи маркетплейса — плагин ставится выключённым, пользователь включает его сам. Документация советует это ровно для случая «плагин добавляет стоимость или объём, на который пользователь должен согласиться, например подключается к внешнему сервису». Источник: [Default enablement](https://code.claude.com/docs/en/plugins-reference#default-enablement).

---

## 3. `${CLAUDE_PLUGIN_ROOT}` и остальные переменные

Да, `${CLAUDE_PLUGIN_ROOT}` доступна в плагинном `.mcp.json` — это её основной сценарий. Всего документировано **три** path-переменных:

| Переменная | Во что резолвится | Для чего |
| --- | --- | --- |
| `${CLAUDE_PLUGIN_ROOT}` | абсолютный путь к каталогу установки плагина | скрипты, бинари и конфиги, поставляемые плагином |
| `${CLAUDE_PLUGIN_DATA}` | `~/.claude/plugins/data/{id}/`, переживает обновления, создаётся при первом обращении | зависимости, сгенерированный код, кэши |
| `${CLAUDE_PROJECT_DIR}` | корень проекта | проектные скрипты и конфиги |

Источник: [Plugins reference → Environment variables](https://code.claude.com/docs/en/plugins-reference#environment-variables).

Все три **экспортируются как переменные окружения** в hook-процессы и в подпроцессы MCP- и LSP-серверов. Там же.

Где они раскрываются инлайном (таблица оттуда же):

| Компонент | Поля, где подставляются плейсхолдеры |
| --- | --- |
| Содержимое скиллов и агентов | везде |
| Команды хуков и мониторов | везде |
| MCP `stdio` | `command`, `args`, `env` |
| MCP `http`, `sse`, `ws` | `url`, `headers`, `headersHelper` |
| LSP | `command`, `args`, `env`, `workspaceFolder` |

То же подтверждено в [MCP → Plugin MCP features](https://code.claude.com/docs/en/mcp#plugin-provided-mcp-servers) (там же оговорка: до v2.1.195 `headersHelper` получал плейсхолдер как литеральную строку).

Отдельный документированный факт про `${CLAUDE_PROJECT_DIR}`: в **проектном** `.mcp.json` и в `~/.claude.json` эта переменная в `command`/`args` требует дефолта (`${CLAUDE_PROJECT_DIR:-.}`), потому что она ставится в окружение сервера, а не самого Claude Code. **Плагинные же MCP-конфиги подставляют `${CLAUDE_PROJECT_DIR}` напрямую, дефолт не нужен.** Источник: [MCP → Option 3: Add a local stdio server](https://code.claude.com/docs/en/mcp#option-3-add-a-local-stdio-server).

Предупреждение из документации: `${CLAUDE_PLUGIN_ROOT}` **меняется при каждом обновлении плагина**; каталог предыдущей версии остаётся на диске лишь на время grace-периода — писать туда состояние нельзя. Для состояния — `${CLAUDE_PLUGIN_DATA}`. Источник: [Environment variables](https://code.claude.com/docs/en/plugins-reference#environment-variables).

Ещё две подстановки, специфичные для плагинов:

- `${user_config.KEY}` — значения из `userConfig`, доступны в конфигах MCP- и LSP-серверов и в командах хуков; несенситивные — также в содержимом скиллов и агентов. Поля, исполняющиеся в шелле (shell-form hook commands, monitor commands, MCP `headersHelper`), **отвергают** `${user_config.*}` — компонент падает с ошибкой; до v2.1.207 подставляли. Источник: [User configuration](https://code.claude.com/docs/en/plugins-reference#user-configuration).
- `CLAUDE_PLUGIN_OPTION_<KEY>` — все значения `userConfig` экспортируются в hook-процессы как переменные окружения (ключ в верхнем регистре). Там же.

**Не подтверждено:** экспортируются ли `CLAUDE_PLUGIN_OPTION_<KEY>` в окружение **MCP-сервера** (документация называет только hook-процессы). Также нигде не сказано, раскрывается ли `${user_config.*}` в поле `command` stdio-сервера — в списке отвергающих полей его нет, но и явного разрешения нет.

Наконец, `roots/list`: MCP-сервер может запросить рабочие каталоги сессии в рантайме. Claude Code отвечает launch-каталогом плюс всеми добавленными через `--add-dir`/`/add-dir`/`additionalDirectories`, и шлёт `notifications/roots/list_changed` при изменении набора (≥ v2.1.203). Источник: [MCP → Option 3](https://code.claude.com/docs/en/mcp#option-3-add-a-local-stdio-server).

---

## 4. Подстановка переменных окружения в плагинном `.mcp.json`

**Синтаксис, документированный для `.mcp.json`:**

- `${VAR}` — значение переменной окружения `VAR`;
- `${VAR:-default}` — `VAR`, если задана, иначе `default`.

Поля, где раскрывается: `command`, `args`, `env`, `url`, `headers`. Источник: [MCP → Environment variable expansion in `.mcp.json`](https://code.claude.com/docs/en/mcp#environment-variable-expansion-in-mcp-json).

Поведение при отсутствии переменной и без дефолта: **конфиг всё равно грузится**, Claude Code показывает missing-variable warning для этого сервера в выводе `claude mcp list` и использует нераскрытый текст `${VAR}` как есть. Там же.

**Применимо ли это к плагинному `.mcp.json`?** Прямого утверждения «раздел про expansion распространяется и на плагинный `.mcp.json`» в документации нет — раздел живёт внутри главы про scope'ы. Но подтверждений косвенных достаточно:

1. Документированный пример плагинного `.mcp.json` содержит `"env": { "DB_URL": "${DB_URL}" }` — [MCP → Plugin-provided MCP servers](https://code.claude.com/docs/en/mcp#plugin-provided-mcp-servers).
2. Там же в списке фич плагинных серверов: «**User environment access**: access to the same environment variables as manually configured servers».
3. Формулировка про `${CLAUDE_PROJECT_DIR:-.}` («Plugin-provided MCP configurations substitute `${CLAUDE_PROJECT_DIR}` directly and don't need the default») предполагает, что механизм `${VAR:-default}` в плагинных конфигах в принципе действует, иначе оговорка была бы бессмысленной — [MCP → Option 3](https://code.claude.com/docs/en/mcp#option-3-add-a-local-stdio-server).
4. Живой первоисточник: официальный плагин Anthropic использует форму с дефолтом прямо в плагинном `.mcp.json` — `/Users/conte/.claude/plugins/marketplaces/claude-plugins-official/external_plugins/context7/.mcp.json` содержит `"Authorization": "${CONTEXT7_API_KEY:-}"`. Соседний `.../github/.mcp.json` использует `"Authorization": "Bearer ${GITHUB_PERSONAL_ACCESS_TOKEN}"`, `.../greptile/.mcp.json` — `"Bearer ${GREPTILE_API_KEY}"`.

**Отличия от проектного `.mcp.json`:** документировано ровно одно — обработка `${CLAUDE_PROJECT_DIR}` (см. п. 3). Никаких других различий в синтаксисе или наборе полей подстановки документация не называет.

**Не подтверждено:** раскрывается ли `${VAR}`/`${VAR:-default}` в плагинном `.mcp.json` в поле `env` **применительно к переменным, которых нет в окружении Claude Code** — точнее, ведёт ли missing-variable warning себя для плагинного сервера так же, как описано для проектного (текст про warning в `claude mcp list` привязан к разделу про scope'ы). Также неизвестно, применяется ли `${VAR}`-подстановка к полю `headersHelper` в плагинном конфиге (плейсхолдеры `${CLAUDE_*}` — да, про обычные env-переменные не сказано).

---

## 5. Что ещё плагин может поставлять

### 5.1 Permissions и settings — почти ничего

Плагин может положить `settings.json` **в корень плагина**; настройки применяются, пока плагин включён. Но: «Currently, only the `agent` and `subagentStatusLine` keys are supported», незнакомые ключи молча игнорируются, а `settings.json` имеет приоритет над ключом `settings` в `plugin.json`. Источник: [Create plugins → Ship default settings with your plugin](https://code.claude.com/docs/en/plugins#ship-default-settings-with-your-plugin), дублируется в таблице [File locations reference](https://code.claude.com/docs/en/plugins-reference#file-locations-reference).

**Вывод: поставить permission-правила (`allow`/`deny`/`ask`) из плагина нельзя** — `permissions` не входит в разрешённый список ключей. Отдельно документировано, что и для агентов, поставляемых плагином, `permissionMode`, `hooks` и `mcpServers` во frontmatter запрещены «for security reasons». Источник: [Plugins reference → Agents](https://code.claude.com/docs/en/plugins-reference#agents).

Косвенное подтверждение из схемы SchemaStore для `plugin.json`: поле `settings` описано как «Settings to merge into the user settings while this plugin is enabled. **Only the documented allowlisted keys are applied.**»

**Не подтверждено:** список allowlisted-ключей может расширяться между релизами; на момент проверки документация называет только `agent` и `subagentStatusLine`. Способа предложить пользователю permission-правило из плагина (например, авто-allow для read-инструментов) в документации не нашлось.

### 5.2 Hooks установки — отдельного install-хука нет

Событий жизненного цикла много (`SessionStart`, `Setup`, `PreToolUse`, `PostToolUse`, `SessionEnd`, `ConfigChange`, `FileChanged` и др. — полная таблица в [Plugins reference → Hooks](https://code.claude.com/docs/en/plugins-reference#hooks)), но **события «плагин установлен» или «плагин обновлён» среди них нет**.

Документированный паттерн для «скачать/собрать зависимость» — `SessionStart`-хук, пишущий в `${CLAUDE_PLUGIN_DATA}`, с диффом манифеста для детекта обновления. Цитата из [Persistent data directory](https://code.claude.com/docs/en/plugins-reference#persistent-data-directory):

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "diff -q \"${CLAUDE_PLUGIN_ROOT}/package.json\" \"${CLAUDE_PLUGIN_DATA}/package.json\" >/dev/null 2>&1 || (cd \"${CLAUDE_PLUGIN_DATA}\" && cp \"${CLAUDE_PLUGIN_ROOT}/package.json\" . && npm install) || rm -f \"${CLAUDE_PLUGIN_DATA}/package.json\""
          }
        ]
      }
    ]
  }
}
```

Оговорки:

- `SessionStart` выполняется на **каждой** сессии, документация просит держать такие хуки быстрыми; поддерживаются только `type: "command"` и `type: "mcp_tool"`. Источник: [Hooks → SessionStart](https://code.claude.com/docs/en/hooks#sessionstart).
- Событие `Setup` — не про установку плагина: оно срабатывает только при запуске с `--init-only`, либо `--init`/`--maintenance` в `-p`-режиме, и предназначено для CI/скриптов. Источник: [Hooks → Setup](https://code.claude.com/docs/en/hooks#setup).
- Node-зависимости плагина Claude Code ставит **сам**, при копировании в кэш, если в корне есть `package.json` **и** поддерживаемый lockfile (`bun.lock`/`bun.lockb` → `bun install --frozen-lockfile --ignore-scripts`; `npm-shrinkwrap.json`/`package-lock.json` → `npm ci --ignore-scripts`). `yarn.lock` и `pnpm-lock.yaml` пропускаются. Лимит 60 секунд, lifecycle-скрипты выключены, отключить механизм нельзя. Источник: [Node.js package dependencies](https://code.claude.com/docs/en/plugins-reference#node-js-package-dependencies).
- Каталог `bin/` в корне плагина: файлы оттуда добавляются в `PATH` **Bash-инструмента** и вызываются как голые команды, пока плагин включён. Источник: [File locations reference](https://code.claude.com/docs/en/plugins-reference#file-locations-reference). **Не подтверждено:** резолвится ли `command` MCP-сервера против этого `PATH` — документация говорит только про Bash-инструмент, так что для MCP-сервера безопаснее писать полный путь через `${CLAUDE_PLUGIN_ROOT}`.

### 5.3 Slash-команды и скиллы — да

- **Скиллы:** каталог `skills/` в корне плагина, каждый скилл — папка с `SKILL.md` (плюс опциональные `reference.md`, `scripts/`). Обнаруживаются автоматически. Плагин с ровно одним скиллом может положить `SKILL.md` прямо в корень. Имя вызова берётся из frontmatter-поля `name`; без него — из имени каталога установки, а у marketplace-плагина это строка версии, меняющаяся при каждом обновлении. Источник: [Plugins reference → Skills](https://code.claude.com/docs/en/plugins-reference#skills).
- **Slash-команды:** каталог `commands/` — плоские `.md`-файлы. Документация называет их «Skills as flat Markdown files» и рекомендует для новых плагинов использовать `skills/`. Источник: та же таблица [File locations reference](https://code.claude.com/docs/en/plugins-reference#file-locations-reference).
- Скиллы и агенты namespace'ятся именем плагина: агент `code-reviewer` плагина `my-plugin` виден как `my-plugin:code-reviewer`. Источник: [Plugins reference → Agents](https://code.claude.com/docs/en/plugins-reference#agents), [Required fields](https://code.claude.com/docs/en/plugins-reference#required-fields).

### 5.4 Прочее, что плагин может нести

`agents/`, `workflows/`, `output-styles/`, `.lsp.json` (LSP-серверы), `experimental.themes`, `experimental.monitors` (фоновые мониторы), `channels` (каналы, привязанные к MCP-серверу плагина), `dependencies` (зависимости от других плагинов с semver-констрейнтами), `userConfig` (значения, которые Claude Code спрашивает у пользователя при включении плагина; сенситивные уходят в Keychain, остальные — в `pluginConfigs` в user `settings.json`). Источники: [Component path fields](https://code.claude.com/docs/en/plugins-reference#component-path-fields), [User configuration](https://code.claude.com/docs/en/plugins-reference#user-configuration), [Channels](https://code.claude.com/docs/en/plugins-reference#channels).

Важное ограничение: `CLAUDE.md` в корне плагина **не** загружается как контекст. «Plugins contribute context through skills, agents, and hooks rather than CLAUDE.md.» Источник: [Plugin directory structure](https://code.claude.com/docs/en/plugins-reference#plugin-directory-structure).

---

## 6. Обновление плагина и судьба пользовательских правок

### 6.1 Команды и автообновление

- Ручное: `/plugin update <plugin>` или `claude plugin update <plugin>` (флаг `-s/--scope`: `user | project | local | managed`, по умолчанию `user`). Источник: [plugin update](https://code.claude.com/docs/en/plugins-reference#plugin-update).
- Обновление каталога маркетплейса: `/plugin marketplace update <name>` / `claude plugin marketplace update <name>`. Источник: [Manage marketplaces](https://code.claude.com/docs/en/discover-plugins#use-cli-commands).
- Автообновление: Claude Code проверяет обновления маркетплейсов и плагинов после старта сессии со случайной задержкой до 10 минут; текущая сессия продолжает работать на загруженных версиях, а при появлении новых показывается уведомление с предложением `/reload-plugins`. **Официальные маркетплейсы Anthropic имеют автообновление включённым по умолчанию, сторонние и локальные — выключенным.** Тумблер — в `/plugin` → Marketplaces. `DISABLE_AUTOUPDATER` выключает и его; `FORCE_AUTOUPDATE_PLUGINS=1` вместе с ним оставляет автообновление плагинов. Источник: [Configure auto-updates](https://code.claude.com/docs/en/discover-plugins#configure-auto-updates).
- Удаление маркетплейса удаляет и все установленные из него плагины (Warning в [Manage marketplaces](https://code.claude.com/docs/en/discover-plugins#use-cli-commands)).

### 6.2 Что физически происходит с файлами

- Marketplace-плагины **копируются** в кэш `~/.claude/plugins/cache`, а не используются на месте (исключение — `command`-source в link mode). Каждая установленная версия — **отдельный каталог**, сгруппированный по маркетплейсу и плагину и названный по разрешённой версии. Источник: [Plugin caching and file resolution](https://code.claude.com/docs/en/plugins-reference#plugin-caching-and-file-resolution).
  - Подтверждается локально: `/Users/conte/.claude/plugins/cache/mattpocock/mattpocock-skills/1.2.3/` и `.../1.2.2/` лежат рядом; `installed_plugins.json` хранит `installPath`, `version`, `gitCommitSha`, `installedAt`, `lastUpdated`.
- При обновлении или удалении каталог предыдущей версии помечается orphaned и сносится фоновой чисткой примерно через 14 дней. Grace-период нужен, чтобы уже запущенные сессии не падали. Sweep выполняется только пока установлен хотя бы один плагин. Источник: там же.
- Инструменты Glob и Grep пропускают orphaned-каталоги при поиске. Там же.
- При обновлении **посреди сессии** hook-команды, мониторы, MCP- и LSP-серверы продолжают использовать путь предыдущей версии; `/reload-plugins` переключает хуки, MCP и LSP на новый путь, мониторам нужен рестарт сессии. Источник: [Environment variables](https://code.claude.com/docs/en/plugins-reference#environment-variables).

### 6.3 Правки пользователя

Прямого утверждения «правки пользователя в файлах плагина теряются при обновлении» в документации **нет** — помечаю как вывод, а не как цитату. Но он следует из документированной механики:

- новая версия — это новый каталог в кэше со своей копией файлов, старый помечается orphaned и удаляется через ~14 дней ([Plugin caching](https://code.claude.com/docs/en/plugins-reference#plugin-caching-and-file-resolution));
- `${CLAUDE_PLUGIN_ROOT}` меняется при обновлении, и документация прямо просит **не хранить там состояние**: «treat it as ephemeral and don't write state there» ([Environment variables](https://code.claude.com/docs/en/plugins-reference#environment-variables)).

Что действительно переживает обновление:

- **`${CLAUDE_PLUGIN_DATA}`** (`~/.claude/plugins/data/{id}/`, где `{id}` — идентификатор плагина с заменой символов вне `[a-zA-Z0-9_-]` на `-`; для `formatter@my-marketplace` это `~/.claude/plugins/data/formatter-my-marketplace/`). Удаляется автоматически при удалении плагина из последнего scope, если не передан `--keep-data`. Источник: [Persistent data directory](https://code.claude.com/docs/en/plugins-reference#persistent-data-directory).
- **Настройки пользователя.** Запись в `enabledPlugins` в любом scope переживает обновления и переустановки, поэтому изменение `defaultEnabled` в новом релизе не переключает существующего пользователя. Источник: [Default enablement](https://code.claude.com/docs/en/plugins-reference#default-enablement).
- **`userConfig`-значения** живут в `pluginConfigs[<plugin-id>].options` в user `settings.json` (несенситивные) и в Keychain / `~/.claude/.credentials.json` (сенситивные) — то есть вне каталога плагина. Источник: [User configuration](https://code.claude.com/docs/en/plugins-reference#user-configuration).
- **Скиллы-директории (`@skills-dir`-плагины)** обнаруживаются **на месте**, а не копируются в кэш, и у них нет шага установки — редактирование `SKILL.md` действует сразу, изменения остальных компонентов требуют `/reload-plugins`. Источник: [Skills-directory plugins](https://code.claude.com/docs/en/plugins-reference#skills-directory-plugins).
- **Симлинк development-чекаута в кэш** Claude Code никогда не помечает orphaned и не удаляет, и не пишет внутрь связанного чекаута свои version-tracking файлы. Источник: [Plugin caching and file resolution](https://code.claude.com/docs/en/plugins-reference#plugin-caching-and-file-resolution).

**Не подтверждено:** предупреждает ли Claude Code пользователя перед перезаписью, если тот правил файлы в кэше; и есть ли документированный механизм миграции пользовательских данных между версиями кроме `${CLAUDE_PLUGIN_DATA}`.

---

## Что это значит для проекта

Дизайн, где плагин передаёт серверу `--profile default`, а бинарь сам вычисляет путь конфига по XDG, ложится на платформу почти без трения. Выводы:

1. **XDG-подход выбран верно.** Плагин не может поставить пользователю ни permission-правила, ни настройки (в `settings.json` плагина работают только `agent` и `subagentStatusLine`), и не может писать состояние в свой каталог — `${CLAUDE_PLUGIN_ROOT}` меняется при каждом обновлении. Конфиг обязан жить снаружи плагина, и XDG — единственный вариант, который не зависит от механики кэша.
2. **`--profile default` в `args` — валидно и безопасно.** `args` подставляет `${CLAUDE_PLUGIN_ROOT}`/`${CLAUDE_PLUGIN_DATA}`/`${CLAUDE_PROJECT_DIR}` и env-переменные, так что запись вида `"args": ["--profile", "${INFRA_MCP_PROFILE:-default}"]` даёт пользователю переключение профиля переменной окружения без правки файлов плагина. Правка `.mcp.json` в кэше при обновлении затирается — переменная окружения нет.
3. **`.mcp.json` в корне плагина, форма с обёрткой `mcpServers`.** Бесключевую форму используют официальные плагины Anthropic, но она не документирована — на неё не опираться.
4. **«Деградированный старт» — правильная стратегия для stdio.** Документированного `not configured`-статуса для stdio-серверов нет (он существует только для remote-серверов с пустым `url`). Значит сервер без конфига должен стартовать успешно и объяснять проблему через диагностический инструмент: иначе пользователь получит `✘ Failed to connect` без внятной причины, а stdio-серверы ещё и не переподключаются автоматически.
5. **Шесть серверов в одном `.mcp.json` = шесть процессов при включённом плагине.** Все стартуют автоматически. Пользователь может выключить лишние точечно тумблером в `/mcp` (пишется в `disabledMcpServers` в `~/.claude.json` **по проекту**), но выключить сервер «из коробки» на стороне плагина нельзя — только целиком плагин через `defaultEnabled: false`.
6. **Имена инструментов в permissions длиннее, чем кажется.** Read-инструмент `pg_read_query` сервера `postgres` в плагине `infra-mcp` адресуется как `mcp__plugin_infra-mcp_postgres__pg_read_query`. Это форма для permission-правил, `allowed-tools` скиллов и hook-матчеров. Правила писать пользователю придётся самому — с учётом вывода 1.
7. **Бинарь надо чем-то доставлять.** Автоустановка зависимостей работает только для npm/bun, install-хука не существует, а `bin/` кладёт файлы в `PATH` Bash-инструмента, но про резолв `command` MCP-сервера документация молчит. Варианты: (а) коммитить бинари в репозиторий плагина и указывать полный путь через `${CLAUDE_PLUGIN_ROOT}`; (б) `SessionStart`-хук, качающий бинарь в `${CLAUDE_PLUGIN_DATA}` с диффом версии — этот каталог переживает обновления; (в) требовать предустановки через `brew`/`go install`. Вариант (а) раздувает репозиторий кратно числу платформ, (б) даёт задержку на каждой сессии.
8. **Версионирование: ставить `version` в `plugin.json` явно.** Иначе версией станет git-SHA и любой коммит в репозиторий поедет пользователям как обновление. С явным `version` релиз-цикл контролируемый, но и бампать поле придётся дисциплинированно — без бампа `/plugin update` скажет «already at the latest version».
9. **Маркетплейс и плагин в одном репозитории — рабочая схема.** `.claude-plugin/marketplace.json` в корне, `"source": "./"` или `"./plugins/infra-mcp"`. Установка: `/plugin marketplace add Conte777/infra-mcp`, затем `/plugin install infra-mcp@infra-mcp`. Учесть: у стороннего маркетплейса автообновление по умолчанию **выключено** — пользователи будут обновляться руками, пока сами не включат тумблер.
10. **Документацию по конфигу поставлять скиллом, а не `CLAUDE.md`.** `CLAUDE.md` в корне плагина не грузится в контекст. Инструкцию «где лежит конфиг, как добавить профиль» разумно оформить скиллом в `skills/`, а для интерактивного заполнения подключений посмотреть в сторону `userConfig` (значения доступны как `${user_config.KEY}` в конфиге MCP-сервера, сенситивные уходят в Keychain).
