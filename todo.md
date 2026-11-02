# Обзор ifxodbc и план верификации

Дата: 2026-11-02. Основа: текущий код ifxodbc и `todo.md`, `stress/README.md`,
`stress/live_test.go`, `stress/operations_test.go` из истории соседнего
`go_ibm_db`, commit `99fc9ef`. В его текущем рабочем дереве этих файлов уже нет.

Обзор изначально был статическим: короткие попытки останавливались на
`SQLDriverConnect`, до стресс-сценариев не доходили. 2026-11-02 после
исправлений выполнены live-прогоны: `make test-ifx` PASS, smoke 10s PASS
(26448 операций), race-стресс 8 воркеров 2 минуты PASS (310047 операций),
live fuzz 1 минута PASS (12081 execs, corpus 2 -> 64); gc/soak не
запускались. Успешные прогоны не закрывают риски закрепления C-буферов
и отмены в полёте - это следующий приоритет (см. раздел рисков).

## Подготовлено

- [x] `ifx_stress_test.go`: 12 сценариев, случайный порядок с seed, 1–64 воркера,
  отдельная закреплённая connection и TEMP-таблица для каждого воркера.
- [x] Фаззер `FuzzIFXSequence`: последовательности до 32 операций, seed corpus,
  автоматическое сохранение минимального воспроизводящего входа стандартным Go fuzz.
- [x] Счётчики env/conn/stmt читаются под mutex. Во время нагрузки сравниваются
  env/stmt, после закрытия всех ресурсов — все три счётчика с исходными значениями.
- [x] Make-цели `stress-ifx`, `fuzz-ifx`; скрипт `scripts/stress-informix.sh`
  с режимами `stress`, `fuzz`, `both`, `soak`, `gc`, логами и сохранением exit status.
- [x] Без `-ifxstress` новые live-тесты и фаззер пропускаются.
- [x] Подготовлены исправления тестового окружения: `UID/PWD` в DSN вместо
  `user/password`, `ODBCINI` с `UNICODE=UCS-2` в образе, получение имени сервера
  из `sqlhosts`, если `INFORMIXSERVER` не экспортирован.
  Диагностический ANSI-клиент подключился; UCS-2 устранил обрезанную диагностику
  и позволил увидеть неподдерживаемые атрибуты DSN. Полный Go-прогон после
  исправления DSN выполнен 2026-11-02 (см. ниже и матрицу).

Настройка Unicode в секции `[ODBC]` описана в
[документации IBM](https://www.ibm.com/docs/en/informix-servers/14.10.0?topic=application-configuration).
Значение UCS-2 здесь выбрано для двухбайтовых SQLWCHAR unixODBC и текущего Go API.

## Подтверждено чтением кода; исправления 2026-11-02

- [x] **Высокий: ошибка `SQLMoreResults` не завершает Exec** (исправлено).
  `stmt.go`, `sumRowCount`: после `SQLMoreResults` добавлена проверка
  `IsError` с возвратом диагностики; цикл завершается только по
  `SQL_NO_DATA`. Регрессия через инъекцию ошибки `SQLMoreResults` всё ещё
  не создана: функции `api` сгенерированы и не подменяются без рефакторинга.
- [ ] **Высокий: контекст не покрывает все блокирующие этапы** (частично
  исправлено 2026-11-02, остаётся открытым). `Conn.QueryContext` проверяет
  контекст до и после подготовки; подготовленные пути проверяют его после
  захвата mutex и перед выполнением. Общий `callWithContext` повторяет
  `SQLCancel` каждые 10 мс до завершения нативного вызова: отмена, попавшая
  до входа в ODBC, больше не теряется. Перед диагностикой, закрытием и
  повторным использованием хендла вызывающий дожидается выхода watcher.
  Покрыты `SQLExecute` и `SQLMoreResults` в `Stmt.ExecContext`, а после
  доработки `rows.go` - `SQLFetch` и `SQLMoreResults` в `Rows.Next` и
  `NextResultSet`: Rows хранит контекст запроса, ошибки чтения после
  отмены возвращают `ctx.Err()`. После следующего этапа (2026-11-02)
  покрыты также `SQLPrepare` (`prepareODBCStmtContext`, включая
  `reprepare` в подготовленных путях), `SQLRowCount` и `SQLNumResultCols`
  (`sumRowCountContext`, `bindColumnsContext`) - все через
  `callWithContext`. Регрессия чтения: `TestIFXCancelLockWait`
  (exclusive table lock + repeatable read блокируют чтение; на 14.10.FC3DE
  строковая блокировка чтения не останавливает, потребовался table lock).
  Остаются без наблюдателя: `SQLGetData` в колоночном цикле и
  `getDataChunked` (требуют смены сигнатуры интерфейса `Column`),
  `SQLDescribeParam`/`SQLDescribeCol` метаданные; блокировка
  `SQLPrepare`/`SQLRowCount`/`SQLNumResultCols` на реальных данных не
  воспроизведена (на этом сервере они не блокируются на lock-wait -
  корректность конструктивная); отдельная блокировка
  `SQLMoreResults`/`NextResultSet` не воспроизведена - нужен
  многорезультиатный сценарий. Эти ограничения не следует считать
  исправленными успешными тестами отмены SQLExecute и чтения.
- [x] **Высокий: потеря отмены перед входом в SQLExecute** (исправлено).
  Однократный SQLCancel мог завершиться до запуска worker, после чего
  SQLExecute начинал выполняться и ожидание worker не прерывалось.
  Выполнение теперь синхронное, отмена выполняется отдельным watcher с
  повторными попытками и обязательным ожиданием его завершения.
  `TestCallWithContextCancelBeforeEntry` принудительно пропускает первую
  отмену до входа в операцию и проверяет повторную; дополнительно проверены
  заранее отменённый контекст и ожидание ещё работающего cancel.
- [x] **Средний: отменённый прямой запрос может вернуть успех** (исправлено
  для отмены во время SQLExecute). После нативного вызова и завершения
  watcher возвращается `ctx.Err()`. При отмене закрывается возможный курсор.
  Старый `waitQuery` удалён; отмена после завершения SQLExecute остаётся
  гонкой завершения, в частности получение метаданных не наблюдает контекст.
- [x] **Высокий: NUMERIC/DECIMAL декодируются через float64** (исправлено).
  `column.go`, `NewColumn`: `SQL_NUMERIC`/`SQL_DECIMAL` привязываются как
  `SQL_C_CHAR` (строка с сохранением масштаба), буфер - precision + знак,
  точка и NUL. `SQL_FLOAT`/`SQL_REAL`/`SQL_DOUBLE` остаются `SQL_C_DOUBLE`.
  Регрессия: `TestIFXDecimal`, `decimal(32,0)` со значением `9007199254740993`
  и `decimal(16,4)` со значением `1.0001`, сравнение точного текста.
- [x] **Средний: буфер имени колонки не оставляет места для NUL** (исправлено).
  `column.go`, `readColumnName`: повторное чтение при `namelen >= len(namebuf)`
  с буфером `namelen+1`. Регрессия: `TestReadColumnName` с границами
  148/149/150/151/300 через фейк `SQLDescribeCol`; live-проверка на сервере
  с длинными именами не выполнялась.
- [x] **Средний: неуспешный Begin оставляет `c.tx`** (исправлено).
  `tx.go`, `Begin`: `c.tx` присваивается после успешной установки атрибута.
  Регрессия: `TestIFXBeginErr` через `testBeginErr`, включая проверку
  `c.tx == nil`, `c.bad` и отсутствия утечки хендлов.
- [x] **Средний: обработчик chunked-данных может паниковать на диагностике**
  (исправлено). `column.go`, `getDataChunked`: заменена паникующая
  проверка типа `.(*Error)` на безопасную с возвратом исходной ошибки.
  Отдельная регрессия не добавлена: нужна инъекция сбоя `NewError`.
- [x] **Средний: тестовый helper проверяет устаревшие счётчики и копирует
  mutex** (исправлено). `stats.go`: добавлен `StmtCountSnapshot` под
  mutex; `helpers_test.go`, `closeDB` снимает снимок после `db.Close`;
  `ifx_test.go`, `ifxConnect` больше не копирует `Stats`.

## Риски, которые ещё надо подтвердить

Следующий приоритет: `SQLGetData` в колоночном цикле (смена интерфейса
`Column`), C TSan/Valgrind для закрепления и конкурентной отмены,
многорезультиатная регрессия `NextResultSet`. Отмены SQLExecute, чтения,
подготовки, `SQLRowCount` и `SQLNumResultCols` реализованы через
`callWithContext`; закрепление буферов - через `runtime.Pinner`.

- [ ] **Высокий: закрепление Go-памяти, сохраняемой C-драйвером.**
  `param.go`, `BindValue` передаёт буферы и индикаторы в `SQLBindParameter`,
  `column.go`, `BufferLen.Bind` — в `SQLBindCol`. C использует эти указатели
  позже, в другом cgo-вызове. Поля `Data`/`Buffer` удерживают объекты живыми,
  но не заменяют явное закрепление. Проверить весь lifetime, применимость
  `runtime.Pinner` либо C-буферов, перепривязку, закрытие и ошибки частичного bind.
  Проверки: `GOEXPERIMENT=cgocheck2`, `-race`, `GOGC=1`, многократные rebind/GC.
  Отсутствие panic не доказывает корректность cgo lifetime.
  Реализовано через `runtime.Pinner` 2026-11-02: `Parameter.pinner`
  закрепляет буфер данных и адрес индикатора после успешного
  `SQLBindParameter` (перепривязка сначала снимает старые закрепления);
  `BindableColumn.pinner` закрепляет `Buffer` и `Len` после `SQLBindCol`
  (`Unbind` снимает); `releaseHandle` снимает все закрепления перед
  освобождением хендла statement. Регрессия: `TestIFXRebindGC` - 100
  перепривязок int64/string/decimal/float64 с `runtime.GC()` между bind
  и execute, контроль строк и точного decimal. Проверено: `make test-ifx`
  PASS, cgocheck2 smoke PASS (27111 операций; до исправления cgocheck2
  тоже не падал - детектор не видит указатели, спрятанные драйвером во
  внутренней C-памяти, поэтому корректность обеспечена конструктивно).
  Осталось: C TSan/Valgrind и поведение при будущей moving GC.

- [ ] **Высокий: конкурентная отмена и работа statement.**
  `callWithContext` вызывает SQLCancel из другой горутины. Watcher больше
  не собирает диагностику параллельно нативному вызову; вызывающий ждёт
  завершения watcher перед диагностикой и закрытием. Внутренние гонки C
  и поведение клиентской библиотеки всё ещё требуют отдельной проверки.
  Проверить требования клиентской библиотеки и окна completion/cancel под
  C TSan/Valgrind; Go `-race` внутренние гонки библиотеки не видит.

- [x] **Средний: проверка unsafe-декодирования WCHAR** (исправлено).
  `column.go`, `BaseColumn.Value`: `*[1<<28]uint16` заменён на
  `unsafe.Slice((*uint16)(p), len(buf)/2)` - семантика та же, срез
  корректен под checkptr. Регрессия: `TestBaseColumnValueWChar` -
  ascii/cyrillic/суррогатные пары, NUL-терминация и нечётная длина.
- [x] **Средний: восстановление усечённых bound-колонок** (частично,
  в пределах текущей БД). Регрессия `TestIFXTruncatedColumns`:
  границы varchar 254/255, длинный lvarchar(4000) через chunked-путь,
  NULL отдельно от пустой строки, точное сравнение байтов. Ограничения
  окружения: страница сервера даёт varchar максимум 255 (проверка границ
  1023/1024/1025 недостижима), БД однобайтовая (819) - charset expansion
  и Unicode значения отвергаются сервером ("Inexact character conversion"),
  так что живой триггер пути bound-truncation на этой БД невозможен;
  параметр длиннее ~4000 символов биндится как text и не кастуется в
  lvarchar (длинное значение вставляется литералом). `ct.Length()` для
  всех колонок возвращает 0/не-ok. Пути multi-column unbind справа от
  усечённой и длинные UTF-16 строки требуют multibyte-БД.

- [x] **Средний: текущий `TestIFXTime` не проверяет обычный datetime**
  (исправлено). Добавлен `TestIFXDateTimeFraction`: round-trip современной
  даты с дробными секундами в `datetime year to fraction(5)` с допуском
  20 мкс; зона - локальная на запись и чтение. Устаревший комментарий про
  `SQL_TIME_STRUCT` в `TestIFXTime` заменён на корректный.

## Инфраструктура и границы покрытия

- [ ] **Исправить bootstrap `start-ifx`.** У реально запущенного образа
  `14.10.FC3DE` переменные DB_NAME/DB_USER/DB_PASS не создали БД и пользователя;
  обычный `docker exec ... dbaccess` также не имеет нужного PATH/окружения.
  Нужны явная инициализация, ограниченное ожидание readiness, диагностика ошибки
  и работа из login-окружения informix. Сейчас это записано как отдельная задача,
  а не скрыто за бесконечным readiness-циклом.
- [ ] Добавить общую prepared statement, используемую несколькими воркерами.
- [ ] Проверить запись в логируемой таблице с Commit/Rollback и содержимое после
  каждого исхода. Нынешние транзакции читают данные; TEMP WITH NO LOG не подходит
  для доказательства отката записей.
- [ ] Добавить typed round-trip: NULL, пустые и длинные строки/bytes, Unicode,
  границы int32/int64, DECIMAL, datetime. Сейчас запись проверяет integer.
- [ ] Добавить контроль RSS/FD/goroutines. Сейчас контролируются только
  ODBC-хендлы; стабильные хендлы не исключают утечки другой памяти.
- [ ] Добавить дифференциальную проверку через независимый ODBC-клиент с точным
  сравнением значений, не только факта выполнения SQL.
- [ ] При необходимости портировать C harness/TSan и Windows/Wine.
  Эти инструменты здесь пока не подготовлены и не запускались.

`FETCHSIZE`, массивы параметров, GetInfo и собственный Pool из go_ibm_db здесь
отсутствуют; соответствующие сценарии намеренно не перенесены.
`LastInsertId` явно возвращает not implemented; это ограничение API, не скрытая ошибка.
`Driver.Close` не вызывается под нагрузкой: он закрывает глобальный environment.

## Матрица для ручного запуска

Команды и параметры описаны в README.

| Прогон | Команда | Что записать |
|---|---|---|
| Smoke, 10 секунд | `make stress-ifx IFX_STRESS_DURATION=10s` | PASS 2026-11-02: seed=1, 26448 операций, race чисто, хендлы стабильны |
| Race, 8 workers, 2 минуты | `scripts/stress-informix.sh stress` | PASS 2026-11-02: seed=1, 310047 операций, race-отчётов нет; в нагрузке env/conn/stmt 1/9/8 -> 1/16/8 (пул открыл доп. соединения; сам тест в нагрузке сверяет только env/stmt); cleanup в `openIFXStressDB` после `db.Close` сверяет все три счётчика со снимком, снятым до открытия пула, а не с 9 - PASS; лог /tmp/ifxodbc-stress.DDdmF3 |
| Live fuzz, 1 минута | `scripts/stress-informix.sh fuzz` | PASS 2026-11-02: 12081 execs за 1m, 4 воркера, race чисто, corpus 2 -> 64 (62 new interesting); аварийных входов в testdata/fuzz не записано - corpus живёт в GOCACHE контейнера и не сохранён |
| GC pressure | `scripts/stress-informix.sh gc` | PASS 2026-11-02 (с закреплением Pinner): GOGC=1, GOMEMLIMIT=200MiB, race, seed=1, 312609 операций, ошибок и race-отчётов нет, хендлы стабильны |
| Soak, 30 минут | `scripts/stress-informix.sh soak` | попытка 1 (2026-11-02, worktree 59791b5): FAIL через 3m21s, 534807 операций - ISAM `Could not position within a file via an index` на systables (operation 8); попытка 2: FAIL через 8s - `Cannot create a user thread` на connect. Обе попытки запускались параллельно с другими live-прогонами против того же сервера (лимит ~24 соединения) - soak требует монопольного сервера; повторяется без конкуренции |
| Soak без race | `IFX_RACE= scripts/stress-informix.sh soak` | сравнение RSS с race-прогоном; не запускался (цепочка останавливалась на первом FAIL) |

Время отдельной операции ограничено контекстом 5 секунд, но текущий драйвер
не гарантирует прерывание всех ODBC-вызовов. Реальный аварийный ограничитель —
`go test -timeout`; для soak установлен 40m. Нельзя считать зависание успешно
обработанной отменой или скрывать ошибку в конце нагрузочного интервала.

## Проверки подготовки (без нагрузки)

- Образ `ifxodbc` с новым ODBCINI собран успешно.
- `go test -race -run '^TestNullTerminatorSize$' -count=1 ./...` в этом образе:
  PASS; новые тесты скомпилированы, соединения с БД не открывались.
- `bash -n scripts/stress-informix.sh`, `git diff --check` и dry-run make:
  успешно на момент подготовки. Live-сценарии позже выполнены - записи
  2026-11-02 ниже и матрица выше.
- 2026-11-02: полный live-прогон `make test-ifx` после исправлений PASS:
  `TestIFXTime`, `TestIFXBeginErr`, `TestIFXDecimal`, `TestIFXColumnTypes`.
  Попутно исправлен порядок закрытия rows в `TestIFXColumnTypes`: Informix
  не даёт drop table при открытом курсоре.
- 2026-11-02: smoke `make stress-ifx IFX_STRESS_DURATION=10s` PASS: seed=1,
  8 воркеров, 26448 операций, race-отчётов нет, хендлы стабильны.

## Перепроверка отмены после c4380e8 (2026-11-02)

- PASS, `-race`: unit-регрессии заранее отменённого контекста, потери первой
  отмены до входа в нативную операцию и ожидания завершения cancel.
- PASS, `-race`: полный набор `TestIFX*` (stress пропущен без флага),
  `TestReadColumnName*`, `TestNullTerminatorSize`, `TestCallWithContext*`.
- PASS, `-race -count=20`: `TestIFXCancelBlocked` и `TestCallWithContext*`.
  На живом Informix выполнены 80 проверок блокирующего SQLExecute: прямые
  и подготовленные ExecContext/QueryContext. Дедлайн 200 мс; сторожевой
  откат снимает блокировку через 3 секунды, если отмена сломалась.
  Проверены context.DeadlineExceeded, отсутствие записи отменённого UPDATE,
  повторное использование того же statement и возврат счётчиков хендлов.
  Query-пути намеренно используют UPDATE: это заставляет ждать именно
  SQLExecute, независимо от отложенного чтения SELECT через SQLFetch.
- Лог повторений: `/tmp/ifxodbc-cancel-race.log`. Race-отчётов нет.
- PASS, `-race`: `TestIFXCancelLockWait` после перевода `Rows.Next`/
  `NextResultSet` на `callContext`: чтение под exclusive table lock в
  repeatable read блокируется и прерывается с `context.DeadlineExceeded`
  за ~3 с, покрыты прямой и подготовленный запрос.
- PASS, `-race`: повторный smoke `make stress-ifx IFX_STRESS_DURATION=10s`
  после изменений rows: 25896 операций, race чисто, хендлы стабильны.
- Полный live-прогон `make test-ifx` PASS: `TestIFXCancelLockWait`,
  `TestIFXCancelBlocked`, `TestIFXTime`, `TestIFXBeginErr`,
  `TestIFXDecimal`, `TestIFXColumnTypes`.
- 2026-11-02, закрепление: `TestIFXRebindGC` PASS; cgocheck2 smoke
  `docker run -e GOEXPERIMENT=cgocheck2 ... stress-ifx 10s` PASS и до
  исправления (27161 операций), и после (27111 операций) - детектор этот
  паттерн не видит, корректность конструктивная.

Повторить:

```bash
make test-ifx IFX_TEST_ARGS="-race -v -count=20 -timeout=3m -run 'Test(IFXCancelBlocked|CallWithContext)'"
```
