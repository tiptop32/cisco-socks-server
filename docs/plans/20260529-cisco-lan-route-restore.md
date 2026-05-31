# Cisco LAN Route Restore

## Overview

После подключения Cisco Secure Client инсталлирует маршрут `192.168.0.0/16 → utun4`, который захватывает домашнюю LAN (`192.168.1.0/24`). При входящем TCP-соединении из LAN на SOCKS5-листенер `0.0.0.0:8080` ядро принимает SYN на en0, но SYN-ACK уходит по «широкому» маршруту через `utun4` (в отсутствие /24 на en0 — longest prefix /16 побеждает) → дропается корпоративным шлюзом → клиент получает таймаут 75с.

Решение: до подключения VPN утилита **снимает снапшот** текущего LAN-интерфейса и его connected-prefix (когда таблица ещё чистая), а после успешного коннекта добавляет более специфичный маршрут (`route add -net <subnet> -interface <iface>`), который выигрывает longest-prefix match и возвращает SYN-ACK на физический интерфейс. В существующем supervisor-loop (тик 5с) проверяется, что маршрут всё ещё на месте, и реапплаится, если Cisco его затёр.

Бенефит: SOCKS5-прокси становится доступен с других машин LAN — изначально заявленная цель утилиты, которая сейчас не работает.

## Context (from discovery)

- **Тип проекта**: Go 1.24, single binary, цель — macOS arm64. Сетевая утилита, требует sudo.
- **Файлы, которых касается изменение**:
  - `internal/service/cisco.go` — supervisor-loop, основные правки
  - `internal/service/service.go` — расширение `State` (поля state + кэш-поля под тем же mutex)
  - `internal/utils/route/route.go` — **новый** пакет
  - `internal/utils/tui/status.go` — отображение нового флага
  - `README.md` — короткое примечание
- **Паттерны, на которые опираемся**:
  - Пакет `internal/utils/cisco/cisco.go` — образец для `internal/utils/route`: shell-out'ы через `exec.CommandContext`, единый `run()`-хелпер, типизированные ошибки.
  - `Service.setStatus(func(*State))` под `sync.RWMutex` — единственный путь мутации состояния. **Все** новые поля (включая кэш subnet/iface) живут внутри `State`.
  - `startCisco` уже имеет три «этапа» в одном тике (connect / DisablePF / ciscoReady close). LAN restore добавляется как новый этап.
- **Зависимости**: только стандартная библиотека (`net`, `os/exec`, `strings`, `errors`). Новых внешних модулей не вводится.
- **Диагностика подтверждена**: `Status: Disabled for 22s` (pf не виноват), listener на `*:http-alt`, `route -n get 192.168.1.82` → `interface: utun4 mask 255.255.0.0`.

## Development Approach

- **Testing approach**: без тестов — следуем существующему стилю репо (в `internal/` нет ни одного `_test.go`). Осознанное отклонение от дефолтов планировщика, согласованное на стадии brainstorm. Парсер `parseDefaultNonTunnel` сделан максимально тривиальным; `Exists` реализован через `route -n get` (single-line парсинг), а не через таблицу netstat — поэтому необходимости в широком table-driven тесте нет.
- Каждая задача завершается полностью перед переходом к следующей.
- Маленькие сфокусированные правки.
- **CRITICAL: при изменении scope обновлять этот файл.**
- После каждой задачи — `go build ./...` и ручной прогон утилиты на маке с реальным Cisco-подключением.
- Backward-compatibility: конфиг-формат `~/.cisco-socks5.yaml` не меняется.

## Testing Strategy

- **Unit-тесты**: не вводим в этой итерации (см. Development Approach).
- **E2E**: ручной — `sudo ./cisco-socks-server`, с LAN-клиента `curl --socks5 <mac-lan-ip>:8080 https://httpbin.org/ip`. Должен вернуться `origin: <vpn-exit-ip>` без таймаута.
- **Smoke**: после старта на маке выполнить `route -n get <lan-ip-в-той-же-/24>` — `interface:` должен показать физический интерфейс, не `utun*`.

## Progress Tracking

- Отмечать выполненные пункты `[x]` сразу.
- Новые задачи — с префиксом ➕.
- Блокеры — с префиксом ⚠️.
- Если scope меняется по ходу — править этот файл, не накапливать расхождение.

## Solution Overview

Архитектурно — дописываем единственный новый «этап» в существующий supervisor-loop. Никаких новых горутин, никакой блокировки `ciscoReady`. State и кэш живут на тех же примитивах, что и `PFDisabled`. Изоляция шелл-выовов вынесена в отдельный пакет `internal/utils/route` по точному образцу `internal/utils/cisco`.

Ключевые решения:

- **Снапшот LAN до коннекта Cisco**. Cisco после connect может затереть единственный en0-default. Поэтому `DetectLAN` вызывается **до** первой попытки `Connect`, при пустом кэше. Если на момент старта утилиты VPN уже был поднят кем-то снаружи — fallback на сканирование `net.Interfaces()` (UP, не loopback, не P2P, имя не начинается с `utun`, есть IPv4 в RFC1918).
- **Periodic re-apply** (а не one-shot) — на случай, если Cisco будет переподнимать свой /16. В каждом тике supervisor-а: после коннекта вызвать `Add` (идемпотентно). `Exists` смотрит через `route -n get`, не парся netstat.
- **Идемпотентный `Add`**. macOS `route add` возвращает non-zero на «File exists». Это **не** ошибка — `Add` рассматривает её как «уже на месте». Иначе `LANRestored` никогда не переключится в true и будет бесконечный warn-loop.
- **Маршрут не удаляется при потере VPN** — на en0 он безвреден. Удаление — только в финальном `defer` процесса.
- **Графdeгradация**, а не падение: если `DetectLAN` не нашёл LAN — лог warn, `LANRestored = false`, SOCKS5 продолжает работать на localhost. `ciscoReady` всё равно закрывается — прокси и DNS стартуют независимо.

## Technical Details

### Структуры данных

```go
// service.State — всё под s.mu
type State struct {
    CiscoConnected bool
    PFDisabled     bool
    LANRestored    bool
    LANSubnet      string // например, "192.168.1.0/24"
    LANInterface   string // например, "en0"
    ProxyStarted   bool
    DNSStarted     bool
}
```

Все мутации только через `s.setStatus(func(*State))`. Никаких приватных полей в `Service` для кэша — единственный источник истины это `State`.

### API нового пакета `internal/utils/route`

```go
package route

// DetectLAN ищет первый физический интерфейс с default-route (не utun*)
// и возвращает его connected-CIDR и имя.
// Сценарий 1 (основной): VPN ещё не поднят → netstat показывает обычный default → парсим.
// Сценарий 2 (fallback): VPN уже поднят → default ушёл в utun*, en0-default отсутствует
//   → сканируем net.Interfaces(), берём первый UP, не loopback, не P2P, имя не utun*,
//     с IPv4 в RFC1918 (10/8, 172.16/12, 192.168/16).
func DetectLAN(ctx context.Context) (subnet, iface string, err error)

// Add добавляет interface-route: route add -net <subnet> -interface <iface>.
// "File exists" / "route already in table" → success (идемпотентно).
func Add(ctx context.Context, subnet, iface string) error

// Delete удаляет interface-route: route delete -net <subnet>.
// "not in table" → success.
func Delete(ctx context.Context, subnet string) error

// Exists проверяет, что в таблице действительно destination=subnet → iface.
// Реализация: `route -n get <первый-IP-в-subnet>`, парсим строку `interface: <name>`.
// Это надёжнее, чем парсить netstat: macOS форматирует destinations нестандартно
// для /23, /22 и т.п., и netstat ради этого парсить — хрупко.
func Exists(ctx context.Context, subnet, iface string) (bool, error)

// ErrNoLANInterface — не нашли подходящего физического интерфейса.
var ErrNoLANInterface = errors.New("no non-tunnel LAN interface found")
```

### Поток в `startCisco` (supervisor tick)

В **начале** каждой итерации, до проверки `IsConnected`, если `LANSubnet` ещё пустой — пробуем задетектить (снапшот «до Cisco»):

```go
state := s.GetState()
if state.LANSubnet == "" {
    if subnet, iface, err := route.DetectLAN(ctx); err == nil {
        slog.Info("LAN detected", "subnet", subnet, "interface", iface)
        s.setStatus(func(st *State) {
            st.LANSubnet, st.LANInterface = subnet, iface
        })
    } else if !errors.Is(err, route.ErrNoLANInterface) {
        slog.Warn("failed to detect LAN", "error", err)
    }
    // если LAN не нашли — продолжаем, прокси будет на localhost
}
```

После блока `DisablePF` и **перед** `close(ciscoReady)`:

```go
state = s.GetState()

switch {
case state.CiscoConnected && state.LANSubnet != "" && !state.LANRestored:
    if err := route.Add(ctx, state.LANSubnet, state.LANInterface); err != nil {
        slog.Warn("failed to add LAN route", "error", err,
            "subnet", state.LANSubnet, "interface", state.LANInterface)
        break
    }
    s.setStatus(func(st *State) { st.LANRestored = true })
    slog.Info("LAN route installed",
        "subnet", state.LANSubnet, "interface", state.LANInterface)

case state.CiscoConnected && state.LANRestored:
    ok, err := route.Exists(ctx, state.LANSubnet, state.LANInterface)
    if err != nil {
        slog.Debug("failed to check LAN route", "error", err)
        break
    }
    if !ok {
        slog.Warn("LAN route missing, reinstalling",
            "subnet", state.LANSubnet, "interface", state.LANInterface)
        if err := route.Add(ctx, state.LANSubnet, state.LANInterface); err != nil {
            slog.Warn("failed to reinstall LAN route", "error", err)
        }
    }

case !state.CiscoConnected && state.LANRestored:
    s.setStatus(func(st *State) { st.LANRestored = false })
}
```

`ciscoReady` закрывается без оглядки на `LANRestored` — single-host usage не должен зависеть от наличия LAN.

### Cleanup в `defer` `startCisco`

```go
defer func() {
    state := s.GetState()

    s.setStatus(func(st *State) {
        st.CiscoConnected = false
        st.PFDisabled = false
        st.LANRestored = false
        // LANSubnet/LANInterface оставляем — могут пригодиться для cleanup ниже
    })

    if state.LANSubnet != "" {
        if err := route.Delete(context.Background(), state.LANSubnet); err != nil {
            slog.Error("failed to delete LAN route", "error", err)
        }
    }

    if err := cisco.Disconnect(context.Background()); err != nil {
        slog.Error("failed to disconnect cisco", "error", err)
    }
}()
```

Если процесс убит SIGKILL — `/24`-маршрут на en0 останется в таблице. Это безвредно (он идентичен connected-маршруту и не мешает обычной работе) и будет вытеснен при следующей DHCP-аренде / cмене сети. Документируем в README, исправлять не нужно.

### Парсер `netstat` (для `DetectLAN`)

`netstat -rn -f inet` на macOS даёт колонки `Destination Gateway Flags Netif Expire`. Логика:

1. Сканируем строки.
2. Берём те, где первая колонка == `default`.
3. Среди них первая, у которой 4-я колонка (`Netif`) **не** начинается с `utun`, **не** `lo`, не пустая — это наш LAN-интерфейс.
4. По имени → `net.InterfaceByName(iface).Addrs()` → первый `*net.IPNet` с `To4() != nil`, не `IsLoopback()/IsLinkLocalUnicast()`. Из него:
   ```go
   ones, _ := ipnet.Mask.Size()
   network := ipnet.IP.Mask(ipnet.Mask)
   cidr := fmt.Sprintf("%s/%d", network, ones)
   ```

Если `parseDefaultNonTunnel` вернул пустую строку — переходим к fallback'у на сканирование `net.Interfaces()` (см. API выше).

### Парсер `Exists` (через `route -n get`)

```go
// для subnet "192.168.1.0/24" берём первый адрес — "192.168.1.0"
out, err := run(ctx, "route", "-n", "get", subnetFirstIP(subnet))
// ищем строку "  interface: <name>"
// сравниваем с iface
```

Преимущества над парсингом `netstat`:
- macOS даёт стабильный machine-readable вывод у `route -n get`.
- Учитывает routing-table tiebreaking как ядро его реально вычисляет, а не как netstat форматирует.
- Один if на строку, никакого посимвольного сравнения коротких форм destination.

### TUI

В `internal/utils/tui/status.go` `setupStatus` уже выводит четыре строки через `fmt.Fprintf`:

```
 VPN    ● OK
 Filter ● OK
 Proxy  ● OK
 DNS    ● OK
```

Добавляется одна строка между `Filter` и `Proxy`:

```go
fmt.Fprintf(v, " LAN    %s\n", indicator(state.LANRestored))
```

## What Goes Where

- **Implementation Steps** (с чекбоксами): код, парсеры, интеграция, README.
- **Post-Completion**: ручная верификация на маке с реальным Cisco + curl с LAN-клиента; проверка переподключения VPN; проверка на разных LAN-сетях.

## Implementation Steps

### Task 1: Создать пакет `internal/utils/route` с idempotent Add/Delete

**Files:**
- Create: `internal/utils/route/route.go`

- [ ] создать файл и package declaration `package route`
- [ ] приватный `run(ctx, name, args...) (string, error)` по образцу `internal/utils/cisco/cisco.go`
- [ ] `Add(ctx, subnet, iface)` — выполняет `route -n add -net <subnet> -interface <iface>`. Если stderr содержит `File exists` / `route already in table` — возвращаем `nil`.
- [ ] `Delete(ctx, subnet)` — выполняет `route -n delete -net <subnet>`. Если stderr содержит `not in table` — возвращаем `nil`.
- [ ] объявить `ErrNoLANInterface = errors.New("no non-tunnel LAN interface found")`
- [ ] `go build ./...` зелёное

### Task 2: Реализовать `DetectLAN` (netstat-primary + interface-scan fallback)

**Files:**
- Modify: `internal/utils/route/route.go`

- [ ] `DetectLAN(ctx) (subnet, iface string, err error)`
- [ ] приватный `parseDefaultNonTunnel(netstatOutput string) string` — возвращает имя интерфейса (или пустую строку) для первой `default`-строки с netif не на `utun*`/`lo*`
- [ ] приватный `connectedCIDR(iface string) (string, error)` через `net.InterfaceByName().Addrs()` (первый IPv4 не link-local, не loopback) → канонический `<network>/<plen>`
- [ ] приватный `scanRFC1918(iface preference) (iface, subnet string, err error)` — `net.Interfaces()`, фильтр `FlagUp && !FlagLoopback && !FlagPointToPoint`, имя не на `utun*`, IPv4 в RFC1918 (10/8, 172.16/12, 192.168/16). Возвращает первый подходящий.
- [ ] логика `DetectLAN`: сначала netstat, если нашли — `connectedCIDR`; если нет или ошибка — `scanRFC1918`; если и там пусто — `ErrNoLANInterface`.
- [ ] `go build ./...` зелёное

### Task 3: Реализовать `Exists` через `route -n get`

**Files:**
- Modify: `internal/utils/route/route.go`

- [ ] приватный `subnetFirstIP(subnet string) (string, error)` — парсит CIDR, возвращает сетевой адрес как `1.2.3.0`
- [ ] `Exists(ctx, subnet, iface) (bool, error)`:
  - `route -n get <subnetFirstIP>` → output
  - построчно ищем `interface: <name>`
  - сравниваем `<name>` с `iface`
  - если `route` вернул ошибку «not in table» (или подобную) — `(false, nil)`, не error
- [ ] `go build ./...` зелёное

### Task 4: Расширить `State` полями LAN

**Files:**
- Modify: `internal/service/service.go`

- [ ] добавить в `State`: `LANRestored bool`, `LANSubnet string`, `LANInterface string` — после `PFDisabled`, перед `ProxyStarted`
- [ ] никаких приватных полей в `Service` — кэш живёт в `State`
- [ ] `go build ./...` зелёное

### Task 5: Интегрировать LAN-snapshot и LAN-restore в `startCisco`

**Files:**
- Modify: `internal/service/cisco.go`

- [ ] добавить импорт `github.com/merzzzl/cisco-socks-server/internal/utils/route`
- [ ] **в начале** тика, до `IsConnected`: если `state.LANSubnet == ""` — попытка `route.DetectLAN`, при успехе `setStatus(LANSubnet/LANInterface)`, лог Info; при `ErrNoLANInterface` — молча; иные ошибки — Warn
- [ ] **после блока `DisablePF`, до `close(ciscoReady)`** — switch-блок (Case 1 / 2 / 3) из Technical Details
- [ ] **в `defer`** — снять снимок `state := s.GetState()` перед `setStatus`, далее в `setStatus` обнулить `CiscoConnected/PFDisabled/LANRestored`, после mutex'а — `route.Delete(bg, state.LANSubnet)` под условием `state.LANSubnet != ""`
- [ ] лог-сообщения соответствуют Technical Details (`LAN route installed`, `LAN route missing, reinstalling`, и т.д.) с атрибутами `subnet=`, `interface=`
- [ ] `go build ./...` зелёное

### Task 6: Добавить строку LAN в TUI status

**Files:**
- Modify: `internal/utils/tui/status.go`

- [ ] вставить `fmt.Fprintf(v, " LAN    %s\n", indicator(state.LANRestored))` между строками `Filter` и `Proxy` (l. 36-37 в текущем файле)
- [ ] `go build ./...` зелёное

### Task 7: Ручная проверка на маке с Cisco

**Files:** —

- [ ] `make build-darwin-arm64`
- [ ] остановить старый процесс (если запущен)
- [ ] **до запуска утилиты** убедиться, что VPN отключён и роутинг чистый: `route -n get <свой-lan-ip>` → `interface: en0` (или активный физический)
- [ ] запустить `sudo ./cisco-socks-server`
- [ ] в логах увидеть `LAN detected subnet=192.168.1.0/24 interface=en0` (или соответствующее)
- [ ] дождаться в TUI `LAN ● OK` (после успешного коннекта Cisco)
- [ ] `route -n get 192.168.1.82` (IP другого устройства в той же LAN) → `interface: en0`, **не** `utun*`
- [ ] с LAN-клиента: `curl --socks5 <mac-ip>:8080 https://httpbin.org/ip` — ответ за <5с, IP — VPN-exit
- [ ] остановить утилиту Ctrl-C, убедиться, что маршрут /24 удалён или сведён к connected (логи покажут `route delete` без ошибки)

### Task 8: Проверить переподключение VPN и сценарий «Cisco уже подключен»

**Files:** —

- [ ] во время работы утилиты вручную: `sudo /opt/cisco/secureclient/bin/vpn -s disconnect`
- [ ] подождать 10-15с, утилита должна переконнектиться сама, `LAN ● OK` должен снова появиться через ≤5с после VPN, curl с LAN снова работает
- [ ] отдельный сценарий: вручную сначала `sudo /opt/cisco/secureclient/bin/vpn -s connect <profile>`, **потом** запустить утилиту. В логах должен быть `LAN detected` через fallback-путь (RFC1918 scan), `LAN ● OK` всё равно поднимается, curl с LAN работает.

### Task 9: Verify acceptance criteria

- [ ] из LAN curl через SOCKS5 → success при свежем запуске утилиты
- [ ] из LAN curl через SOCKS5 → success когда Cisco был подключен до запуска утилиты
- [ ] `route -n get <lan-ip>` показывает физический интерфейс при активном VPN
- [ ] graceful shutdown очищает /24
- [ ] переподключение VPN не ломает LAN-доступ дольше ~5с (один supervisor-тик)
- [ ] нет бесконечного warn-loop'а (idempotent Add работает)
- [ ] `go build ./...` зелёный
- [ ] `golangci-lint run` без новых замечаний (если установлен локально)

### Task 10: Обновить README и переместить план

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md` (если по ходу всплыли новые паттерны)
- Move: `docs/plans/20260529-cisco-lan-route-restore.md` → `docs/plans/completed/`

- [ ] в README в разделе Run — один абзац о том, что утилита автоматически восстанавливает маршрут до LAN-подсети после Cisco-коннекта (без implementation details про /16 hijack)
- [ ] в CLAUDE.md в Architecture-секцию `startCisco` — добавить упоминание этапа LAN-restore
- [ ] `mkdir -p docs/plans/completed && git mv docs/plans/20260529-cisco-lan-route-restore.md docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention or external systems — informational only.*

**Manual verification**:
- Подключение к разным LAN-сетям (домашняя, офис, тетеринг) — убедиться, что автодетект всегда находит корректный интерфейс. На тетеринг через USB IPv4 может оказаться в `172.16/12` — проверить, что `scanRFC1918` его подхватит.
- Долгий прогон (несколько часов) — мониторить, не реапплаит ли периодически `LAN route missing, reinstalling`. Если реапплаит часто (>1 раза в минуту) — Cisco-агент активно борется за свой /16, и стоит подумать про более частый тик или route-monitor сокет (`route -n monitor`).
- Проверка на машинах с проводным Ethernet вместо Wi-Fi — что детектится не `en0`, а активный физический.

**Known limitations**:
- Если процесс убит SIGKILL — `/24`-маршрут на en0 остаётся. Безвреден; вытесняется при следующей смене сети.
- IPv6 не патчится (текущая утилита IPv4-only).

**External system updates**: нет — изменения локальны.
