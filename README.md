odbc driver written in go. Implements database driver interface as used by standard database/sql package. It calls into odbc dll on Windows, and uses cgo (unixODBC) everywhere else.

To get started using odbc, have a look at the [wiki](https://github.com/alexbrainman/odbc/wiki) pages.

This code is based on two repositories:

- [alexbrainman/odbc](https://github.com/alexbrainman/odbc) - the upstream odbc driver; its full git history is preserved here
- [OpenInformix/IfxGo](https://github.com/OpenInformix/IfxGo) - an earlier fork with Informix-specific patches (Informix tests, removal of MSSQL/MySQL/FoxPro tests), applied on top

## Локальный стресс Informix

Обзор драйвера, приоритеты и незакрытые проверки — в [todo.md](todo.md).
Нагрузка запускается вручную; успешный прогон пока не заявлен.

Нужны Docker, готовый сервер `informix_test` в сети `informixnetwork`,
логируемая БД `test` и пользователь `test` с паролем `test` и правом создавать
TEMP-таблицы. Это значения для локального стенда. Текущий `start-ifx` требует
доработки bootstrap (см. todo); наличие контейнера ещё не означает готовность БД.
В текущем локальном контейнере БД и пользователь уже подготовлены.

```bash
make build-ifx
make stress-ifx IFX_STRESS_DURATION=10s
scripts/stress-informix.sh both
scripts/stress-informix.sh gc
scripts/stress-informix.sh soak
```

Скрипт сохраняет логи в отдельный каталог `/tmp/ifxodbc-stress.*` и возвращает
код ошибки make даже при использовании tee. Каталог можно задать через
`IFX_STRESS_LOG_DIR`; повторный запуск в тот же каталог перезаписывает логи.
Логи содержат команду запуска и реквизиты тестового подключения: каталог по
умолчанию закрыт для других пользователей.

Настройки передаются аргументами make или переменными окружения скрипта:

| Переменная | Значение по умолчанию |
|---|---|
| `IFX_STRESS_DURATION` | `2m` (`30m` в режиме soak) |
| `IFX_STRESS_WORKERS` | `8`, допустимо 1–64 |
| `IFX_STRESS_SEED` | `1` |
| `IFX_FUZZ_TIME` | `1m`, 4 процесса фаззера |
| `IFX_TEST_TIMEOUT` | `10m` (`40m` в режиме soak) |
| `IFX_RACE` | `-race`; пустая строка отключает race |
| `IFX_GOGC` / `IFX_GOMEMLIMIT` | `100` / `off`; режим gc задаёт `1` / `200MiB` |
| `IFX_CONTAINER_NAME` / `IFX_NETWORK` | `informix_test` / `informixnetwork` |
| `IFX_DB_NAME` / `IFX_DB_USER` / `IFX_DB_PASS` | `test` / `test` / `test` |

Пример повторения с другим seed:

```bash
IFX_STRESS_SEED=42 IFX_STRESS_WORKERS=16 scripts/stress-informix.sh stress
IFX_RACE= scripts/stress-informix.sh soak
```

Тест проверяет прямые и подготовленные запросы, integer-параметры и RowsAffected,
метаданные, раннее закрытие и полное чтение Rows, пустой результат, восстановление
после ошибки SQL, транзакции чтения, заранее отменённый контекст, переоткрытие
соединений и работу пула database/sql. Каждый воркер пишет только в собственную
TEMP-таблицу. Проверяются значения и освобождение ODBC-хендлов. Блокирующая
отмена, точность DECIMAL, большие строки и откат записей остаются в плане.

Фаззер сохраняет найденный вход в `testdata/fuzz/FuzzIFXSequence/` на хосте
(исходники смонтированы в контейнер). Воспроизведение конкретного входа:

```bash
make test-ifx IFX_TEST_ARGS='-race -v -count=1 -timeout=2m -run FuzzIFXSequence/ИМЯ_ВХОДА -ifxstress'
```

Новые стресс-тесты и фаззер пропускаются без `-ifxstress`. Обычные `TestIFXTime`
и `TestIFXColumnTypes` по-прежнему требуют БД и используют таблицу `temp`.
