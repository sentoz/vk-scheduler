# vk-scheduler

Утилита для запуска и остановки виртуальных машин и кластеров Kubernetes
в VK Cloud по расписанию.

## Описание

`vk-scheduler` управляет существующими ВМ Nova, кластерами Magnum
и Managed-k8s. Поддерживает ежедневные, еженедельные, ежемесячные
и cron-расписания. Один экземпляр обслуживает один проект и регион VK Cloud.

Результат запуска или остановки определяется по стабильному состоянию
`running` или `stopped` в API облака.

## Использование

### Требования

* Учётная запись VK Cloud или токен Keystone с доступом к выбранному проекту.
* Права на чтение состояния и запуск/остановку выбранных ресурсов.
* Основная конфигурация в YAML или JSON и каталог с YAML-расписаниями.
* Go 1.26.2 или новее для сборки из исходников.

### Запуск

Соберите бинарник и скопируйте пример конфигурации.

```bash
make build
cp config.example.yaml config.yaml
```

Укажите часовой пояс и каталог расписаний в `config.yaml`. Создайте
расписания с идентификаторами своих ресурсов по примерам ниже.

```bash
export OS_PROJECT_ID='<project-id>'
export OS_REGION_NAME='<catalog-region>'
export OS_AUTH_TYPE=v3password
export OS_USERNAME='<username>'
unset OS_TOKEN
read -rs -p 'OS_PASSWORD: ' OS_PASSWORD
export OS_PASSWORD
printf '\n'

./build/vk-scheduler --config config.yaml --dry-run
```

`--dry-run` выполняет авторизацию, проверяет доступ к ресурсам и записывает
в лог срабатывания расписаний без запуска и остановки ресурсов. После
проверки запустите приложение в обычном режиме.

```bash
./build/vk-scheduler --config config.yaml
```

Для авторизации по токену установите `OS_AUTH_TYPE=v3token` и передайте
исходный токен через `OS_TOKEN`. Keystone выдаёт токен для указанного проекта.
При HTTP 401 приложение может один раз обновить этот токен с помощью исходных
учётных данных и повторить запрос. При замене исходного `OS_TOKEN` нужно
перезапустить процесс.

### Параметры командной строки

| Флаг | Назначение | По умолчанию |
| --- | --- | --- |
| `-c, --config` | Путь к основному конфигу | `config.yaml` |
| `--auth-type` | Авторизация Keystone (`v3password` или `v3token`) | `v3password` |
| `-n, --dry-run` | Проверка расписаний и чтение состояния ресурсов | Выключен |
| `--log-level` | Уровень логов (`trace`, `debug`, `info`, `warn`, `error`) | `info` |
| `--log-format` | Формат логов (`console` или `json`) | `console` |
| `--version` | Вывести информацию о сборке и завершить работу | |
| `-h, --help` | Показать справку | |

### Переменные окружения

| Переменная | Назначение |
| --- | --- |
| `VK_SCHEDULER_CONFIG` | Путь к конфигу для `--config` |
| `OS_AUTH_TYPE` | Режим авторизации для `--auth-type` |
| `OS_USERNAME`, `OS_PASSWORD` | Учётные данные для `v3password` |
| `OS_TOKEN` | Исходный токен для `v3token` |
| `OS_PROJECT_ID`, `OS_REGION_NAME` | Проект и регион VK Cloud |
| `OS_AUTH_URL` | HTTPS-адрес Keystone v3 |
| `OS_USER_DOMAIN_NAME` | Домен пользователя Keystone |
| `OS_INTERFACE` | Интерфейс каталога сервисов (`public`, `internal`, `admin`) |
| `LOG_LEVEL`, `LOG_FORMAT` | Уровень и формат логов |

Явно заданные флаги имеют приоритет над переменными окружения.
Непустые переменные проекта, региона, адреса Keystone, домена и интерфейса
переопределяют соответствующие поля `cloud`. Для `v3password` оставьте
`OS_TOKEN` пустым.

### Конфигурация

Основной конфиг задаёт часовой пояс, каталог расписаний и параметры
исполнения. Пример `config.yaml` с проектом и регионом из окружения.

```yaml
timezone: Europe/Moscow
schedules_dir: ./schedules
max_concurrent_jobs: 5
validation_resources: true
validation_interval: 10m
operation_timeout: 30m
shutdown_timeout: 5m
metrics_enabled: true
ui_enabled: true
metrics_port: 9090
```

Относительный `schedules_dir` отсчитывается от каталога основного конфига.
Полный список настроек приведён в [config.example.yaml](config.example.yaml).
Изменения основного конфига и учётных данных применяются после перезапуска.

Пример ежедневного расписания для ВМ с запуском в 09:00 и остановкой в 18:00.

```yaml
apiVersion: scheduler.vk/v1alpha1
kind: Schedule
metadata:
  name: vm-workhours
  annotations:
    vk-scheduler/display-name: dev-vm
spec:
  type: daily
  resource:
    type: vm
    id: 11111111-1111-4111-8111-111111111111
  actions:
    start:
      enabled: true
      time: "09:00"
    stop:
      enabled: true
      time: "18:00"
```

В `spec.resource` задаются тип и ID. Проект и регион берутся из основной
конфигурации с учётом окружения. Аннотация `vk-scheduler/display-name`
задаёт название в календаре. По умолчанию используется `metadata.name`.

Примеры для разных типов ресурсов и расписаний находятся в
[examples/schedules](examples/schedules/). Замените синтетические ID на свои.
Один YAML-файл может содержать несколько документов через `---`.

### Автоперезагрузка расписаний

Приложение проверяет каталог `schedules_dir` каждые 10 секунд. Новый набор
применяется целиком после проверки манифестов и доступа к ресурсам.
Если проверка завершается ошибкой, продолжает действовать предыдущий набор.

Операция, уже разрешённая к отправке, может завершиться после обновления.
Чтобы применить несколько расписаний одним обновлением, сохраните их
в одном YAML-файле и замените его через атомарное переименование.

### Развёртывание в Kubernetes

Укажите проект и регион в [deploy/scope.example.env](deploy/scope.example.env),
свои расписания в `deploy/schedules/` и их имена в
[deploy/kustomization.yaml](deploy/kustomization.yaml). Задайте доступный образ
в [deploy/deployment.yaml](deploy/deployment.yaml).

Для первого развёртывания подготовьте приватный env-файл с `OS_USERNAME`
и `OS_PASSWORD`, затем выполните команды.

```bash
kubectl apply -f deploy/namespace.yaml
kubectl -n vk-scheduler create secret generic vk-scheduler-auth \
  --from-env-file=/absolute/path/to/private-vk-scheduler.env
kubectl apply -k deploy/
```

Для токена подключите `deploy/token-mode.patch.yaml` в `patches` Kustomize
и передайте в Secret ключ `OS_TOKEN`. Конфиг монтируется в
`/config/config.yaml`, расписания — в `/schedules/`.

Deployment использует одну реплику и стратегию `Recreate`. Размещайте
планировщик вне кластера, остановкой которого он управляет. Рабочее состояние
хранится в памяти процесса. При перезапуске теряются сведения об операциях
с неизвестным исходом.

### Типы расписаний

| Тип | Параметры действия |
| --- | --- |
| `daily` | `time` в формате `HH:MM` или `HH:MM:SS` |
| `weekly` | `time` и `day` от 0 до 6, где 0 — воскресенье |
| `monthly` | `time` и `day` от 1 до 31. Несуществующая дата пропускается |
| `cron` | `crontab` из пяти полей в часовом поясе приложения |

### Типы ресурсов

| Тип | Сервис VK Cloud | Значение `spec.resource.id` |
| --- | --- | --- |
| `vm` | Nova (`compute`) | UUID ВМ в нижнем регистре |
| `k8s_v1` | Magnum (`container-infra`) | UUID кластера в нижнем регистре |
| `k8s_v2` | Managed-k8s (`managed-k8s`) | Поле `id` из ответа API с сохранением регистра |

Для `k8s_v2` допустимы буквы `A–Z` и `a–z`, цифры, `_` и `-`. ID начинается
с буквы или цифры. Пример есть в
[managed-k8s-weekly.yaml](examples/schedules/managed-k8s-weekly.yaml).

Адреса сервисов берутся из каталога Keystone. Для ручной настройки доступны
`cloud.compute_endpoint`, `cloud.magnum_endpoint` и
`cloud.managed_k8s_endpoint`.

### Действия

* `start` — запуск ресурса.
* `stop` — остановка ресурса.

Для каждого манифеста требуется хотя бы одно действие с `enabled: true`.
Если у ресурса несколько расписаний, цель определяет последнее наступившее
событие. При одновременных `start` и `stop` приложение фиксирует конфликт
и пропускает операцию.

Поле `spec.suspend: true` в любом расписании приостанавливает управление
всем ресурсом. Удаление всех его расписаний снимает ресурс с управления.

### Метрики Prometheus

HTTP-сервер использует `metrics_port` (по умолчанию `9090`).

* `/health` и `/health/live` — состояние процесса.
* `/health/ready` — готовность принимать задачи после запуска.
* `/info` — JSON с информацией о сборке.
* `/metrics` — метрики Prometheus при `metrics_enabled: true`.

`vk_scheduler_operations_total` считает результаты по `resource_type`,
`action` и `status` (`success`, `error`, `dry_run`, `skipped`).
Дополнительные счётчики отражают корректировки валидатора и причины пропуска.

### Календарный UI

При `ui_enabled: true` на том же HTTP-порту доступны:

* `/` — недельный календарь расписаний и состояния ресурсов.
* `/api/calendar?from=YYYY-MM-DD&to=YYYY-MM-DD` — события за выбранные даты
  в JSON.

Интерфейс работает в режиме чтения и рассчитан на доступ из внутренней
сети или через reverse proxy с авторизацией.

### Валидатор состояния

При `validation_resources: true` (по умолчанию) валидатор проверяет ресурсы
при старте и затем с интервалом `validation_interval` (по умолчанию `10m`).
Если состояние расходится с последним событием расписания, валидатор ставит
корректирующую задачу. Ресурсы в переходном состоянии пропускаются.

При `validation_resources: false` продолжают работать будущие срабатывания
расписаний и отображение состояния в календаре.

## Сборка

* `make build` — собрать `build/vk-scheduler` для текущей платформы.
* `make tools` — установить golangci-lint для проверки.
* `make check` — проверить схемы, форматирование, vet, lint и тесты
  с race detector.
* `make release` — собрать бинарники для Linux, macOS и Windows
  на amd64 и arm64.
* `make container` — собрать локальный образ из
  `build/vk-scheduler-linux-amd64`, созданного командой `make release`.

### Переменные сборки

`Makefile` записывает в бинарник версию из Git-тега, SHA коммита,
время сборки в UTC и URL репозитория. Значения доступны через `--version`
и `/info`.
