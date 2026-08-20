# Рабочий контур MCP-сервера на `github.com/modelcontextprotocol/go-sdk` v1.2.0

Исследование по [issue #4](https://github.com/Conte777/infra-mcp/issues/4).

**Версия**: `github.com/modelcontextprotocol/go-sdk v1.2.0` (go.mod требует Go >= 1.23.0).
Транзитивные зависимости, важные для темы: `github.com/google/jsonschema-go v0.3.0`.
Источник: [`go.mod`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/go.mod).

**Как проверялось**: исходники v1.2.0 из модульного кеша (`go get github.com/modelcontextprotocol/go-sdk@v1.2.0`),
плюс прогон собранного пробного сервера по stdio и по streamable HTTP с реальными JSON-RPC-сообщениями.
Все JSON-выводы ниже — фактический ответ сервера, а не реконструкция. Примеры кода взяты из
пробного сервера, который собирается и проходит `go vet` против v1.2.0; фрагменты с `...` —
сокращения того же кода.

> Осторожно с документацией из веба и context7: там по умолчанию отдаётся ветка `main`, где сигнатуры
> уже разошлись с v1.2.0 (например, `toolForErr(t, h, s.opts.SchemaCache)` — поля `SchemaCache`
> в `ServerOptions` v1.2.0 нет). Ссылки в этом документе привязаны к тегу `v1.2.0`.

---

## 1. Регистрация инструмента: типизированный вход и JSON Schema

### Две API-точки

Низкоуровневая — [`(*Server).AddTool`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L231):

```go
func (s *Server) AddTool(t *Tool, h ToolHandler)
type ToolHandler func(context.Context, *CallToolRequest) (*CallToolResult, error)
```

Она ничего не делает за вас: схему нужно задать руками, валидация входа/выхода, заполнение
`Content`/`StructuredContent`/`IsError` — на вас
([doc-комментарий](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L209-L230)).
`InputSchema` обязателен и должен быть `type: "object"`, иначе `panic`.

Рабочая — обобщённая функция верхнего уровня
[`mcp.AddTool`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L447):

```go
func AddTool[In, Out any](s *Server, t *Tool, h ToolHandlerFor[In, Out])

type ToolHandlerFor[In, Out any] func(
	_ context.Context, request *CallToolRequest, input In,
) (result *CallToolResult, output Out, _ error)
```

Что она делает автоматически
([`ToolHandlerFor` doc](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/tool.go#L29-L57)):
выводит входную схему из `In`, разбирает и валидирует аргументы против неё (невалидный вход до
хендлера не доходит), выводит выходную схему из `Out` (если `Out != any`), кладёт `Out` в
`StructuredContent`, а обычную Go-ошибку упаковывает в `IsError`.

### Генерация схемы из Go-структуры

Схема выводится через `jsonschema.ForType` из `github.com/google/jsonschema-go`
([`setSchema`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L389-L410)).
Правила для структур
([`infer.go`](https://github.com/google/jsonschema-go/blob/v0.3.0/jsonschema/infer.go#L181-L217)):

- имя свойства берётся из тега `json`;
- **описание свойства — из тега `jsonschema`**, целиком, как строка описания. Пустой тег — ошибка;
  значение не должно начинаться с `WORD=` (зарезервировано на будущее);
- **обязательность**: поле попадает в `required`, если у него **нет** `omitempty` и **нет** `omitzero`.
  То есть обязательное — это состояние по умолчанию, опциональность включается через `json:",omitempty"`;
- у структур всегда проставляется `additionalProperties: false`;
- указатель `*T` даёт `"type": ["null", T]`;
- `In` должен быть структурой или map (чтобы схема была `object`). Особый случай: `In = any` даёт
  пустую схему `{"type": "object"}`
  ([`toolForErr`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L281-L284)).

```go
type ReadQueryInput struct {
	SQL     string `json:"sql" jsonschema:"SQL-запрос, только SELECT"`
	MaxRows int    `json:"max_rows,omitempty" jsonschema:"верхняя граница числа строк"`
}

mcp.AddTool(s, &mcp.Tool{
	Name:        "pg_read_query",
	Description: "Выполнить SELECT и вернуть markdown-таблицу.",
}, src.readQuery)
```

Фактический `tools/list` (проверено прогоном):

```json
{
  "name": "pg_read_query",
  "description": "Выполнить SELECT и вернуть markdown-таблицу.",
  "inputSchema": {
    "type": "object",
    "required": ["sql"],
    "properties": {
      "max_rows": {"type": "integer", "description": "верхняя граница числа строк"},
      "sql": {"type": "string", "description": "SQL-запрос, только SELECT"}
    },
    "additionalProperties": false
  }
}
```

Вызов без обязательного `sql` до хендлера не доходит — это **протокольная** ошибка (проверено):

```json
{"jsonrpc":"2.0","id":4,"error":{"code":-32602,
 "message":"invalid params: validating \"arguments\": validating root: required: missing properties: [\"sql\"]"}}
```

Если нужна схема тоньше, чем выводится (диапазоны, enum), её строят вручную через
`jsonschema.For[T](opts)` и кладут в `Tool.InputSchema` / `Tool.OutputSchema` —
`AddTool` тогда inference не делает
([docs/server.md](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/docs/server.md#L362-L395)).
Ограничение: `AddTool` умеет валидировать только draft 2020-12
([`Tool.InputSchema` doc](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/protocol.go#L1056-L1069)).

### Ограничение на имя

[`validateToolName`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/tool.go#L104-L133):
непустое, ≤ 128 символов, только `[a-zA-Z0-9_.-]`. Нарушение **не** паникует — пишется `Logger.Error`
([`server.go#L232`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L232-L234)),
то есть кривое имя молча уедет в продакшн. Наш `pg_read_query` под правила подходит.

---

## 2. `ToolAnnotations`

### Структура

[`mcp/protocol.go#L1105`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/protocol.go#L1105):

```go
type ToolAnnotations struct {
	DestructiveHint *bool  `json:"destructiveHint,omitempty"` // default: true
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`  // default: false
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`   // default: true
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`    // default: false
	Title           string `json:"title,omitempty"`
}
```

**Асимметрия типов важна.** Хинты со значением по умолчанию `true` (`DestructiveHint`, `OpenWorldHint`)
объявлены как `*bool` — только так можно отличить «не задано» от явного `false`. Хинты со значением
по умолчанию `false` (`ReadOnlyHint`, `IdempotentHint`) — обычные `bool` с `omitempty`,
и **явный `false` записать невозможно**: он просто исчезнет из JSON, что эквивалентно значению
по умолчанию. Это совпадает с дефолтами из спеки
([schema.ts 2025-06-18, `ToolAnnotations`](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/schema/2025-06-18/schema.ts)).

Практический вывод: чтобы сказать «инструмент безопасный и закрытый», недостаточно `ReadOnlyHint: true` —
нужно ещё явно гасить `DestructiveHint`/`OpenWorldHint`, иначе они остаются `true` по умолчанию спеки.

```go
func ptr[T any](v T) *T { return &v }

mcp.AddTool(s, &mcp.Tool{
	Name: "pg_read_query",
	Annotations: &mcp.ToolAnnotations{
		ReadOnlyHint:    true,
		IdempotentHint:  true,
		DestructiveHint: ptr(false),
		OpenWorldHint:   ptr(false),
	},
}, src.readQuery)
```

### Попадают ли в список инструментов

Да. `Annotations` — поле [`Tool`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/protocol.go#L1050),
а [`listTools`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L665-L677)
отдаёт `*Tool` как есть. Фактический ответ (проверено):

```json
{"name":"pg_diagnostics",
 "annotations":{"destructiveHint":false,"idempotentHint":true,"openWorldHint":false,"readOnlyHint":true},
 "inputSchema":{"type":"object"}}
```

### Что с ними делает клиент

Сам SDK — **ничего**: `ToolAnnotations` нигде в `mcp/*.go` (кроме тестов) не читается, только
сериализуется. Это чистый passthrough.

Спека: «all properties in ToolAnnotations are hints… Clients should never make tool use decisions
based on ToolAnnotations» и «clients **MUST** consider tool annotations to be untrusted unless they
come from trusted servers»
([spec/server/tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools),
[SDK doc](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/protocol.go#L1097-L1104)).

Что делают клиенты на практике (по блогу MCP,
[Tool Annotations as Risk Vocabulary](https://blog.modelcontextprotocol.io/posts/2026-03-16-tool-annotations/)):
`readOnlyHint: true` от доверенного сервера может авто-одобряться; `destructiveHint: true` даёт
предупреждение перед выполнением; `idempotentHint` разрешает авто-ретрай; `openWorldHint` помечает
пересечение границы доверия и заставляет пристальнее смотреть на выход.

> **Не подтверждено первичным источником.** Конкретное поведение Claude Code (авто-одобрение
> read-only инструментов, распараллеливание вызовов, синтаксис правила `mcp__*[readOnly]`) в
> официальной документации Anthropic я подтвердить не смог — это встречается только в обсуждениях
> issue ([anthropics/claude-code#30142](https://github.com/anthropics/claude-code/issues/30142),
> [#12368](https://github.com/anthropics/claude-code/issues/12368)). Планировать permissions,
> опираясь только на аннотации, нельзя: правила `.mcp.json`/`permissions` надёжнее.

---

## 3. `instructions` сервера

### Где задаются

[`ServerOptions.Instructions`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L58-L60):

```go
server := mcp.NewServer(
	&mcp.Implementation{Name: "pg-mcp", Title: "Postgres MCP", Version: "0.1.0"},
	&mcp.ServerOptions{
		Instructions: "Инструменты доступа к Postgres. Начинайте с `pg_read_query`.",
	},
)
```

### Когда передаются

**Один раз, в ответе на `initialize`**, и больше нигде. Единственное место чтения —
[`ServerSession.initialize`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L1350-L1366):

```go
return &InitializeResult{
	ProtocolVersion: negotiatedVersion(params.ProtocolVersion),
	Capabilities:    s.capabilities(),
	Instructions:    s.opts.Instructions,
	ServerInfo:      s.impl,
}, nil
```

Фактический ответ (проверено):

```json
{"jsonrpc":"2.0","id":1,"result":{
  "capabilities":{"logging":{},"tools":{"listChanged":true}},
  "instructions":"Инструменты доступа к Postgres. Начинайте с `pg_read_query`.",
  "protocolVersion":"2025-06-18",
  "serverInfo":{"name":"pg-mcp","title":"Postgres MCP","version":"0.1.0"}}}
```

Следствия:

- **Менять `instructions` после старта бессмысленно** — уведомления вида «instructions changed» в
  протоколе нет. `ServerOptions` копируется по значению в `NewServer`
  ([`server.go#L154-L158`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L154-L158)),
  так что внешняя мутация опций после создания сервера тоже не сработает.
- В **stateless** HTTP-режиме `initialize` может вообще не прилететь (см. §5) — если клиент сразу
  шлёт `tools/call`, он `instructions` не увидит. То, что модель обязана знать, должно жить в
  `Tool.Description`, а не только в `instructions`.

Назначение по спеке: «This can be used by clients to improve the LLM's understanding of available
tools, resources, etc. It can be thought of like a "hint" to the model. For example, this information
MAY be added to the system prompt»
([lifecycle](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle),
[schema.ts](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/schema/2025-06-18/schema.ts)).

### Ограничения на размер

**Ни спека, ни SDK не задают лимит.** В `InitializeResult` это обычная `string` без валидации;
поиск по `mcp/*.go` не находит ни одной проверки длины `Instructions`.

> **Не подтверждено.** Предел, если он есть, задаётся конкретным клиентом (сколько он готов влить
> в системный промпт), и в первоисточниках не документирован. Практическое правило — держать
> instructions в пределах десятков строк — это эвристика, не факт из источника.

---

## 4. Ошибки инструмента: Go-ошибка против `IsError: true`

### Три разных исхода

В `ToolHandlerFor` возврат ошибки обрабатывается так
([`toolForErr`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L331-L343)):

```go
if err != nil {
	// Check if this is already a structured JSON-RPC error
	if wireErr, ok := err.(*jsonrpc.Error); ok {
		return nil, wireErr        // → протокольная ошибка JSON-RPC
	}
	// For regular errors, embed them in the tool result as per MCP spec
	var errRes CallToolResult
	errRes.setError(err)           // → результат с IsError:true
	return &errRes, nil
}
```

`setError` ([`protocol.go#L121`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/protocol.go#L121-L125)):

```go
func (r *CallToolResult) setError(err error) {
	r.Content = []Content{&TextContent{Text: err.Error()}}
	r.IsError = true
	r.err = err
}
```

То есть **обычная Go-ошибка уже делает ровно то, что нужно**: `err.Error()` целиком уезжает в
текстовый контент, и модель его видит. Никакого «только в лог» тут нет.

| Возврат из хендлера | Что уходит в ответ | Что видит модель |
|---|---|---|
| `fmt.Errorf(...)` (обычная ошибка) | `result` с `isError:true` и `content:[{type:"text", text: err.Error()}]` | текст ошибки как результат вызова, может исправиться и повторить |
| `&jsonrpc.Error{...}` | JSON-RPC `error` объект | зависит от клиента; обычно это сбой транспорта/протокола, а не «результат» |
| `&mcp.CallToolResult{IsError:true, Content:...}, nil, nil` | то же, что первый вариант, но текст полностью под вашим контролем (markdown!) | ваш текст |

Проверено прогоном. Обычная ошибка:

```json
{"jsonrpc":"2.0","id":3,"result":{
  "content":[{"type":"text","text":"подключение к postgres не удалось: dial tcp: connection refused"}],
  "isError":true}}
```

Протокольные ошибки, которые SDK генерирует сам, минуя ваш хендлер (проверено):
неизвестный инструмент — `-32602 unknown tool "nope"`
([`callTool`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L679-L689));
провал валидации аргументов — `-32602 invalid params: validating "arguments": …`
([`server.go#L313-L321`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L313-L321)).
Второе — известная шероховатость, в коде висит `TODO(#450): should this be considered a tool error?`.

Спека проводит ту же границу: protocol errors (unknown tool, invalid arguments, server errors)
против tool execution errors с `isError: true` (API failures, invalid input data, business logic errors)
([spec/server/tools § Error Handling](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)).
Мотивировка в SDK: «Otherwise, the LLM would not be able to see that an error occurred and self-correct»
([`CallToolResult.IsError` doc](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/protocol.go#L94-L110)).

### Как правильно вернуть человекочитаемую ошибку подключения

Ошибка подключения к источнику — **бизнес-ошибка инструмента**, не протокольная. Два корректных способа:

```go
// Способ 1: обычная Go-ошибка. Проще всего; текст = err.Error(), одна строка.
func (s *Source) readQuery(ctx context.Context, req *mcp.CallToolRequest, in ReadQueryInput) (*mcp.CallToolResult, any, error) {
	if err := s.connect(ctx); err != nil {
		return nil, nil, fmt.Errorf("не удалось подключиться к postgres (профиль %q): %w", s.profile, err)
	}
	...
}

// Способ 2: явный IsError с markdown — когда нужно объяснить, что делать дальше.
func (s *Source) readQuery(ctx context.Context, req *mcp.CallToolRequest, in ReadQueryInput) (*mcp.CallToolResult, any, error) {
	if err := s.connect(ctx); err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(
				"## Источник недоступен\n\nПрофиль `%s`: %v\n\nПроверьте DSN в конфиге и сетевой доступ.",
				s.profile, err)}},
		}, nil, nil
	}
	...
}
```

Чего **не** делать: возвращать `*jsonrpc.Error` при недоступности источника (модель этого не увидит
как результат) и логировать ошибку в `slog`, вернув пустой успешный результат (модель решит, что всё ок).

Логирование при этом ортогонально: пишите в `slog` **и** возвращайте текст. На stdio-транспорте
логи обязаны идти в `stderr` — `stdout` занят протоколом
([`StdioTransport`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/transport.go#L87-L94)
использует `os.Stdin`/`os.Stdout`).

---

## 5. Один `*mcp.Server` под двумя транспортами

`*mcp.Server` — это набор фич (tools/prompts/resources), к которому можно подключить сколько угодно
сессий. `Server.Connect` создаёт новую `*ServerSession` на каждый транспорт
([`server.go#L956`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L956)),
а `NewStreamableHTTPHandler` прямо документирует: «It is OK for getServer to return the same server
multiple times»
([`streamable.go#L165-L170`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/streamable.go#L165-L170)).

```go
func main() {
	httpAddr := flag.String("http", "", "если задан, слушать streamable HTTP на этом адресе")
	flag.Parse()

	srv := newServer(loadConfig()) // одна и та же сборка инструментов

	if *httpAddr == "" {
		// stdio: одна сессия, блокируется до отключения клиента
		if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
		return
	}

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	log.Fatal(http.ListenAndServe(*httpAddr, handler))
}
```

`Server.Run` — «a convenience for servers that handle a single session (or one session at a time).
It need not be called on servers that are used for multiple concurrent connections, as with
`StreamableHTTPHandler`»
([`server.go#L869-L882`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L869-L882)).

### Что означает `Stateless: true`

[`StreamableHTTPOptions`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/streamable.go#L127-L163):

```go
type StreamableHTTPOptions struct {
	Stateless      bool
	JSONResponse   bool
	Logger         *slog.Logger
	EventStore     EventStore
	SessionTimeout time.Duration
}
```

Механика (
[`ServeHTTP`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/streamable.go#L236-L450)):

1. **Заголовок `Mcp-Session-Id` не валидируется.** Неизвестный id не даёт 404 — вместо этого
   создаётся временный транспорт (`streamable.go#L242-L248`).
2. **Сессия живёт ровно один HTTP-запрос**: `defer session.Close()` сразу после обработки
   (`streamable.go#L437-L439`). Никакого состояния между запросами.
3. **Состояние инициализации подделывается.** Если в теле нет `initialize`/`notifications/initialized`,
   SDK подставляет дефолтный `ServerSessionState` с `ProtocolVersion` из заголовка и `LogLevel: "info"`
   (`streamable.go#L352-L397`). Поэтому `tools/call` работает **без предварительного `initialize`** —
   проверено:
   ```
   POST / (без Mcp-Session-Id, без initialize)
   {"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"pg_read_query","arguments":{"sql":"select 1"}}}
   → {"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"| col |\n| --- |\n| 1 |"}]}}
   ```
4. **Server→client запросы запрещены** — жёстко, на уровне записи в соединение
   ([`streamable.go#L1265-L1267`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/streamable.go#L1265-L1267)):
   ```go
   if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() && (c.stateless || c.sessionID == "") {
       return fmt.Errorf("%w: stateless servers cannot make requests", jsonrpc2.ErrRejected)
   }
   ```
   Значит: **никакого sampling (`CreateMessage`), никакого elicitation (`Elicit`), никаких
   `roots/list`**. Нотификации (в т.ч. `Log`, `tools/list_changed`) уходить могут, но только если
   сделаны в контексте входящего запроса — иначе они уедут в standalone SSE-поток, которого в
   stateless нет ([`StreamableServerTransport` doc](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/streamable.go#L473-L492)).
5. **`GET` запрещён**: `stateless || sessionID == ""` → `405 Method Not Allowed`, «GET requires an
   active session» (`streamable.go#L277-L280`). Проверено — `405`. То есть standalone SSE-поток
   (сервер-инициированные сообщения вне запроса) недоступен.
6. **`DELETE`**: без `Mcp-Session-Id` → `400`; с любым id → `204 No Content`, причём никакой сессии
   на самом деле не закрывается, `sessInfo` в stateless всегда `nil` (`streamable.go#L260-L273`).
   Проверено.
7. **Session id всё равно генерируется** и уходит в заголовке `Mcp-Session-Id` ответа
   (проверено: `Mcp-Session-Id: LEDWNOWSDZXEFPYDLUAVTBO4TY`) — как «логический» id, доступный
   через `ServerSession.ID()` ([`server.go#L1092`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L1092)),
   но он ничего не адресует.

Зачем это: распределённые серверы, где запросы одной «сессии» садятся на разные процессы
([`examples/server/distributed`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/examples/server/distributed/main.go)).

> Предупреждение из документации SDK: «Stateless mode is not directly discussed in the spec,
> and is still being defined»
> ([docs/protocol.md](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/docs/protocol.md#stateless-mode)).
> Это расширение SDK, а не часть спеки.

`JSONResponse: true` меняет `Content-Type` ответа с `text/event-stream` на `application/json`
для одиночных сообщений (`streamable.go#L140-L144`). По умолчанию даже в stateless ответ приходит
как SSE (проверено).

---

## 6. Чистый markdown без `structuredContent`

### Как вернуть только текст

Задать `Out = any` и **вернуть `nil` в качестве `out`**:

```go
func (s *Source) readQuery(ctx context.Context, req *mcp.CallToolRequest, in ReadQueryInput) (*mcp.CallToolResult, any, error) {
	md, err := s.query(ctx, in.SQL, in.MaxRows)
	if err != nil {
		return nil, nil, fmt.Errorf("запрос не выполнен: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: md}},
	}, nil, nil
}
```

Результат (проверено): `{"content":[{"type":"text","text":"| col |\n| --- |\n| 1 |"}]}` —
ни `outputSchema` в `tools/list`, ни `structuredContent` в ответе.
Это же делает штатный пример
[`examples/server/hello`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/examples/server/hello/main.go).

### Как связаны `OutputSchema` и `structuredContent`

Ключевое условие в
[`toolForErr`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L300-L306):

```go
if t.OutputSchema != nil || reflect.TypeFor[Out]() != reflect.TypeFor[any]() {
	elemZero, err = setSchema[Out](&tt.OutputSchema, &outputResolved)
	...
}
```

и в теле хендлера ([`server.go#L349-L397`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L349-L397)):

```go
var outval any = out
...
if outval != nil {
	outbytes, _ := json.Marshal(outval)
	outJSON, err = applySchema(outJSON, outputResolved)  // валидация против OutputSchema
	res.StructuredContent = outJSON
	if res.Content == nil {                              // ← только если Content не задан
		res.Content = []Content{&TextContent{Text: string(outJSON)}}
	}
}
```

Отсюда — четыре наблюдаемых режима (все проверены прогоном):

| `Out` | что вернул хендлер | `outputSchema` в `tools/list` | ответ на `tools/call` |
|---|---|---|---|
| `any` | `out = nil`, `Content` задан | нет | только ваш `content` |
| `any` | `out = map[...]{...}` (не nil!) | нет | `structuredContent` **есть** + JSON-текст в `content` |
| `Out` типизирован | `out = Out{...}`, `Content` не задан | есть | `structuredContent` + сериализованный JSON в `content` |
| `Out` типизирован | `out = Out{...}`, `Content` задан руками | есть | `structuredContent` + **ваш** `content` |

**Ответ на вопрос: нет, типизированный результат не заставляет отдавать JSON модели.** Если задать
`CallToolResult.Content` явно, SDK его не перезатрёт — JSON останется только в `structuredContent`.
Проверено:

```json
{"result":{"content":[{"type":"text","text":"# markdown"}],"structuredContent":{"rows":3}}}
```

Две ловушки:

- `Out = any` **не гарантирует** отсутствие `structuredContent`: если вернуть не-nil `out`, он всё
  равно попадёт в `structuredContent`, причём без выходной схемы (проверено:
  `{"content":[{"type":"text","text":"{\"leaked\":true}"}],"structuredContent":{"leaked":true}}`).
  Правило: `any` + всегда `nil`.
- Если `OutputSchema` задан, спека требует, чтобы `structuredContent` ему соответствовал
  («Servers **MUST** provide structured results that conform to this schema»,
  [spec/server/tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)),
  и SDK это валидирует (`applySchema`) — провал валидации даёт **протокольную** ошибку
  (`fmt.Errorf("validating tool output: %w", err)`), а не `IsError`.

Замечание о дублировании: спека говорит «a tool that returns structured content SHOULD also return
the serialized JSON in a TextContent block». SDK это по умолчанию делает — то есть при типизированном
`Out` без своего `Content` модель получает **два** представления одного и того же. Для «бюджета
вывода» это удвоение объёма.

---

## 7. Динамический набор инструментов при старте

Регистрация инструментов — обычный императивный код: `mcp.AddTool` вызывается сколько нужно раз,
до `Run`/`Connect`. Никакого декларативного реестра нет.

```go
type Config struct {
	DSN         string
	AllowWrites bool
	Err         error // невалидный/отсутствующий конфиг
}

func newServer(cfg Config) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "pg-mcp", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: "Инструменты доступа к Postgres. Начинайте с `pg_read_query`.",
	})

	// Деградированный старт: рабочих инструментов нет, есть только диагностический.
	if cfg.Err != nil {
		msg := cfg.Err.Error()
		mcp.AddTool(s, &mcp.Tool{
			Name:        "pg_diagnostics",
			Description: "Почему сервер стартовал в деградированном режиме.",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint: true, IdempotentHint: true,
				DestructiveHint: ptr(false), OpenWorldHint: ptr(false),
			},
		}, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "## Деградированный старт\n\n" + msg}},
			}, nil, nil
		})
		return s
	}

	src := &Source{dsn: cfg.DSN}
	mcp.AddTool(s, &mcp.Tool{Name: "pg_read_query", ...}, src.readQuery)
	if cfg.AllowWrites {
		mcp.AddTool(s, &mcp.Tool{Name: "pg_write_execute", ...}, src.writeExecute)
	}
	return s
}
```

Диагностический инструмент без аргументов удобно объявлять с `In = any` — это даёт валидную пустую
схему `{"type": "object"}` (проверено на wire).

Что важно знать:

- **Capabilities считаются в момент `initialize`, а не при `NewServer`.**
  [`capabilities()`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L501-L522)
  добавляет `"tools": {"listChanged": true}` только если `s.tools.len() > 0`. Поэтому режим
  «вообще ноль инструментов» стоит избегать: сервер тогда не объявит capability `tools`, и клиент
  может даже не звать `tools/list`. Оставленный диагностический инструмент решает это автоматически.
  Альтернатива — `ServerOptions.Capabilities.Tools = &mcp.ToolCapabilities{ListChanged: true}`
  ([`server.go#L85-L113`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L85-L113)).
- **Менять набор можно и после старта**: `AddTool`/`RemoveTools` вызывают
  [`changeAndNotify`](https://github.com/modelcontextprotocol/go-sdk/blob/v1.2.0/mcp/server.go#L574-L585),
  которая рассылает `notifications/tools/list_changed` всем сессиям с небольшой задержкой
  (батчинг через `time.AfterFunc`). Но в **stateless** HTTP-режиме сессия живёт один запрос, так что
  уведомление практически никуда не дойдёт — на HTTP набор инструментов надо считать фиксированным
  на время процесса.
- `AddTool` при ошибке вывода схемы **паникует** (`server.go#L447-L453`). Схема выводится из типа,
  а не из конфига, так что это ошибка программиста, а не рантайм-условие — паника на старте тут
  приемлема.

---

## Что это значит для проекта

Складывая §1–§7 в скелет одного сервера-источника (термины — по [CONTEXT.md](../../CONTEXT.md)):

**Ленивая инициализация.** SDK ей не мешает: соединение живёт в структуре-владельце инструментов,
методы которой и есть хендлеры (`src.readQuery`), а `sync.Once` внутри `connect` делает первый вызов
инструмента точкой подключения. Ни `NewServer`, ни `AddTool`, ни `Run` источник не трогают —
процесс всегда стартует успешно. Недоступность источника проявляется как результат вызова
инструмента с `IsError: true`, что ровно совпадает с §4.

**Деградированный старт.** `newServer(cfg)` — чистая функция от конфига к серверу. При `cfg.Err != nil`
регистрируется единственный `<src>_diagnostics` c `In = any`. Важная деталь из §7: полностью пустой
сервер объявит себя без capability `tools`, поэтому диагностический инструмент — не только удобство
для модели, но и техническая необходимость.

**Markdown-вывод.** Наш формат ответа — markdown-таблица с пометкой об усечении по бюджету вывода,
а не JSON. Значит все инструменты объявляются с `Out = any` и **всегда** возвращают `nil` вторым
значением (§6). `OutputSchema` не задаём: она не нужна, а её наличие удвоило бы объём ответа
(structuredContent + сериализованный JSON в content). Усечение по бюджету — это успешный результат
с явной пометкой в тексте, не `IsError`.

**Два транспорта.** Один `*mcp.Server`, флаг `-http`: пусто → `srv.Run(ctx, &mcp.StdioTransport{})`,
задан → `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true})`.
`Stateless: true` согласуется с термином «Stateless» из CONTEXT.md: пул соединений внутри процесса
он не ломает (пул — ресурс, а не состояние диалога), но накладывает жёсткие рамки — ни sampling,
ни elicitation, ни server→client запросов, ни GET/SSE, а `tools/list_changed` фактически бесполезен.
Для нашего набора read/write-инструментов ничего из этого не нужно, так что цена нулевая.

**Аннотации и permissions.** Read-инструменты помечаем `ReadOnlyHint: true, IdempotentHint: true,
DestructiveHint: ptr(false), OpenWorldHint: ptr(false)`; write-инструменты — `DestructiveHint: ptr(true)`.
Помнить про §2: `DestructiveHint`/`OpenWorldHint` по умолчанию `true`, их надо гасить явно через
указатель. Но авто-одобрение read-инструментов в permissions **строить на аннотациях нельзя** —
поведение Claude Code тут не подтверждено первичным источником; опираемся на явные правила и на
префиксы имён (`pg_read_*` / `pg_write_*`).

**Логи.** На stdio `stdout` занят протоколом — весь `slog` только в `stderr`. Плюс ошибка всегда
дублируется в текст ответа инструмента, иначе модель её не увидит (§4).

**Имена.** `[a-zA-Z0-9_.-]`, ≤128 символов. Наша схема `<src>_read_<verb>` / `<src>_write_<verb>`
подходит; но помните, что невалидное имя SDK не отвергает, а только логирует (§1).

---

## Сводка по неподтверждённому

1. **Поведение Claude Code относительно `ToolAnnotations`** (авто-одобрение, распараллеливание,
   синтаксис `mcp__*[readOnly]`) — в официальной документации Anthropic не найдено, только issue.
2. **Лимит на размер `instructions`** — ни в спеке 2025-06-18, ни в SDK нет. Клиентские лимиты
   не документированы.
3. **Стабильность stateless-режима** — сам SDK помечает его как не описанный в спеке и
   дорабатываемый (см. modelcontextprotocol/modelcontextprotocol#1364, #1372, #1442).
4. **v1.2.0 vs `main`** — на момент исследования актуальны релизы вплоть до v1.7.0
   ([список версий](https://proxy.golang.org/github.com/modelcontextprotocol/go-sdk/@v/list)).
   Вся эта заметка — про v1.2.0; в `main` API уже разошёлся (например, `ServerOptions.SchemaCache`).
   Стоит отдельно решить, зачем пин именно на v1.2.
