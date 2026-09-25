# OpenFlux Deploy

Форк [p1neappleXpress/OpenFlux](https://github.com/p1neappleXpress/OpenFlux) с установкой одной командой, защищённой веб-панелью, управлением нодами и автоматическим обновлением серверного бинарника из upstream `main`.

## Установка одной командой

```bash
sudo bash -c "$(wget -qO- https://raw.githubusercontent.com/vnenapravo7-source/openflux-deploy/main/deploy.sh)"
```

Установщик автоматически выберет Docker, если доступен `docker compose`, иначе — `systemd`. Выбор можно зафиксировать:

```bash
# Нативный сервис systemd
sudo bash -c "$(wget -qO- https://raw.githubusercontent.com/vnenapravo7-source/openflux-deploy/main/deploy.sh)" -- --mode systemd

# Docker Compose
sudo bash -c "$(wget -qO- https://raw.githubusercontent.com/vnenapravo7-source/openflux-deploy/main/deploy.sh)" -- --mode docker
```

В конце установки будут показаны HTTPS-адрес панели, логин, сгенерированный пароль и SHA-256 отпечаток сертификата. Пароль показывается один раз. По умолчанию панель использует self-signed TLS; перед принятием предупреждения браузера сверьте отпечаток.

Для полностью автоматической установки передайте параметры через окружение:

```bash
wget -qO /tmp/openflux-deploy.sh https://raw.githubusercontent.com/vnenapravo7-source/openflux-deploy/main/deploy.sh
sudo OPENFLUX_INSTALL_MODE=systemd \
  OPENFLUX_ADMIN_USER=admin \
  OPENFLUX_ADMIN_PASSWORD='change-this-long-password' \
  OPENFLUX_PORT=8088 \
  bash /tmp/openflux-deploy.sh
rm /tmp/openflux-deploy.sh
```

Секреты не рекомендуется помещать в историю shell. Для постоянного сервера безопаснее передать их из секрет-хранилища или защищённого файла окружения.

## Что умеет панель

- Yandex Docs, Yandex Volga, Yandex Board, Mail.ru Docs и Cups.online;
- отдельные учётные записи администратора и пользователей: пользователь видит только свои подключения и журнал;
- любое количество подключений на пользователя, каждое со своим транспортом, ссылкой, статусом и процессом exit-ноды;
- быстрое создание мульти-подключения: имя и ссылки Yandex Docs/Mail.ru Docs, остальные параметры назначаются автоматически;
- отдельный случайный ключ для каждого нового подключения, независимо от выбранного транспорта и режима;
- запуск, остановка и перезапуск каждого подключения;
- для Cups.online — автоматическое отображение base64-кода комнат в карточке подключения с кнопкой копирования (его нужно вставить в поле URL мобильного клиента);
- для каждого подключения — ссылка `openflux://` и QR-код для импорта профиля в Android v1.1.0;
- статус, журнал и лёгкий график RX/TX сервера;
- L3 и L4, batched/zstd и legacy LZ4;
- подключение удалённых серверов как нод;
- ручная и автоматическая проверка обновлений upstream с сохранением конфигурации и резервной копией бинарника;
- отдельная кнопка **Обновить панель**: сборка актуальной версии из `vnenapravo7-source/openflux-deploy`, проверка и перезапуск панели в systemd или Docker;
- проверка новых версий сервера и панели при входе администратора с предложением обновить каждый компонент;
- кнопки отката сервера и панели на сохранённую предыдущую версию;
- раздел **Инструкции**: редактируемые администратором текстовые блоки с форматированием, фото, видео и вложения для всех пользователей;
- ссылки на [iOS TestFlight](https://testflight.apple.com/join/BwnAcdus) и [Android-клиент](https://github.com/damnurmum/OpenFlux-Android/releases/latest).

> График показывает суммарный трафик сетевых интерфейсов сервера, кроме loopback. Это сознательно лёгкий мониторинг без Prometheus и базы временных рядов.

В разделе **Пользователи** администратор создаёт учётные записи и назначает владельца при создании подключения. Пользователь может добавлять несколько собственных подключений, менять их настройки, перезапускать и смотреть их журналы. Администратор видит все подключения, а также общие графики, ноды и обновления. Сервер хранит пароли в виде bcrypt-хешей; после смены пароля активные сеансы пользователя завершаются. При удалении пользователя удаляются и останавливаются все его подключения.

В разделе **Мой профиль** каждый пользователь может изменить свой логин или пароль, указав текущий пароль. Администратор также может изменить логин или сбросить пароль другого пользователя в разделе **Пользователи**. Логины уникальны без учёта регистра; после изменения логина или пароля сеансы этого пользователя завершаются.

Для пароля нет требований к длине или составу; он должен быть только непустым.

При обновлении старой установки прежнее единственное подключение из `config.json` автоматически переносится в `connections.json` и назначается первому администратору. Данные пользователей, подключений и нод сохраняются при повторном запуске установщика. Если учётные записи уже существуют, установщик не меняет их пароли и не показывает новый пароль.

Код Cups.online генерируется upstream OpenFlux при старте, поэтому появится в карточке после создания комнат. После перезапуска процесса комнаты и код могут измениться — скопируйте новый код в клиент. Код виден только владельцу подключения и администраторам.

В карточке подключения нажмите **Ссылка и QR**: панель соберёт профиль в официальном формате OpenFlux `openflux://v1/`. Android v1.1.0 может открыть ссылку или считать QR-код. Если в профиле есть Direct, укажите публичный IP-адрес или домен VPS. Для Cups.online сначала запустите подключение, чтобы появился код комнат. Ссылка и QR содержат общий ключ шифрования — передавайте их только своим устройствам. Ключ и QR остаются раскрытыми при обновлении списка и скрываются повторным нажатием.

Для Yandex Board выберите одноимённый транспорт и вставьте ссылку вида `https://boards.yandex.ru/whiteboard/?hash=...`. Деплой применяет исправление формата запросов `/api`: Board теперь требует JSON и отклоняет старый form-encoded запрос с HTTP 415. После обновления панели 0.6.6 серверная часть пересобирается с исправлением автоматически, если есть включённое подключение Board и разрешено автообновление. В мобильном клиенте тоже должна быть поддержка этого транспорта — добавление пункта в панели не обновляет приложение на телефоне.

### Режим OpenFlux 0.0.5

В форме подключения можно включить **Защищённую сессию** и указать до восьми транспортов с приоритетами. Для разных типов транспорта панель использует штатные `--transports`, URL-флаги и `--negotiate`; при повторе одного типа создаёт именованный `--config` с отдельным URL каждого документа. В обоих случаях передаются URL контекста шифрования (`--url`), `--max-packet-size` и отдельное хранилище cookies. По умолчанию URL контекста — первый Yandex Docs, иначе первый документ; при необходимости его можно задать вручную. Приоритетный транспорт используется первым; при потере связи ядро переключается на другой. Для Direct TCP оставьте поле порта пустым: панель назначит свободный порт из диапазона 39000–39015 и не даст двум подключениям занять один порт.

При новой Docker-установке этот диапазон уже опубликован автоматически. У ранее установленного контейнера Docker нельзя изменить опубликованные порты без пересоздания. Панель обнаружит старую конфигурацию и предложит скопировать команду для **однократного** повторного запуска установщика; пользователи и подключения сохранятся. После этого порты выбираются в панели автоматически. Docker сам настраивает свои сетевые правила и может обходить UFW; если у провайдера VPS включён отдельный облачный firewall, его правила меняются в панели провайдера, не в OpenFlux.

Новый режим требует Batched и одинаковый секретный ключ шифрования у сервера и клиента. Поле ключа можно оставить пустым — панель создаст его — либо вставить готовый ключ или абсолютный путь к файлу. Клиент должен поддерживать `--negotiate` и иметь тот же набор транспортов, адреса, ключ и URL контекста. Для одного транспорта есть отдельный переключатель `--negotiate`; старые подключения не меняют протокол автоматически. UDP на стороне ядра доступен, но на iOS включается отдельно в мобильном приложении. Cookies одиночных подключений теперь хранятся в постоянном томе Docker, отдельно для каждого подключения. SmartCaptcha, которую получил сам сервер, нельзя подтвердить одной лишь проверкой на телефоне: по протоколу upstream передача cookies на exit-ноду требует уже работающего канала связи. Если одиночный Yandex заблокирован CAPTCHA до открытия документа, панель покажет это состояние вместо «Работает»; на таком IP необходим отдельный способ авторизации с адреса сервера либо другой доступный транспорт. Веб-панель не подменяет IPC-компонент мобильного клиента.

Форма создания подключения содержит прямые ссылки на [Yandex Docs и Volga](https://docs.360.yandex.ru/) и [Mail.ru Docs](https://doc.mail.ru/). Для Yandex Docs снимите галочку «Перейти на новый редактор», для Volga оставьте её. Ссылку на документ в обоих сервисах и в Mail.ru Docs берите через «Поделиться».

## Инструкции

В панели 0.5.1 добавлены имена полей формы и видимый результат проверки версий, но у части установок загрузка медиа продолжала отвечать «kind and a named file are required». Повторная Docker-установка той же ревизии больше не заменяет резервную копию идентичным бинарником.

В панели 0.5.2 отправка медиа явно прикрепляет тип, заголовок и выбранный файл. Кнопки обновления неактивны, если проверка версий не обнаружила новой ревизии. После входа администратора при доступном обновлении появляется окно с выбором компонента и опцией «Не напоминать об этих версиях» для текущего браузера. При появлении следующей ревизии напоминание возвращается.

Раздел **Инструкции** виден всем вошедшим пользователям. Администратор может добавлять, изменять, удалять и менять порядок блоков. Текст поддерживает заголовки, жирный и курсив, списки, ссылки и предпросмотр; HTML-код не выполняется. Фотографии (до 20 МБ), видео MP4/WebM/MOV (до 200 МБ) и другие вложения (до 50 МБ) загружаются на сервер. Файлы доступны только после входа в панель. Метаданные хранятся в `/etc/openflux-deploy/instructions.json`, сами файлы — в `/var/lib/openflux-deploy/instruction-assets`; повторная установка их не удаляет.

## Подключение серверов как нод

На дополнительном сервере выполните:

```bash
sudo bash -c "$(wget -qO- https://raw.githubusercontent.com/vnenapravo7-source/openflux-deploy/main/deploy.sh)" -- --role node --mode systemd
```

Установщик напечатает URL ноды, токен и SHA-256 отпечаток TLS-сертификата. В центральной панели откройте **Ноды → Добавить ноду** и внесите эти три значения. Токен хранится на центральном сервере в файле с правами `0600` и не возвращается браузеру после сохранения. Команды между панелью и нодой идут только по HTTPS; для self-signed сертификата используется строгая проверка указанного отпечатка.

## Обновления

`openflux-update.timer` (systemd) либо встроенный updater (Docker) проверяет `p1neappleXpress/OpenFlux:main` вскоре после старта и затем каждые 6 часов. Новая версия сначала собирается во временном каталоге и проходит smoke-тест. Только после этого бинарник атомарно заменяется, а старый остаётся как `openflux.rollback`. Настройки, ноды, сертификат и учётные данные находятся вне каталога исходников и не перезаписываются. В панели доступен журнал последней попытки обновления сервера; в Docker он сохраняется в `/var/lib/openflux-deploy/server-update.log`.

При повторном запуске Docker-установщика точная ревизия upstream `main` передаётся в сборку как аргумент. Это не даёт Docker использовать старый кэш слоя с `git clone`, даже если файлы самой панели не изменились.

Автообновление можно выключить в панели. Ручная кнопка обновления работает независимо от расписания.

Начиная с upstream OpenFlux 0.0.5, существующие подключения продолжают запускаться в совместимом режиме без `--negotiate`. Новые протокольные возможности (согласование сессии, несколько транспортов, обмен cookies через приложение) нельзя включать только на сервере: для них нужна соответствующая поддержка мобильного клиента. Обновление ядра не включает эти режимы автоматически.

При входе администратора панель асинхронно сверяет ревизии сервера и панели с их основными ветками GitHub и показывает предложение обновить только устаревший компонент. Проверку можно повторить кнопкой **Проверить версии**. При отсутствии доступа к GitHub работа панели не блокируется — показывается ошибка проверки.

Кнопки **Откатить сервер** и **Откатить панель** используют последние сохранённые бинарники и номера ревизий. Откат сервера проверяет состояние подключений и отключает автообновление, чтобы проблемная версия не установилась снова по расписанию. Откат панели перезапускает сервис/контейнер и требует повторного входа. После успешного отката текущая версия становится резервной, поэтому повторное нажатие может вернуть её обратно. Кнопка недоступна, пока нет резервной копии. Для уже установленного Docker-образа один раз повторно запустите установщик, чтобы добавить поддержку отката и обновить резервные копии.

Кнопка **Обновить панель** доступна администратору отдельно от обновления серверного OpenFlux. Панель загружает свой исходный код из `vnenapravo7-source/openflux-deploy:main`, собирает новый бинарник, проверяет его и сохраняет предыдущий как `openflux-panel.rollback`. Ход работы и ошибки отображаются под кнопкой и сохраняются в `/var/lib/openflux-deploy/panel-update.log`. Операции скачивания и сборки имеют ограничение по времени. Затем панель перезапускается; после этого нужно войти снова. Пользователи, подключения, ноды и TLS-сертификат остаются в постоянных каталогах. В Docker обновлённый бинарник хранится в `/var/lib/openflux-deploy/bin`, поэтому сохраняется при перезапуске контейнера. Первое включение этой функции и установка исправления перезапуска Docker на ранее установленном сервере требуют повторно запустить установщик, чтобы обновить сервис и Docker-образ.

Если новая панель не запускается, верните `/var/lib/openflux-deploy/bin/openflux-panel.rollback` на место `openflux-panel`, а `panel-revision.rollback` — на место `panel-revision`, затем перезапустите `openflux-panel.service` или Docker-контейнер.

## Файлы на сервере

| Путь | Назначение |
|---|---|
| `/etc/openflux-deploy/config.json` | настройка автообновления и источник миграции старого подключения |
| `/etc/openflux-deploy/connections.json` | подключения и их владельцы (`0600`) |
| `/etc/openflux-deploy/users.json` | пользователи и bcrypt-хеши паролей (`0600`) |
| `/etc/openflux-deploy/nodes.json` | подключённые ноды и токены (`0600`) |
| `/etc/openflux-deploy/instructions.json` | блоки инструкций (`0600`) |
| `/etc/openflux-deploy/panel.env` | учётные данные панели (`0600`) |
| `/var/lib/openflux-deploy/` | рабочий бинарник, версия и резервная копия |
| `/var/lib/openflux-deploy/bin/openflux-panel` | бинарник панели и резервная копия после её обновления |
| `/var/lib/openflux-deploy/panel-update.log` | журнал последнего обновления панели |
| `/var/lib/openflux-deploy/instruction-assets/` | фото, видео и вложения инструкций |
| `/opt/openflux-deploy/` | установочные файлы и Docker Compose |

## Диагностика

```bash
# systemd
systemctl status openflux-panel
journalctl -u openflux-panel -n 100 --no-pager

# Docker
cd /opt/openflux-deploy && docker compose ps
cd /opt/openflux-deploy && docker compose logs --tail=100

# health-check (не требует авторизации)
curl -k https://127.0.0.1:8088/healthz
```

## Upstream OpenFlux

Ниже сохранена исходная документация OpenFlux.

---

# OpenFlux

**English** | [Русский](README.ru.md)

Network stack research tool. TCP tunnel with pluggable transports,
batched+zstd codec, and two exit-node backends (L3 raw forward / L4 gVisor proxy).

# Disclaimer

The author of OpenFlux **does not encourage** the use of this project to bypass
restrictions or violate the rules of any platform, and **is not responsible**
for the final scenarios of how users apply this tool in real life or on the
Internet. Any specific technical features of the application are nothing more
than an **architectural coincidence**, created **without any intent**.

The project is **entirely non-commercial**, contains **no paid features, hidden
subscriptions, or commercial benefit**.

The author **is not responsible** for forks, modifications, or derivative
versions of OpenFlux created by third parties. Any changes added to a fork are
the responsibility of its author.

The author **is not responsible** for:

- Any use of OpenFlux by third parties
- Consequences caused by the use of forks and modifications
- Damage resulting from derivative versions
- Violations committed using forks

The original code is provided **as is**, **without any warranties**.

## Clients

| Platform | Download | Notes |
|----------|----------|-------|
| **macOS**   | build from source | CLI + utun L3 client (`--inbound=tun`, default on macOS) |
| **Linux**   | build from source | CLI client (SOCKS5) / exit node (L3 or L4) |
| **Windows** | build from source | CLI client (SOCKS5) / exit node (`l4`, or `l3` via QEMU - see TODO) |
| **Android** | [OpenFluxAndroid releases](https://github.com/p1neappleXpress/OpenFluxAndroid) | Standalone APK |
| **iOS**     | [TestFlight beta](https://testflight.apple.com/join/BwnAcdus) | System-wide VPN via Network Extension |

> **iOS app** built by [@saharev1](https://github.com/saharev1) - full iOS client,
> TestFlight pipeline, system VPN support, DNS-over-TLS, and many stability fixes.
> HUGE thanks!
>
> **Android app** - [p1neappleXpress/OpenFluxAndroid](https://github.com/p1neappleXpress/OpenFluxAndroid).

## Architecture

Any client works with either exit backend. `--mode` is chosen on the **exit
node**, not on the client.

```
Client (any):  macOS (utun) / Linux / Windows / iOS (packet tunnel) / Android
                    |
                    v
               Transport (Yandex.Docs / Volga / MAX / Cups / Mail.ru)
                    |
                    v
               Exit node  -->  Internet
                 --mode l3   (raw SNAT/DNAT, Linux + root)
                 --mode l4   (gVisor proxy, any platform)
```

| Client (any)                            | Exit backend | Requires              |
|-----------------------------------------|--------------|-----------------------|
| macOS / Linux / Windows / iOS / Android | `--mode l3`  | exit on Linux + root  |
| macOS / Linux / Windows / iOS / Android | `--mode l4`  | nothing               |

In `l3`, the exit node terminates nothing: it forwards raw IP packets with
SNAT/DNAT (conntrack + egress-IP filter). One TCP connection end-to-end
between the client and the real server.

In `l4`, the exit node terminates TCP in a userspace gVisor stack, then
re-dials the real server with `net.Dial`. Works on any OS, no root.

The client terminates TCP locally (gVisor, utun, or NEPacketTunnelProvider),
then sends raw IP packets into the transport.

## Exit-node backends

The exit node has exactly **two** backends, selected with `--mode` on the
**exit node**. The client does not choose a backend - the same client works
against either.

| `--mode` | Backend | Forwarding | Requires | Platforms |
|----------|---------|-----------|----------|-----------|
| `l3` | Raw L3 | SNAT/DNAT on raw IPv4 via SOCK_RAW + conntrack. No userspace TCP stack. | root / CAP_NET_RAW | Linux only |
| `l4` (alias `proxy`) | gVisor proxy | Terminates TCP in a userspace gVisor stack, then `net.Dial` to the real server. | nothing | Linux, macOS, Windows |

- `proxy` is a deprecated alias for `l4`; both select the same backend.
  `l4` is the canonical name going forward.
- **l3 is faster** (single end-to-end TCP connection, no double termination)
  but Linux-only and needs root.
- **l4 works everywhere** without root, at the cost of terminating TCP twice
  (client -> gVisor on exit -> real server).
- On Linux with root, prefer `l3`. On Windows, the intended path is `l3`
  inside a lightweight QEMU VM (see TODO) - the WinDivert backend is not wired
  yet, and `l4` is the working fallback until QEMU is shipped. On non-root
  hosts, use `l4`.

### l3 and kernel RSTs

In `l3` mode the kernel sees return packets for connections it never opened
and emits RSTs, tearing the tunnel connections down. Drop them:

```
# Scoped (recommended): assign a dedicated egress IP, run with --local-ip, then:
sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -s <egress-ip> -j DROP

# Host-wide fallback (drops ALL outbound RST; makes closed ports look filtered):
sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP
```

The L3 code additionally drops client-originated RSTs before `sendto()`, so
the kernel rule above is only needed for kernel-generated RSTs.

## Highlights

- **Pluggable transports** - Yandex.Docs (WS), Yandex Volga (HTTP relay + WS),
  MAX/OneMe (WebRTC DataChannel), Cups.online (Centrifugo rooms),
  Mail.ru Docs (WS).
- **Batched + zstd codec** - coalesces many tunnel packets into a single
  transport message. Fewer channel messages, higher throughput. See
  `transport/batched.go` and `transport/framing.go`.
- **Two exit backends** - `l3` (raw SNAT/DNAT) and `l4` (gVisor proxy).
  See [Exit-node backends](#exit-node-backends).
- **macOS utun client** - `--inbound=tun` (default on macOS). Creates a utun
  interface, watches its own sockets to install bypass routes, then takes
  the default route. No SOCKS5, no gVisor on the client.
- **iOS packet tunnel** - NEPacketTunnelProvider, pure L3 forwarding.
- **Legacy codec** - `--codec=legacy` reverts to the old per-packet LZ4 codec
  (compatible with older clients).
- **Optional encryption** - `--encryption-key-file` wraps the transport in
  AES-256-GCM. Both peers must share the secret.
- **Benchmark modes** - `--role=bench-send --bench-bytes=N` / `--role=bench-sink`
  measure raw goodput through the transport without touching the host network.

## Requirements

1. **Go** - to build the desktop client / exit-node binary. See `go.mod` for
   the exact version.
2. **Android NDK r27+** - to build the Android client binary.
3. **Xcode 26.6+** - to build the iOS client binary.
4. **A Linux VPS / VDS** for the exit node. The `l3` backend requires root;
   `l4` works without.

## Structure

```
OpenFlux/
  main.go                          # CLI entry (client / exit / benches)
  bench.go                         # Benchmark helpers
  tun_darwin.go                    # macOS utun L3 client
  tun_watch.go                     # Socket watcher for bypass routes
  tun_other.go                     # Stubs for non-darwin platforms
  export_ios.go                    # cgo bridge for the iOS static library
  transport/
    transport.go                   # Transport interface
    batched.go                     # BatchedTransport (coalescing + zstd)
    framing.go                     # Wire framing for batched frames
    compressor.go                  # Legacy per-packet LZ4 codec
    encrypted.go                   # Optional AES-256-GCM wrapper
    yandex/                        # Yandex.Docs + Volga backends
    oneme/                         # MAX Messenger backend
    cupsonline/                    # Cups.online backend
    mailru/                        # Mail.ru Docs backend
  tunnel/
    tunnel.go                      # Client tunnel (gVisor + TunnelLinkEndpoint)
    endpoint.go                    # Virtual NIC (client)
    exit.go                        # NewExitNode dispatcher (l3 / l4)
    proxy_exit.go                  # L4 exit (gVisor + net.Dial)
    l3/
      l3.go                        # L3Exit: SNAT/DNAT, conntrack, egress filter
      backend.go                   # L3Backend interface
      backend_linux.go             # SOCK_RAW backend (Linux)
      backend_windows.go           # Stub (WinDivert not wired yet)
      backend_other.go             # Unsupported-platform stub
      conntrack.go                 # Conntrack table
      flow.go                      # Flow keys, SNAT/DNAT, checksums
    rawsocket_linux.go             # Legacy raw exit (kept for reference)
    rawsocket_{darwin,windows}.go  # Stubs
    windivert/                     # WinDivert backend (present, not wired to L3 yet)
  socks5/                          # SOCKS5 server (client fallback)
  network/                         # Checksums, packet parsing
  utils/                           # Logging
  ios-app/                         # SwiftUI iOS client (XcodeGen)
  build_ios.sh                     # Build iOS static library (liboflux.a)
  build_ios_app.sh                 # Build + archive + export iOS app IPA
  build_android.sh                 # Build Android client binary
  scripts/
    cleanup-utun.sh                # Remove leftover utun routes (macOS)
    build-flx-linux-img.sh         # Build minimal Alpine rootfs for QEMU
```

## Build

```
go mod tidy
go build -o openflux .
```

Cross-build for the exit node (Linux amd64), stripped:

```
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -trimpath -o openflux-linux .
```

## Usage

### Exit node - L3 (Linux, root)

```
sudo ./openflux --role=exit --mode=l3 \
    --transport=yandex \
    --url="YOUR_YANDEX_DOC_URL"
```

Requires root / CAP_NET_RAW. Install the iptables rule (see
[l3 and kernel RSTs](#l3-and-kernel-rsts)).

### Exit node - L4 (any OS, no root)

```
./openflux --role=exit --mode=l4 \
    --transport=yandex \
    --url="YOUR_YANDEX_DOC_URL"
```

Fallback for platforms where `l3` is unavailable (Windows without WinDivert,
macOS, non-root Linux). Slower than `l3` (double TCP termination).

### Client - macOS utun (default on macOS)

```
sudo ./openflux --role=client --inbound=tun \
    --transport=yandex \
    --url="YOUR_YANDEX_DOC_URL"
```

Creates a utun interface, installs bypass routes for the transport, waits for
the transport to connect, then takes the default route. No SOCKS5.
Requires sudo. All traffic except the transport goes through the tunnel.

### Client - SOCKS5 (all platforms, fallback)

```
./openflux --role=client --inbound=socks5 \
    --transport=yandex \
    --url="YOUR_YANDEX_DOC_URL" \
    --socks5=:1080
```

Point your browser / app at `127.0.0.1:1080` as a SOCKS5 proxy. This is the
default inbound on non-macOS platforms.

### Codec selection

By default the transport uses the batched + zstd codec
(`transport/batched.go` + `transport/framing.go`). For the old per-packet
LZ4 codec, pass `--codec=legacy`:

```
./openflux --role=client --codec=legacy ...
```

**Important:** the batched wire format is NOT compatible with the legacy LZ4
format. Client and exit node must both use the same codec (both new, or both
`--codec=legacy`).

### Encryption (optional)

```
./openflux ... --encryption-key-file=/path/to/secret.txt
```

Both peers must use the same secret file. AES-256-GCM, directional keys.
Unset means unencrypted, unchanged behavior.

### Benchmarks

Measure raw goodput through the transport, without touching the host network:

```
# Sender: push 100 MB
./openflux --role=bench-send --bench-bytes=100 --transport=yandex --url="..."

# Receiver: measure goodput
./openflux --role=bench-sink --transport=yandex --url="..."
```

### Other transports

```
# Yandex Volga (HTTP relay + WS)
./openflux --role=exit --mode=l3 --transport=vyandex --url="..." --debug

# MAX / OneMe (WebRTC DataChannel)
./openflux --role=exit --mode=l3 --transport=oneme \
    --maxToken="..." --maxUid="..." --debug

# Cups.online (Centrifugo rooms)
./openflux --role=exit --mode=l3 --transport=cupsonline --debug
# prints a base64 room list; pass it to the client via --url

# Mail.ru Docs (WS)
./openflux --role=exit --mode=l3 --transport=mailru \
    --url="YOUR_MAILRU_PUBLIC_LINK" --debug
# accepts either a bare weblink (AbCdEfGh1/IjKlMnOp2) or a full URL
# (https://cloud.mail.ru/public/AbCdEfGh1/IjKlMnOp2)
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--role` | `-r` | `client` | `client` \| `exit` \| `bench-send` \| `bench-sink` |
| `--inbound` | `-i` | (platform) | `tun` (macOS) \| `socks5` |
| `--transport` | `-t` | `yandex` | `yandex` \| `vyandex` \| `oneme` \| `cupsonline` \| `mailru` |
| `--mode` | `-m` | `l3` | Exit-node mode: `l3` \| `l4` |
| `--codec` | `-c` | `batched` | `batched` \| `legacy` |
| `--url` | `-u` | `http://#` | Document URL |
| `--socks5` | `-s` | `:1080` | SOCKS5 listen address |
| `--local-ip` | `-l` | (auto) | Egress IP for l3 SNAT / RST filter |
| `--debug` | `-d` | `false` | Verbose per-packet logging |
| `--encryption-key-file` | | | AES-256-GCM shared secret file |
| `--maxToken` | | | MAX auth token (`--transport=oneme`) |
| `--maxUid` | | | MAX user id (`--transport=oneme`) |
| `--bench-bytes` | | `0` | MB to push (`--role=bench-send`) |
| `--bench-compressible` | | `false` | Use compressible payload (bench) |

Deprecated (kept for one release, mapped automatically to the new flags):
`--client`, `--exit-node`, `--tun`, `--socks5-mode`, `--legacy`,
`--bench-send`, `--bench-sink`.

## Implementing custom transports

Implement the `Transport` interface from `transport/transport.go` and register
your transport in the `main.go` switch block (see `transport/mailru/` for a
complete example). The batched codec (`BatchedTransport`) wraps any transport,
so a new backend gets batching for free.

## TODO

- **L3 exit on Windows and macOS.** The L3 exit currently works on Linux
  (SOCK_RAW) only; Windows and macOS use `--mode=l4`. The `tunnel/windivert/`
  package (Windows) exists but is not wired to the L3 forwarder yet. A native
  macOS L3 exit is not implemented.
- **Run the exit node (QEMU).**

## License

GNU General Public License v3.0 or later. See LICENSE for the full text.

Third-party licenses are listed in [NOTICE](NOTICE).
## Параллельные документы и Direct

В защищённой сессии можно добавить несколько Yandex Docs или Mail.ru Docs — каждый документ получает отдельное имя транспорта. Ключ можно оставить пустым (панель создаст его) или вставить готовый ключ из клиента; после сохранения ключ показывается и копируется кнопкой в карточке подключения. Direct TCP также доступен как отдельное подключение без включения режима нескольких транспортов. Для Docker после этого обновления один раз повторно запустите установщик, чтобы собрать сервер с поддержкой отдельных URL и применить публикацию TCP-портов.
