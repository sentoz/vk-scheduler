# vk-scheduler

Планировщик запуска и остановки существующих ресурсов VK Cloud на Go.
Один процесс обслуживает один проект и регион. Поддерживаются ВМ Nova,
Kubernetes Magnum (`k8s_v1`) и **Managed-k8s** (`k8s_v2`).

| Тип ресурса | Catalog service | Запуск / остановка |
| --- | --- | --- |
| `vm` | `compute` | Nova `os-start` / `os-stop` |
| `k8s_v1` | `container-infra` | Magnum `turn_on_cluster` / `turn_off_cluster` |
| `k8s_v2` | `managed-k8s` | POST `clusters/{id}/turn_on` / `turn_off`, JSON `{}` |

Допустимы только `vm`, `k8s_v1`, `k8s_v2`. Другие имена типов отклоняются
при загрузке; преобразования и алиасов нет.

Успех операции определяется стабильным `running` или `stopped` в API облака.
Нагрузка, приложения, guest OS, nodes Ready и workloads не проверяются.

## Сборка и запуск

Требуются Go 1.26.2+ и доступ к Keystone и нужным сервисам выбранного проекта.
Для race tests нужен C compiler. Для lint используется golangci-lint 2.12.2.

```bash
make tools
make check build
./build/vk-scheduler --help
./build/vk-scheduler --version
cp config.example.yaml config.yaml
```

Укажи в `config.yaml` свой каталог `schedules_dir` и timezone. Создай в нём
расписания по [examples](examples/schedules/). Все IDs там синтетические:
замени resource IDs перед подключением к облаку. Project и region задаются
один раз через `OS_PROJECT_ID`/`OS_REGION_NAME` либо глобальный `cloud`.
Секреты не хранятся в конфигурации или manifests.

Password auth:

```bash
export OS_AUTH_TYPE=v3password
export OS_PROJECT_ID='<project-id>'
export OS_REGION_NAME='<catalog-region>'
export OS_USERNAME='<service-user>'
read -rs -p 'OS_PASSWORD: ' OS_PASSWORD
export OS_PASSWORD
printf '\n'
./build/vk-scheduler --config=config.yaml --dry-run
```

После проверки конфигурации запусти ту же команду без `--dry-run`.
Dry-run выполняет auth и GET preflight, но не вызывает resource start/stop.
Help/version работают без config, credentials и сети.

Для token auth выбери `OS_AUTH_TYPE=v3token` и передай `OS_TOKEN` через
environment. Он используется для получения project-scoped токена Keystone.
Срок исходного `OS_TOKEN` не определяется приложением. Срок полученного
scoped token проверяется по `expires_at` в ответе Keystone.
При 401 приложение один раз повторяет auth исходным `OS_TOKEN` и повторяет
отвергнутый запрос. Параллельные 401 используют один refresh; scope и expiry
нового токена проверяются до публикации. Password mode использует тот же
механизм с исходными username/password. Истёкший или отозванный исходный
`OS_TOKEN` нужно заменить и перезапустить процесс. Наличие `OS_TOKEN` в
password mode считается ошибкой неоднозначности.

Token mode выполняет `POST /auth/tokens` с исходным токеном в JSON и explicit
project scope; он использует выданный scoped token в resource requests.
Успешный запрос с исходным токеном напрямую не проверяет возможность такого
обмена. Auth 404 сам по себе не доказывает истечение исходного токена:
нужно проверить отказ именно на identity endpoint и project scope.
Сырое тело ошибки не логируется, поскольку оно может содержать сам токен.

Непустые `OS_AUTH_URL`, `OS_PROJECT_ID`, `OS_REGION_NAME`,
`OS_USER_DOMAIN_NAME`, `OS_INTERFACE` имеют приоритет над config.
`--auth-type` имеет приоритет над `OS_AUTH_TYPE`. Также доступны
`VK_SCHEDULER_CONFIG`, `LOG_LEVEL`, `LOG_FORMAT`.

## Конфигурация и расписания

Полный config: [config.example.yaml](config.example.yaml).
Основной config, timezone и credentials меняются через restart.
Только schedules перечитываются каждые 10 секунд.
Относительный `schedules_dir` считается от каталога config, независимо от CWD.

В `spec.resource` указываются только `type` и `id`. Project/region наследуются
из итоговой глобальной конфигурации с учётом `OS_*`; в манифестах эти поля
не допускаются. В примере основного config подстановки `${OS_PROJECT_ID}` и
`${OS_REGION_NAME}` тоже не нужны: loader читает эти переменные напрямую.

Для Managed-k8s используй
[пример расписания](examples/schedules/managed-k8s-weekly.yaml) с типом
`k8s_v2` и **полем `id` ответа Managed-k8s**. Это регистрозависимый ID,
например `3AbCdE4FgHiJ5KlMnOpQrStUvWx`, а не обязательно UUID.
Допустимы ASCII-буквы, цифры, `_` и `-`, первый символ — буква или цифра.
Для `vm` и `k8s_v1` требуется lowercase UUID. Endpoint берётся
из catalog для service type `managed-k8s`. При необходимости укажи
`cloud.managed_k8s_endpoint`:

```yaml
cloud:
  managed_k8s_endpoint: https://msk.cloud.vk.ru/infra/container/managed-kubernetes/v1
```

Это фрагмент полного config. Остальные поля — в `config.example.yaml`.
Magnum продолжает использовать `k8s_v1` и `cloud.magnum_endpoint`.
Автоматического переключения между этими API при 404 нет.

* `timezone` обязателен: IANA name, например `Europe/Moscow`.
* `daily`: строго `HH:MM` или `HH:MM:SS`.
* `weekly`: дополнительно `day: 0..6`, воскресенье — `0`.
* `monthly`: `day: 1..31`; отсутствующая дата пропускается.
* `cron`: ровно пять полей; descriptors, seconds, `TZ=` запрещены.
* Нужен хотя бы один action с `enabled: true`; disabled actions не планируются.
* Длительности используют Go units `s`, `m`, `h`, включая `0.5s`.
* Explicit `0`/`0s` не заменяются defaults и отклоняются для timeout/limits.
* YAML и JSON поддерживают readonly env expansion через jamle.
  `${NAME:=value}`, aliases, merge keys и duplicate keys запрещены.
* Неизвестные поля отклоняются до typed decode.

Несколько manifests одного ресурса объединяются по
`project_id:region:type:id`. Самое позднее наступившее событие определяет
цель; одновременные start/stop дают conflict без mutation. Включённый
validator делает initial scan и затем периодические corrections. При
выключенном validator события downtime не догоняются.

`spec.suspend: true` хотя бы в одном manifest приостанавливает управление
всей группой ресурса. Уже разрешённая операция может завершиться. Снятие
suspend при выключенном validator возобновляет только будущие события.
Удаление расписаний, включая пустой bundle, снимает управление и не
выключает ресурс.

Reload публикуется целиком после validation и GET preflight. Невалидный
candidate сохраняет старый набор. Для атомарного обновления нескольких
файлов используй immutable revision directory с переключением symlink либо
один multi-document YAML с atomic rename. Последовательное редактирование
нескольких файлов не является транзакцией. ConfigMap `..data` читается как
одна ревизия. Лимиты: 1 MiB/file, 16 MiB/bundle, 4096 YAML files.

## Исполнение и ограничения

Одна очередь с объединением намерений по ресурсу и fixed worker pool.
`max_concurrent_jobs` ограничивает cloud operations; отдельный
`cloud.max_concurrent_reads` ограничивает все HTTP GET, включая UI.
Перед первым POST исполнитель повторно проверяет актуальную цель и
поколение. POST не повторяется после network/5xx ошибки. После успешного
202 polling ждёт подтверждённое cloud state.

PVC, БД, Redis, внешних кешей и файлового состояния нет. Неопределённый
исход POST блокирует новые actions этого ресурса в текущем процессе до
readonly подтверждения. Marker не истекает по TTL и переживает reload,
но теряется при restart. Exactly-once между restart не гарантируется.
При старом stable API state после потерянного ответа возможен повторный
POST или ошибочный already-desired shortcut. Это ограничение stateless v1.

При SIGINT/SIGTERM закрывается admission, выполняющиеся операции получают
общий drain budget. Последние `min(5s, shutdown_timeout/5)` отведены для
cancellation и join. Превышение drain/hard deadline даёт ненулевой exit code.
Отмена polling не отменяет принятую облаком операцию.

Остановка VM/кластера не означает прекращения всех начислений: диски, IP
и другие ресурсы могут продолжать тарифицироваться по условиям VK Cloud.

## HTTP и наблюдаемость

Общий порт `metrics_port`, по умолчанию 9090:

* `/health`, `/health/live`: lifecycle процесса, без cloud GET.
* `/health/ready`: 200 после startup, 503 до него и при shutdown.
* `/info`: JSON metadata сборки, независимо от `ui_enabled`.
* `/metrics`: при `metrics_enabled`.
* `/`: календарный UI при `ui_enabled`, иначе 404.
* `/app.js`, `/style.css`: assets UI при `ui_enabled`.
* `/api/calendar`: API календаря при `ui_enabled`.

UI read-only, без встроенной auth; доступ через внутреннюю сеть или reverse
proxy. Короткий RAM cache статусов — 30s, один GET на concurrent cold requests
одной identity/generation. UI работает и при выключенном validator.
Calendar range ограничен 366 днями, 100000 events и 64 MiB JSON budget;
превышение — HTTP 400. Отдельный запрос UI ограничен 90 секундами.

Counters: `vk_scheduler_operations_total`,
`vk_scheduler_validator_corrections_total`,
`vk_scheduler_scheduler_skips_total`. Gauges отражают pending resources,
возраст доставки и unconfirmed operations. Readiness не гарантирует
достижения целей всех ресурсов. Логи JSON: `--log-format=json`.

## Поставка и проверки

```bash
make release
kubectl kustomize deploy/
docker build -t vk-scheduler:local .
docker run --rm --network none --read-only vk-scheduler:local --version
```

Release matrix: Linux/macOS/Windows, amd64/arm64. Dockerfile ожидает
`build/vk-scheduler-linux-amd64` и создаёт только linux/amd64 image.
Scratch image содержит CA/tzdata и работает с UID/GID 65532.

Kubernetes пример: [deploy](deploy/). Сначала замени synthetic scope/IDs и
image tag, затем отдельно создай Secret из private env file:

```bash
kubectl -n vk-scheduler create secret generic vk-scheduler-auth \
  --from-env-file=/absolute/path/to/private-vk-scheduler.env
```

Namespace должен существовать до создания Secret. Для token mode добавь
`deploy/token-mode.patch.yaml` в `patches` своего Kustomization.
Replicas=1, strategy=Recreate: два процесса не координируют mutations.
Schedules ConfigMap имеет стабильное имя для hot reload; main config/scope
меняют hash и требуют restart. Mounts только readonly, без subPath.
Startup probe budget 300s рассчитан на один target с defaults; для другого
числа targets/request timeout его необходимо пересчитать.
Не размещай scheduler в кластере, который он сам выключает.

[API-контракты](docs/api-contracts.md), [тестирование](docs/testing.md),
[матрица приёмки](docs/acceptance.md), [статус реализации](IMPLEMENTATION_STATUS.md).
Live совместимость требует smoke на явно разрешённых тестовых ресурсах.
