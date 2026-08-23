# Мутационное тестирование Go: что живо, что умеет build-теги и что оно показывает на этом наборе

Исследование по issue [Conte777/infra-mcp#60](https://github.com/Conte777/infra-mcp/issues/60) (карта [#59](https://github.com/Conte777/infra-mcp/issues/59)).

**Дата проверки:** 2026-08-23.

> **Прогон шёл на `bdd3f1d`.** Уже после его старта [PR #70](https://github.com/Conte777/infra-mcp/pull/70) переписал `guard.go` на глобы: функции `calls()`, разобранной в §5.3, больше нет — её сменили `calledNames` и `applied`, обе покрыты на 100%, так что три найденные там границы отпали вместе с ней. Переизмерено на `21a4a23` и устояло: `copiesToProgram` — 42.9%, `scannable` — 66.7%. Номера строк `guard.go` из §5.3 требуют перепрогона, прежде чем на них ссылаться. Остальных разобранных здесь файлов #70 не касался.

**Первоисточники:**

- Репозитории и релизы инструментов на GitHub (даты коммитов и тегов брались через GitHub API, не по памяти):
  [zimmski/go-mutesting](https://github.com/zimmski/go-mutesting),
  [avito-tech/go-mutesting](https://github.com/avito-tech/go-mutesting),
  [go-gremlins/gremlins](https://github.com/go-gremlins/gremlins),
  [gtramontina/ooze](https://github.com/gtramontina/ooze),
  [sivchari/gomu](https://github.com/sivchari/gomu),
  [quality-gates/mutago](https://github.com/quality-gates/mutago),
  [codeintegrity-ai/mutahunter](https://github.com/codeintegrity-ai/mutahunter).
- Документация gremlins: [gremlins.dev](https://gremlins.dev/latest/) — раздел [`unleash`](https://gremlins.dev/latest/usage/commands/unleash/).
- Исходники `gremlins v0.6.0` из модульного кеша: `internal/engine/executor.go`, `internal/coverage/coverage.go`, `internal/mutator/mutator.go`, `cmd/unleash.go`.
- Предложения в Go: [golang/go#80892 «proposal: cmd/go: add opt-in mutation testing to go test»](https://github.com/golang/go/issues/80892) (создан 2026-08-15, **закрыт 2026-08-16 как not planned**), [golang/go#75315 «proposal: cmd/asm: support for mutation testing»](https://github.com/golang/go/issues/75315) (открыт 2025-09-08, не приземлился; в [release notes Go 1.26](https://go.dev/doc/go1.26) его нет).
- Собственные прогоны на этом репозитории (отдельный worktree, ветка `fix/research-mutation-testing-findings`): все числа ниже измерены, а не оценены. Машина: darwin/arm64, `go version go1.27.0`, Docker Desktop 29.7.2, 7.9 GB памяти на докер.

---

## 0. Вердикт

**Прогонять — да, разово, инструментом `gremlins`.** Он жив, умеет `--tags`, отбирает мутантов по покрытию и на этом коде почти не даёт эквивалентных мутантов (3 строго эквивалентных на 38 выживших). Полный прогон модуля с `-tags=integration` — это порядок **45–60 минут** на ноутбуке, не часы: 480 мутантов, из них 381 покрыт. Прогнано 274 из них; выжило 38.

Сигнал предельно однородный: **почти каждый выживший мутант — граница усечения**. `>` против `>=` там, где данных ровно столько, сколько разрешено бюджетом или лимитом. Это ровно тот класс, который `strings.Contains` не ловит по устройству.

**Не делать гейтом в CI.** Три причины, все проверены прогоном, не предположены:

1. Таймаут мутанта gremlins выводит из времени базового прогона покрытия, а Go может отдать этот прогон **из тест-кеша**. Тогда таймаут выходит ~2 секунды и весь прогон превращается в стену `TIMED OUT`. Это воспроизвелось дважды и лечится только `go clean -testcache` перед прогоном плюс явным `--timeout-coefficient`.
2. По умолчанию gremlins гоняет на мутанта **только тесты пакета, где мутант живёт**. В этом репозитории `mcpsrv.Load` вызывается из тестов пакета `postgres`, поэтому мутанты в `internal/mcpsrv/config.go` показываются выжившими, хотя модульный прогон их убивает (доказано `go test -overlay`, §6.2). Режим `-i`, который это чинит, гоняет весь набор на каждого мутанта — это 381 × 47 с ≈ 5 часов.
3. Интеграционный набор поднимает по контейнеру на тест. Под тремя-четырьмя воркерами докер начинает отваливаться по таймауту: у меня базовый прогон покрытия упал на `TestDescribeCannotBeMadeToForgeADDLLine` (`context deadline exceeded` при старте контейнера), и весь прогон gremlins оборвался с `ERROR: failed to gather coverage`.

3-я причина оговорка: флап случился, когда параллельно шёл ещё один прогон с докером. Чистый прогон на трёх воркерах отработал 121 мутанта без единого таймаута.

---

## 1. Какие инструменты живы

| Инструмент | Последний коммит / релиз | Go в `go.mod` | Build-теги | Отбор тестов |
|---|---|---|---|---|
| **go-gremlins/gremlins** | коммит 2026-03-30, релиз **v0.6.0 (2025-12-06)** | 1.25.0 | **`--tags` / `-t`, нативно** | только покрытые мутанты; тесты пакета мутанта, либо `-i` — весь набор |
| gtramontina/ooze | коммит **2026-08-19**; тегов нет с v0.3.1 (2023-05-04) | 1.25.0 (toolchain 1.26.6) | через `WithTestCommand`, **но файлы за кастомными тегами не мутируются** | нет |
| quality-gates/mutago | создан 2026-05-29, **v2.8.2 (2026-08-20)** | 1.26+ | `--test-flags='-tags=…'` | `--coverage`, `--per-test` |
| avito-tech/go-mutesting | **v2.3.1 (2025-12-26)** | 1.25.5 | только через свой `--exec`-скрипт | нет |
| sivchari/gomu | коммит 2026-07-10, v0.2.1 (2026-05-29) | 1.24 | нет | нет |
| **zimmski/go-mutesting** | **мёртв: последний коммит master 2021-06-10, релиз v1.2 (2021-06-10)** | 1.10 | нет | нет |

Подробности, важные для решения:

- **`go-mutesting` мёртв.** Не архивирован, но master не двигался с июня 2021, `go.mod` объявляет `go 1.10`, а issue [#100 «Is go-mutesting dead? Proposal for fork»](https://github.com/zimmski/go-mutesting/issues/100) открыт с 2022-07-05 и не закрыт. Build-тегов он не умел никогда: issue [#87 «Enable Providing Build Tags For Tests»](https://github.com/zimmski/go-mutesting/issues/87) открыт с 2021-07-29.
- **Живой потомок этой линии — форк `avito-tech/go-mutesting`** (v2.3.1, декабрь 2025). Он добавил go modules, HTML-отчёт и nolint-стиль аннотаций `// mutator-disable-func`, `// mutator-disable-next-line`, `// mutator-disable-regexp`, но флага для build-тегов так и не завёл: в конфиге есть `skip_with_build_tags`, и это **пропуск** файлов с констрейнтами, а не проброс `-tags`. Единственный путь — свой `--exec`-скрипт.
- **`ooze` — не CLI, а библиотека:** в проект кладётся `mutation_test.go` за `//go:build mutation`, внутри `ooze.Release(t, …)`. Тестовая команда задаётся целиком (`ooze.WithTestCommand("go test -count=1 -tags=integration ./...")`), так что `-tags` туда пробросить можно. Ловушка в другом и задокументирована в README: **обнаружение исходников идёт по default build context**, файлы, отсечённые `//go:build`, не мутируются. Для нас это не проблема (за тегом только тесты), но `ooze` гоняет весь набор на каждого мутанта и не имеет отбора по покрытию — то есть заведомо 5-часовой режим. Плюс на `@latest` придёт код 2023 года: релизов после v0.3.1 нет.
- **Встроенных средств в Go нет.** Предложение добавить мутационное тестирование в `go test` ([#80892](https://github.com/golang/go/issues/80892)) закрыто как not planned на следующий день после создания. Ассемблерное предложение [#75315](https://github.com/golang/go/issues/75315) — про `cmd/asm` для криптографии stdlib, не general-purpose, и в Go 1.26 не вошло. В `gotestsum` слова «mutation» нет вовсе.
- **`mutago`** — самый функционально богатый новичок (есть `--per-test`, `--dry-run`, `--timeout-coefficient`, baseline, git-diff-режим), но репозиторию три месяца и у него единицы звёзд. Дословного примера `--test-flags='-tags=…'` в документации нет, только общий механизм «extra flags passed to each `go test` invocation». Для разовой диагностики брать незрелый инструмент смысла нет.

---

## 2. Build-теги: gremlins умеет, и это проверено

Флаг задокументирован (`gremlins unleash --tags "tag1,tag2"`) и виден в исходнике сборщика команды `internal/engine/executor.go`:

```go
func (m *mutantExecutor) getTestArgs(pkg string) []string {
	args := []string{"test"}
	if m.buildTags != "" {
		args = append(args, "-tags", m.buildTags)
	}
	args = append(args, "-timeout", (2*time.Second + m.testExecutionTime).String())
	args = append(args, "-failfast")
	...
	path := pkg
	if m.integrationMode {
		path = "./..."
	}
	args = append(args, path)
	return args
}
```

Фактическая проверка на этом репозитории — `--dry-run` без тега и с тегом:

| прогон | Runnable | Not covered | Mutator coverage |
|---|---|---|---|
| `gremlins unleash --dry-run .` | 245 | 235 | 51.04% |
| `gremlins unleash --dry-run -t integration .` | **381** | 99 | **79.38%** |

Всего мутантов в модуле — **480**. Без тега три четверти пакета `postgres` числятся непокрытыми; с тегом (сбор покрытия занял 47 секунд, то есть докер реально поднимался) покрытыми становятся 381. Тег доходит и до сбора покрытия, и до прогона мутанта.

Из этой же выдержки виден и главный подвох, разобранный в §6: `path = pkg` по умолчанию и `./...` только под `-i`.

### Распределение мутантов (`--dry-run -t integration`)

| пакет | Runnable |
|---|---|
| `internal/source/postgres` | 237 |
| `internal/mcpsrv` (с `block` и `srvtest`) | 82 |
| `internal/mcpsrv/block` | 59 |
| `internal/pgtest` | 2 |
| `internal/buildinfo` | 1 |

Внутри `postgres`: `describe.go` — 79, `query.go` — 42, `guard.go` — 32, `pool.go` — 31, `config.go` — 17, `lease.go` — 11, `errors.go` — 8, `catalog.go` — 8, `conn.go` — 6, `source.go` — 3.

---

## 3. Стоимость прогона

Базовые времена на этой машине (`-count=1`, без `-race`):

| прогон | время |
|---|---|
| `go test ./internal/mcpsrv/block` | 1.08 с |
| `go test -tags=integration ./internal/mcpsrv` | 1.41 с |
| `go test -tags=integration ./internal/pgtest` | 2.13 с |
| `go test -tags=integration ./internal/source/postgres` | **44.3 с** |
| сбор покрытия по всему модулю с тегом (шаг gremlins) | 45–47 с |

Измеренные прогоны gremlins (`--workers 4`, если не сказано иное):

| прогон | мутантов | время | итог |
|---|---|---|---|
| `./internal/mcpsrv/block` | 68 (59 runnable) | **32.6 с** | 48 killed, 11 lived, 9 not covered, 0 timed out |
| `./internal/mcpsrv` (включая `block`, `srvtest`) | 207 | **1 мин 4 с** | 124 killed, 17 lived, 66 not covered, 0 timed out |
| `./internal/source/postgres`, только `guard.go` | 45 (32 прогнано) | **4 мин 13 с** + 45 с покрытия | 23 killed, 7 lived, 13 not covered, 2 timed out |
| `./internal/source/postgres`, `describe.go` + `query.go`, `--workers 3` | 129 (121 прогнано) | **23 мин 19 с** + 46 с покрытия | 107 killed, 14 lived, 8 not covered, **0 timed out** |

Две ключевые цифры:

- `guard.go` — 32 прогнанных мутанта за 253 секунды на четырёх воркерах, **~7.9 с настенного времени на мутанта** при полном наборе пакета в 44 секунды. Так дёшево получается потому, что `-failfast` обрывает прогон на первом упавшем тесте, а `guard_test.go` — юнит-тест, до контейнеров дело не доходит.
- `describe.go` + `query.go` — 121 мутант за 1399 секунд на трёх воркерах, **~11.6 с на мутанта**. Здесь убивающий тест интеграционный, то есть контейнер поднимается почти всегда, и всё равно это не 44 секунды на мутанта: `-failfast` и порядок тестов работают в плюс.

**Порядок величины для полного прогона модуля** (`-t integration`, без `-i`): 381 покрытый мутант. Измерено 274 из них за ~29 минут; оставшиеся 107 — это `pool.go`, `config.go`, `lease.go`, `errors.go`, `catalog.go`, `conn.go`, `source.go` в `postgres` (84) и мелочь в `pgtest`/`buildinfo`. Итого **45–60 минут** на трёх-четырёх воркерах. Не часы — но и не то, что вешают на каждый PR.

**Режим `-i` (весь набор на каждого мутанта) — нереален:** 381 × 47 с ≈ **5 часов** даже без учёта того, что четыре параллельных набора контейнеров докер тут не выдерживает.

---

## 4. Эквивалентные мутанты: их мало

Разобрано вручную 18 выживших мутантов (11 в `block`, 7 в `guard.go`). **Строго эквивалентных — 2**, то есть около 11%. Остальные указывают на настоящую непроверенную границу.

Провабельно эквивалентные:

- `guard.go:78:42` — `if i := strings.IndexByte(s, '\n'); i >= 0` → `i > 0`. Ветка достижима только внутри `case strings.HasPrefix(s, "--")`, то есть `s[0] == '-'`, поэтому `i == 0` невозможно.
- `guard.go:168:16` — `for i := 0; i < len(sql);` → `i <= len(sql)`. На лишней итерации `strings.Index(sql[len:], name)` вернёт −1 для непустого `name` (пустое отсечено проверкой на строке 165), и функция вернёт тот же `false`.
- Пограничный случай: `cluster.go:85:38` — `make(map[string]any, len(dst)+len(src))` → `-`. Меняется только предвыделенная вместимость, поведение то же. Формально эквивалентный; такие мутанты у gremlins отключаются целиком классом `--arithmetic-base=false`, но тогда потеряются и осмысленные арифметические мутанты в `block`.

Средства борьбы у инструментов:

- **gremlins**: `--exclude-files` (regexp по пути) и пофлаговое включение/выключение каждого из 11 мутаторов (`ARITHMETIC_BASE`, `CONDITIONALS_BOUNDARY`, `CONDITIONALS_NEGATION`, `INCREMENT_DECREMENT`, `INVERT_*`, `REMOVE_SELF_ASSIGNMENTS`) плюс `.gremlins.yaml`. **Построчных комментарных директив нет** — issue [#289](https://github.com/go-gremlins/gremlins/issues/289) открыт с 2026-06-25. Это и есть главный аргумент против гейта в CI: подавить один заведомо эквивалентный мутант нечем, кроме исключения целого файла.
- **avito-tech/go-mutesting** и **mutago**: есть построчные аннотации `// mutator-disable-next-line`, `// mutator-disable-func`, `// mutator-disable-regexp`.
- **ooze**: только `IgnoreSourceFiles(regex)` и ограничение набора «вирусов».

Особый источник шума в gremlins на этом коде — **условия `case` в `switch` без выражения**. Go не создаёт для них отдельного блока в профиле покрытия, позиция условия не попадает ни в один блок, и gremlins помечает такого мутанта `NOT COVERED`, даже если ветка исполняется. Пример: `block/markdown.go:82:20` и `82:35` (условие `case len(notices) > 1 || dropped > 0`) помечены как непокрытые, при том что тело на строке 83 в профиле имеет счётчик 1. Из 9 «непокрытых» мутантов в `block` таких — 5.

---

## 5. Выжившие мутанты

Прогон: `gremlins v0.6.0` (`go install github.com/go-gremlins/gremlins/cmd/gremlins@latest` в `./bin`), `--workers 4`, `--timeout-coefficient 300` для быстрых пакетов.

### 5.1 `internal/mcpsrv/block` (98.0% покрытия) — 11 выживших из 59 прогнанных

Test efficacy 81.36%, mutator coverage 86.76%. Ни одного эквивалентного: все одиннадцать — непроверенная граница или непроверенная побайтовая арифметика бюджета.

| позиция | мутатор | что меняется | что это значит |
|---|---|---|---|
| `markdown.go:47:10` | CONDITIONALS_BOUNDARY | `if rem <= 0` → `rem < 0` | нет случая, где бюджет исчерпан ровно в ноль |
| `markdown.go:48:26` | ARITHMETIC_BASE | `dropped = len(blocks) - i` → `+` | число выброшенных блоков в пометке никем не сверяется |
| `markdown.go:48:26` | INVERT_NEGATIVES | там же | то же |
| `markdown.go:51:17` | CONDITIONALS_BOUNDARY | `if len(parts) > 0` → `>= 0` | учёт разделителя `\n` в бюджете не проверен |
| `markdown.go:51:17` | CONDITIONALS_NEGATION | там же → `<= 0` | то же |
| `markdown.go:122:34` | CONDITIONALS_BOUNDARY | `len(rows) > bud.MaxRows` → `>=` | **нет теста, где строк ровно `MaxRows`**: сейчас пройдёт и версия, которая печатает пометку об усечении, ничего не усекая |
| `markdown.go:156:42` | ARITHMETIC_BASE | `fitWithNotice(len(head)+len(tail), …)` → `-` | обрамление code-fence не учитывается точно; нет теста, где code-блок упирается в лимит |
| `markdown.go:187:18` | CONDITIONALS_BOUNDARY | `if used+len(l) > limit` → `>=` | граница «строка влезает ровно» не проверена |
| `markdown.go:200:55` | ARITHMETIC_BASE | `len(rowsNotice(…))+len(moreTruncated)` → `-` | резерв под пометку |
| `markdown.go:201:56` | ARITHMETIC_BASE | то же на соседней строке | резерв под пометку |
| `markdown.go:207:11` | ARITHMETIC_BASE | `return n + 2` → `n - 2` | резерв под пометку |

Три последних — самая содержательная находка прогона. Обещание «вывод не превышает `Budget.MaxBytes`» **сторожится**: `TestOutputNeverExceedsBudget` (`markdown_test.go:369`) перебирает бюджеты 400…4000 с шагом 37 при двух значениях `MaxCellChars` и проверяет `len(out) <= budget`. Но мутанты, уменьшающие резерв под пометку на 4 и даже на ~150 байт, этот перебор **проходят** — значит, ни один его шаг не попадает во вход, где резерв действительно натянут (пометка максимальной длины при бюджете ровно на грани). Тест правильной формы, но подобранный не по границе, а по сетке.

Остальные восемь — про непроверенные границы: `TestDroppedBlocksAreAnnounced` (`markdown_test.go:349`) проверяет через `strings.Contains` только сам факт пометки и не сверяет число выброшенных блоков (мутанты `48:26`); ни один тест не задаёт вход, где строк ровно `MaxRows` (мутант `122:34`) или где строка влезает в остаток бюджета ровно (мутант `187:18`).

### 5.2 `internal/mcpsrv` — 6 выживших (без учёта `block`)

| позиция | мутатор | что меняется | оценка |
|---|---|---|---|
| `cluster.go:85:38` | ARITHMETIC_BASE | `make(map, len(dst)+len(src))` → `-` | эквивалентный (вместимость) |
| `config.go:252:9` | CONDITIONALS_NEGATION | `if err != nil` после `os.ReadFile` → `== nil` | **ложный на уровне модуля**: убивается тестами пакета `postgres`, см. §6.2 |
| `config.go:333:22` | CONDITIONALS_NEGATION | `return ok && len(m) == 0` → `!= 0` | **ложный на уровне модуля** (проверено overlay: падают те же тесты `postgres`) |
| `config.go:387:63` | CONDITIONALS_NEGATION | `if err := os.WriteFile(…); err != nil` → `== nil` | **эквивалентный**: ветка возвращает `return path, err`, а `err` там nil |
| `main.go:211:40` | CONDITIONALS_NEGATION | `errors.As(err, &wire) && wire.Code == codeServerClosing` → `!=` | **настоящая дыра, проверено overlay**: мутант проходит `go test ./...` целиком. `serve_test.go:72` проверяет только, что обычная ошибка — не shutdown; `*jsonrpc.Error` с кодом, отличным от `-32004`, не строит никто |
| `status.go:42:36` | CONDITIONALS_BOUNDARY | `if len(r.rt.Inventory.Clusters) > 0` → `>= 0` | статус при пустом списке кластеров не проверен |

Три из шести проверены напрямую через `go test -overlay` (§6.2): два оказались межпакетными артефактами, `main.go:211` — настоящей дырой.

### 5.3 `internal/source/postgres`

**`guard.go`** — 7 выживших из 32 прогнанных (test efficacy 76.67%):

| позиция | мутатор | что меняется | оценка |
|---|---|---|---|
| `guard.go:78:42` | CONDITIONALS_BOUNDARY | `i >= 0` → `i > 0` | эквивалентный |
| `guard.go:89:11` | CONDITIONALS_BOUNDARY | `if end < 0` → `<= 0` | нет теста, где оператор начинается с не-идентификаторного символа |
| `guard.go:103:21` | CONDITIONALS_BOUNDARY | `for depth > 0 && i < len(s)` → `<=` | **нет теста с незакрытым `/*`**: мутант возвращает `len(s)+1`, и вызов `s[skipBlockComment(s):]` на строке 84 паникует |
| `guard.go:168:16` | CONDITIONALS_BOUNDARY | `for i := 0; i < len(sql);` → `<=` | эквивалентный |
| `guard.go:170:8` | CONDITIONALS_BOUNDARY | `if j < 0` → `<= 0` | **запрещённое имя в самом начале сканируемой строки не проверено**: мутант заставляет `calls` вернуть `false`, то есть deny-list пропускает вызов |
| `guard.go:175:9` | CONDITIONALS_BOUNDARY | `if at > 0 && isIdentRune(…)` → `>= 0` | то же: при `at == 0` мутант обращается к `sql[-1]` и должен паниковать — до него не доходят |
| `guard.go:175:9` | CONDITIONALS_NEGATION | там же → `<= 0` | **обещание из комментария на строках 160–163** — «слово, входящее в другой идентификатор, не считается вызовом» — **не проверено ни одним тестом** |

Это прямой вход для [#65 «Deny-list: ветки второго замка над COPY»](https://github.com/Conte777/infra-mcp/issues/65): настоящий замок `calls()` покрыт по строкам, но три его границы не сторожатся.

**`describe.go` + `query.go`** — 14 выживших из 121 прогнанного (test efficacy 88.43%, mutator coverage 93.80%, 0 таймаутов, 23 мин 19 с на трёх воркерах). Это самая ценная часть выдачи: здесь живут ровно те утверждения, о которых [#61](https://github.com/Conte777/infra-mcp/issues/61).

| позиция | мутатор | что меняется | что это значит |
|---|---|---|---|
| `describe.go:170:49` | CONDITIONALS_NEGATION | `class(pgErr.Code) == "0A"` → `!=` | класс ошибки `0A` (кросс-БД ссылка) не проверяется ни одним тестом — сравнение с `42` убито, с `0A` нет |
| `describe.go:250:10` | CONDITIONALS_NEGATION | `if size == ""` → `!=` | ветка «нет размера, но есть партиции» в `sizeLine` не различается тестами |
| `describe.go:359:8` | CONDITIONALS_BOUNDARY | `if i < len(body)-1` → `<=` | **запятая между строками DDL**: тело печатается с лишней запятой на последней строке, и ни один тест этого не видит |
| `describe.go:359:8` | CONDITIONALS_NEGATION | там же → `>=` | то же |
| `describe.go:359:19` | INVERT_NEGATIVES | `len(body)-1` → `len(body)+1` | то же |
| `describe.go:359:19` | ARITHMETIC_BASE | `len(body)-1` → `+1` | то же |
| `describe.go:518:43` | CONDITIONALS_BOUNDARY | `if rest := rel.partitions - listed; rest > 0` → `>= 0` | нет случая, где перечислены **все** партиции: мутант печатает «and 0 partitions not listed», тест проходит |
| `query.go:30:11` | CONDITIONALS_BOUNDARY | `if limit > 0 && cursorable(sql)` → `>= 0` | нет теста с `limit == 0`: выбор между курсором и прямым прогоном на «без лимита» не проверен |
| `query.go:30:11` | CONDITIONALS_NEGATION | там же → `<= 0` | то же |
| `query.go:52:12` | CONDITIONALS_BOUNDARY | `if limit > 0 && len(t.Rows) > limit` → `>= 0` | то же, в цикле чтения |
| `query.go:52:12` | CONDITIONALS_NEGATION | там же → `<= 0` | то же |
| `query.go:59:11` | CONDITIONALS_BOUNDARY | `if limit > 0 && …` → `>= 0` | то же, при выставлении `More` |
| `query.go:59:30` | CONDITIONALS_BOUNDARY | `len(t.Rows) > limit` → `>=` | **нет теста, где строк ровно `limit`**: мутант выставляет `More = true` на полном, ничем не урезанном результате |
| `query.go:174:32` | CONDITIONALS_BOUNDARY | `if limit <= 0 \|\| len(t.Rows) <= limit` → `<` | та же граница во втором пути чтения |

Из четырнадцати эквивалентных нет ни одного. Двенадцать из четырнадцати — одна и та же болезнь: **граница усечения (`>` против `>=`) и вход, где данных ровно столько, сколько разрешено**. Именно этот класс `strings.Contains` не видит по устройству: фрагмент, который тест ищет, остаётся на месте и когда вывод обрезан на строку раньше, и когда к нему приписана лишняя пометка.

`TestDescribeTable` (`tools_integration_test.go:161`) — наглядный образец: девять `strings.Contains` по фрагментам DDL, ни один из которых не включает запятую-разделитель. Отсюда и четыре выживших мутанта на `describe.go:359`. Всего `strings.Contains` в интеграционном наборе: **31** в `tools_integration_test.go` и 3 в `address_integration_test.go`.

### 5.4 Итог по прогонам

| прогон | прогнано мутантов | выжило | из них эквивалентных |
|---|---|---|---|
| `internal/mcpsrv/block` | 59 | 11 | 0 |
| `internal/mcpsrv` (без `block`) | ~62 | 6 | 1 + 2 межпакетных артефакта |
| `postgres/guard.go` | 32 | 7 | 2 |
| `postgres/describe.go` + `query.go` | 121 | 14 | 0 |
| **всего** | **274** | **38** | **3 + 2** |

---

## 6. Подводные камни gremlins, которые надо знать заранее

### 6.1 Таймаут выводится из прогона, который Go отдаёт из кеша

`internal/engine/executor.go` считает таймаут мутанта как `2s + elapsed × coefficient`, где `elapsed` — время шага «Gathering coverage». Go кеширует результаты `go test`, и на повторном прогоне тот же шаг занимает не 45 секунд, а 0.3:

```
первый прогон:  Gathering coverage... done in 45.471613041s   → таймаут ≈ 138 с, 2 TIMED OUT из 32
второй прогон:  Gathering coverage... done in 327.1995ms      → таймаут ≈ 3 с,  88 TIMED OUT из 121 прогнанных
```

На быстром юнит-пакете это бьёт всегда: у `block` `elapsed` ≈ 65 мс, при штатном коэффициенте 3 таймаут выходит 2.2 секунды — меньше, чем компиляция мутанта. Первый прогон по `block` дал **44 `TIMED OUT` из 68**; с `--timeout-coefficient 300` тех же мутантов стало **0 `TIMED OUT`, 11 `LIVED`**. То есть без правки коэффициента инструмент врёт на две трети выдачи.

Рецепт: `go clean -testcache` перед прогоном **и** явный `--timeout-coefficient` (300 для юнит-пакетов, штатный — для пакетов с докером).

### 6.2 По умолчанию мутанта проверяют только тесты его собственного пакета

Это видно в `getTestArgs` (§2): `path = pkg`, и только `-i` подставляет `./...`. В этом репозитории `mcpsrv.Load` вызывается из тестов пакета `postgres`, поэтому его мутанты выглядят выжившими.

Проверено напрямую, без gremlins, через `go test -overlay` (мутация `config.go:252`, `err != nil` → `err == nil`):

```
go test -overlay=… ./internal/mcpsrv/   → ok      (мутант выжил)
go test -overlay=… ./...                → FAIL internal/source/postgres
                                          TestLoadRejects/*, TestLoadNamesTheClusterItRefused,
                                          TestInitWritesAConfigThatLoads
```

Вывод: **цифрам gremlins по `internal/mcpsrv` в штатном режиме верить нельзя.** Либо гонять этот пакет с `-i` (он быстрый, полный набор без тега — секунды), либо читать его выдачу вручную. По `block` и `postgres` таких артефактов нет: их тесты живут в своих пакетах.

### 6.3 Прогон падает целиком, если базовый сбор покрытия оказался красным

Интеграционный набор поднимает по контейнеру на тест. Когда параллельно шёл ещё один `go test` с докером, базовый прогон свалился:

```
--- FAIL: TestDescribeCannotBeMadeToForgeADDLLine (63.29s)
    tools_integration_test.go:381: start postgres: … wait until ready: mapped port:
        check target: retries: 9, port: "invalid port", last err: … context deadline exceeded
FAIL	github.com/Conte777/infra-mcp/internal/source/postgres	106.809s

ERROR: failed to gather coverage: impossible to executeCoverage coverage: exit status 1
```

Сорок минут прогона теряются на флапе одного контейнера. Это ограничивает `--workers` сверху: три-четыре — потолок на 8 GB, отданных докеру.

### 6.4 Прочее

- `gremlins unleash ./...` возвращает `No results to report` — путь должен быть каталогом (`.` или `./internal/...`), а не паттерном пакетов.
- Открытые баги 2026 года, о которых стоит знать: [#272](https://github.com/go-gremlins/gremlins/issues/272) — режим `-i` помечает как `LIVED` мутантов, которые должны быть `KILLED`; [#295](https://github.com/go-gremlins/gremlins/issues/295) — все мутанты становятся `NOT COVERED` из-за комментария в `go.mod`.
- `gremlins --version` печатает `dev`, если ставить через `go install` (версия проставляется goreleaser'ом). Фактическая версия видна в логе загрузки: `go: downloading github.com/go-gremlins/gremlins v0.6.0`.

---

## 7. Что это меняет для остальной карты

### Для [#61 «Граничный вход и эталонные срезы вывода»](https://github.com/Conte777/infra-mcp/issues/61)

Прогон подтверждает предпосылку тикета и **уточняет его границы**.

1. **Эталонный срез сам по себе не убьёт ни одного из одиннадцати выживших мутантов `block`.** Все они — про границу и про побайтовый бюджет: `len(rows) > MaxRows` против `>=`, `used+len(l) > limit` против `>=`, размер резерва под пометку. Эталон фиксирует **один** конкретный вывод; чтобы он поймал `>` → `>=`, нужен эталон ровно на граничном входе (строк ровно `MaxRows`, строка влезает ровно в остаток бюджета). То есть тикет должен требовать не «эталон вместо `strings.Contains`», а **эталон на граничном входе** — иначе он поменяет форму утверждений, не усилив их.
2. **Слабое утверждение — не только `strings.Contains`, но и перебор не по границе.** Обещание «вывод не длиннее `MaxBytes`» уже сторожит `TestOutputNeverExceedsBudget`, и это правильный по форме тест-инвариант, а не `strings.Contains`. Тем не менее три мутанта в `noticeReserve` его переживают: сетка бюджетов 400…4000 шагом 37 не попадает во вход, где резерв натянут. То есть тикет должен переформулировать «слабое утверждение» шире: слабым его делает **подбор входа**, а не только форма проверки.
3. **Тикет получает измеренный список входов, а не «перебрать 794 строки на глаз».** Тридцать восемь выживших мутантов задают приоритет сами:
   - **DDL-запятая** (`describe.go:359`, четыре мутанта) — самый громкий сигнал. `TestDescribeTable` ищет девять фрагментов через `strings.Contains`, и ни один из них не содержит запятую-разделитель. Эталонный срез DDL убивает все четыре сразу — это ровно та работа, ради которой тикет заведён.
   - **Граница лимита строк** (`query.go:52`, `59`, `174`; `describe.go:518`; `block/markdown.go:122`, `187`) — семь мутантов, все об одном: **нет входа, где строк ровно `limit` / ровно `MaxRows` / перечислены все партиции**. Эталон здесь бесполезен, если вход не граничный; нужен именно вход «ровно столько».
   - **`limit == 0`** (`query.go:30`, `52`, `59`) — путь «без лимита» не отличается тестами от пути с лимитом, включая выбор курсора.
   - **Пометки об усечении** (`block/markdown.go:47–51`, `200–207`) — число выброшенных блоков и размер резерва.
4. **Формулировку тикета стоит поменять с «эталонные срезы вместо `strings.Contains`» на «эталонный срез на граничном входе».** Эталон меняет форму утверждения; границу ловит вход. Из тридцати восьми выживших чистой заменой формы убиваются четыре (DDL-запятая), остальным нужен новый вход.
5. **`guard.go` в скоуп #61 не входит и требует своего тикета** — там не рендеринг, а три непроверенные границы deny-list'а (§5.3). Ближайший по смыслу — [#65](https://github.com/Conte777/infra-mcp/issues/65).

### Для «держать ли мутационное тестирование постоянно» (фог карты #59)

Ответ по фактам: **нет.** Не из-за времени — 45–60 минут переносимо ночным прогоном, — а из-за §6.1–6.3: выдача зависит от состояния тест-кеша, штатный режим даёт межпакетные ложные срабатывания, а базовый прогон флапает на докере под нагрузкой. Гейт на таком фундаменте будет краснеть без изменений в коде. Разовая диагностика — да; `.gremlins.yaml` в репозитории и шаг в CI — нет.

---

## 8. Что осталось непроверенным

- `pool.go` (31 мутант), `config.go` (17), `lease.go` (11), `errors.go` (8), `catalog.go` (8), `conn.go` (6), `source.go` (3) в пакете `postgres` не прогонялись — это ещё ~84 мутанта и порядка 15 минут. `pool.go` и `lease.go` — единственные места с конкурентностью, там выдача может отличаться по характеру.
- Команда воспроизведения полного прогона:
  ```
  GOBIN=$PWD/bin go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0
  go clean -testcache
  ./bin/gremlins unleash -t integration --workers 3 .           # пакеты с докером
  ./bin/gremlins unleash --workers 4 --timeout-coefficient 300 ./internal/mcpsrv/block
  ```
- Выдача `internal/mcpsrv` в режиме `-i` не снималась; из шести выживших (§5.2) три проверены вручную через `go test -overlay`, остальные три оценены чтением кода.
- `mutago` и `ooze` на этом репозитории не запускались: первый слишком молод, второй заведомо в 5-часовом режиме.
- Дословного примера `--test-flags='-tags=…'` в документации `mutago` найти не удалось — механизм описан общо.
