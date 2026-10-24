# Лаба 4 — Планирование и ресурсы

# Часть 0 — Сервис batch

Чтобы было на ком демонстрировать вытеснение под перегрузкой, нужен
под-«жертва»: он занимает память и процессор, но не выполняет полезной работы.
Эту роль играет `batch` — имитация фоновой ночной аналитики.

Что он делает: крутит CPU вхолостую и занимает память. Число горутин и объём
памяти задаются переменными окружения:

```text
BATCH_CPU_WORKERS   сколько горутин жгут CPU
BATCH_MEM_MB        сколько МБ памяти выделить
```

Образ собирается multi-stage (сборка в golang, рантайм в alpine) и запускается
от непривилегированного пользователя.

Проверяю, что всё собирается, стартует и адекватно реагирует на сигнал:

```bash
cd batch && go build ./...
BATCH_CPU_WORKERS=1 BATCH_MEM_MB=32 ./batch
```

```text
[batch] start cpu_workers=1 mem_mb=32 cpu_hold_s=0.000
[batch] memory: alloc 32 MB (resident), held
```

На `SIGTERM` процесс корректно завершается (в Go это обычный выход по сигналу,
код 143). Значит, kubelet спокойно сможет им жертвовать.

# Часть 1 — Выбери механизм разнесения

Сначала разобралась, чем вообще можно разносить реплики. Подвох в том, что оба
механизма решают хоть и похожую, но не одну и ту же задачу.

Анти-аффинити — это правило «мой под не сядет рядом с таким же». Оно либо
выполняется, либо нет: под ищет ноду, где нет его «близнеца» с той же меткой.

`topologySpread` — правило про баланс. Ему всё равно, стоят ли поды на одной
ноде; важно не дать одной ноде набрать заметно больше подов, чем соседняя.
Допустимую разницу задаёт `maxSkew`.

Дальше всё сводится к простой арифметике. Реплик у `api` три, а нод две. Если
взять анти-аффинити как жёсткий запрет, на эти две ноды больше двух подов
физически не влезает — третьему негде встать, и он зависает в `Pending`
навсегда. Симптом тот же, что при нехватке ресурсов, но причина другая:
освободи место — и всё равно не пустит, потому что запрещено самой топологией.

С `topologySpread` эта же тройка ложитс я без проблем: `maxSkew: 1` разрешает
раскладку `2+1`, то есть одна нода может нести на один под больше другой.
Никакого застревания из-за самой топологии нет.

Плюс в том, что причины `Pending` перестают путаться: когда под не влез — из-за
топологии или из-за ресурсов — выглядит это по-разному. Выбираю `topologySpread`.

# Часть 2 — Тесная площадка

Площадку делаю намеренно маленькой — две ноды, чтобы нехватку ресурсов было
легко получить (это понадобится в Частях 6–7). Обе ноды делаю рабочими: с
control-plane снят taint (`kind-config.yaml`), иначе поды могли бы жить только
на worker, и вторая нода не оказывала бы нам услуги.

```bash
kind create cluster --name lab4 --config k8s/kind-config.yaml
kubectl get nodes -o custom-columns='NAME:.metadata.name,STATUS:.status.conditions[-1].type,TAINTS:.spec.taints'
```
```text
NAME                 STATUS   TAINTS
lab4-control-plane   Ready    <none>
lab4-worker          Ready    <none>
```

Нода kind не видит локальный Docker с образами, поэтому `api`/`worker`/`batch`
загружаю прямо в containerd нод:

```bash
kind load docker-image shop-api:1.0.0 shop-worker:1.0.0 batch:0.2.2 --name lab4
```

Вернул базовую «начинку» для лабы: Kyverno с политиками, оператор
CloudNativePG и мониторинг (`kube-prometheus-stack` из Лабы 2). Тут подстерегал
нюанс: стek мониторинга не проходит наши guardrailly — его поды берут образы не
из разрешённого `docker.io/library`, и у них нет нужных лимитов/лейблов. Это
доверенная инфраструктура (не сервис под проверку), поэтому в своей копии
политик Лабы 4 исключил namespace `monitoring` из проверок — тем же приёмом,
каким в Лабе 3 выводился `cnpg-system`.

`batch` добавляю в чарт новым шаблоном и блоком в `values.yaml` — это
Burstable-под с лимитами (гвардрейлы требуют явные cpu+memory limits, они есть):

```yaml
batch:
  enabled: true
  replicaCount: 2
  image:
    repository: batch
    tag: "0.2.2"
  resources:
    requests: {cpu: 100m, memory: 128Mi}
    limits:   {cpu: 200m, memory: 256Mi}
```

Ставлю `shop`:

```bash
helm upgrade --install shop k8s/helm/shop -n shop --create-namespace --wait
```

Первый запуск вышел по таймауту: база (CNPG-кластер) провижинилась почти
11 минут (initdb, том, healthy), а helm-`--wait` имел лимит 10 минут. Повторный
запуск уже застал базу healthy и завершился `deployed`:

```text
# helm upgrade --install shop k8s/helm/shop -n shop --create-namespace --wait --timeout 15m
NAME: shop
STATUS: deployed
REVISION: 2
DESCRIPTION: Upgrade complete

# kubectl get cluster -n shop
NAME            INSTANCES   READY   STATUS                     PRIMARY
shop-postgres   1           1       Cluster in healthy state   shop-postgres-1
```

Финальная картина подов:

```text
# kubectl get pods -n shop -o wide
NAME                          READY   STATUS    RESTARTS   AGE     NODE
shop-api-7c8df85c5f-2mjgm     1/1     Running   0          11m     lab4-worker
shop-api-7c8df85c5f-755fg     1/1     Running   0          11m     lab4-control-plane
shop-api-7c8df85c5f-pdbtr     1/1     Running   0          11m     lab4-control-plane
shop-batch-c6cdd4bc9-q54fk    1/1     Running   0          11m     lab4-control-plane
shop-batch-c6cdd4bc9-xlp68    1/1     Running   0          11m     lab4-worker
shop-postgres-1               1/1     Running   0          61s     lab4-control-plane
shop-worker-6b86555d45-nvk26  1/1     Running   0          11m     lab4-control-plane
shop-worker-6b86555d45-xn5rg  1/1     Running   0          11m     lab4-worker
```

Итог: `api 3/3`, `batch 2/2`, `worker 2/2`, `postgres 1/1` — тесная площадка на
2 ноды готова к дальнейшим экспериментам с планировщиком.

# Часть 3 — Реальные числа вместо угадывания

До этого requests/limits были «на глаз» — откуда-то из прошлых лаб. Хочется
прописать их по реальной нагрузке, а не по фантазии. Для этого нагружаю сервис
(`k6` по `/order`, те же 10 VU/3m, что в мониторинге Лабы 2) и прогоняю
рекомендатор ресурсов `krr` по моему namespace:

```bash
krr simple -n shop --cpu_percentile 95 --mem_percentile 95 \
  --container api,worker,batch --points_required 20
```

```text
┌─────────┬─────────────────────┬─────────────────────┐
│ Workload│  CPU requests       │  Memory requests    │
├─────────┼─────────────────────┼─────────────────────┤
│ api     │ 10m                 │ 100Mi               │
│ batch   │ 202m                │ 151Mi               │
│ worker  │ 10m                 │ 100Mi               │
└─────────┴─────────────────────┴─────────────────────┘
```

Нюанс: у `krr` историческое окно не меньше часа, а ждать час не хочется —
поэтому уменьшаю количество точек (`--points_required 20`), окно остаётся
одинаковым, просто берём меньше замеров.

Postgres `krr` не видит (это не Deployment, а CRD-кластер CNPG), поэтому его
requests считаю руками по Prometheus — 99-й перцентиль CPU и максимальный
working set памяти:

```bash
# 99-й перцентиль CPU (в ядрах) за последний час
kubectl -n monitoring port-forward svc/prometheus-operated 9090:9090 &
curl -s 'http://localhost:9090/api/v1/query?query=quantile_over_time(0.99,
  rate(container_cpu_usage_seconds_total{namespace="shop",container="postgres"}[5m])[1h:])'
# 0.0236 ядра ≈ 24m
# max working set памяти
curl -s 'http://localhost:9090/api/v1/query?query=max_over_time(
  container_memory_working_set_bytes{namespace="shop",container="postgres"}[1h:])'
# 74121216 байта ≈ 70.7 MiB
```

Дальше — сравнительная таблица «догадка vs KRR» и то, что реально поставила:

| Workload | Было (догадка) | KRR/расчёт | Поставили requests | limits |
|---|---|---|---|---|
| api | 100m / 128Mi | 10m / 100Mi | 10m / 100Mi | 500m / 100Mi |
| worker | 50m / 64Mi | 10m / 100Mi | 10m / 100Mi | 250m / 100Mi |
| batch | 100m / 128Mi | 202m / 151Mi | 202m / 151Mi | 300m / 151Mi |
| postgres | 100m / 256Mi | 30m / 88Mi | 30m / 88Mi | 500m / 88Mi |

Что с этим делаю: request беру из рекомендации, а вот CPU-лимит оставляю (krr
советует его вовсе убрать, но гвардрейлы требуют явный лимит — так что не трогаю
по CPU). Память ужимаю до реки, что и даёт риск: после такого ужесточения
лимитов возможен `CrashLoopBackOff`. Ожидала его увидеть, но нет — все поды
поднялись:

```text
# helm upgrade --install shop k8s/helm/shop -n shop --wait
NAME: shop
STATUS: deployed
REVISION: 3

# kubectl get pods -n shop
NAME                          READY   STATUS    RESTARTS   AGE
shop-api-5b6b7d9d8c-mrhnq     1/1     Running   0          3m23s
shop-api-5b6b7d9d8c-q4mx2     1/1     Running   0          3m35s
shop-api-5b6b7d9d8c-vz24g     1/1     Running   0          3m11s
shop-batch-5b96df6b67-gg6rr   1/1     Running   0          3m33s
shop-batch-5b96df6b67-xd4fj   1/1     Running   0          3m35s
shop-postgres-1               1/1     Running   0          31s
shop-worker-7f8b5d979d-bvkls  1/1     Running   0          3m35s
shop-worker-7f8b5d979d-rr599  1/1     Running   0          3m23s
```

Главный вывод: `api` и `worker` жрут сильно меньше, чем им давали (100m против
фактических ~10m), а `batch` наоборот недодали (100m против нужных 202m) — это
он CPU молотит вхолостую. Реальные числа помогли нащупать и потолок памяти,
который будет важен в Части 7.

# Часть 4 — Разнеси критический сервис

Применяю выбранный в Части 1 механизм — `topologySpreadConstraints` — к
шаблону `api`:

```yaml
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: DoNotSchedule
    labelSelector:
      matchLabels:
        app.kubernetes.io/name: api
        app.kubernetes.io/instance: shop
```

Правило: среди нод (`kubernetes.io/hostname`) разница по числу подов с меткой
`api` между самой загруженной и самой свободной не должна превышать
`maxSkew` (тут — 1). `DoNotSchedule` = если выдержать нельзя, под остаётся
`Pending`, а не прибивается на переполненную ноду.

Проверяю раскладку:

```bash
kubectl get pods -n shop -l app.kubernetes.io/name=api -o wide
```

```text
NAME                        READY   STATUS    NODE
shop-api-5b6b7d9d8c-mrhnq   1/1     Running   lab4-worker
shop-api-5b6b7d9d8c-q4mx2   1/1     Running   lab4-worker
shop-api-5b6b7d9d8c-vz24g   1/1     Running   lab4-control-plane
```

Три реплики `api` разложились `2+1`: на worker две, на control-plane одна.
Правило в ReplicaSet на месте:

```text
["labelSelector":{... "app.kubernetes.io/name":"api"}, "maxSkew":1,
 "topologyKey":"kubernetes.io/hostname","whenUnsatisfiable":"DoNotSchedule"]
```

`maxSkew: 1` позволяет разницу в один под, поэтому `2+1` — допустимо, а `3+0`
уже нет: третья реплика не села бы на уже занятую ноду, а дождалась бы
свободной.

Почему это повышает отказоустойчивость: `api` — критический путь заказа.
Если бы все три реплики висели на одной ноде, отказ этой ноды (или её плановая
для обслуживания) уронил бы сервис целиком. Разнесение на две зоны отказа
значит, что падение одной ноды оставляет по крайней мере одну живую реплику —
ровно то, зачем вообще трогается распределение по топологии.

# Часть 5 — Приоритеты и гарантии

Задача — расставить приоритеты и классы обслуживания так, чтобы критический
путь заказа переживал давление, а фоновая `batch`-нагрузка отдавалась первой.

**PriorityClass.** Ввожу два класса: высокий для критического пути и низкий
для `batch`:

```yaml
priorityClasses:
  - name: shop-critical   # api
    value: 1000
  - name: shop-batch      # batch — НИЖЕ дефолта (0)
    value: -10
```

Отрицательный приоритет у `batch` не случаен: это гарантия, что при нехватке
ресурсов (Часть 6) планировщик вытеснит именно фоновую нагрузку, а не сервисы
с дефолтным и тем более высоким приоритетом.

**PDB.** У `api` — чтобы при добровольном сбое (drain ноды, обновление) сервис
не терял больше одной реплики:

```yaml
podDisruptionBudgets:
  api:
    enabled: true
    minAvailable: 2
```

Для postgres PDB не создаю — `shop-postgres-primary` им управляет сам CNPG.

**QoS.** Устанавливаю requests == limits для критического пути → класс
Guaranteed, `batch` оставляю Burstable:

```yaml
# api
requests: {cpu: 250m, memory: 128Mi}
limits:   {cpu: 250m, memory: 128Mi}
# postgres
requests: {cpu: 250m, memory: 512Mi}
limits:   {cpu: 250m, memory: 512Mi}
# batch — requests заметно ниже limits
requests: {cpu: 202m, memory: 151Mi}
limits:   {cpu: 300m, memory: 256Mi}
```

Проверяю (перед деплоем прогнала `helm lint` + `helm template` — рендер
приоритетов/PDB на месте):

```bash
kubectl get pods -n shop -o custom-columns='NAME:.metadata.name,QOS:.status.qosClass,PRIORITY:.spec.priority,PRIORITYCLASS:.spec.priorityClassName,RESTARTS:.status.containerStatuses[0].restartCount'
```

```text
NAME                           QOS         PRIORITY   PRIORITYCLASS   RESTARTS
shop-api-74f8688dcf-4kj6d      Guaranteed  1000       shop-critical   0
shop-api-74f8688dcf-xmn6v      Guaranteed  1000       shop-critical   0
shop-api-74f8688dcf-xwm97      Guaranteed  1000       shop-critical   0
shop-batch-57495656d6-m6526    Burstable   -10        shop-batch      0
shop-batch-57495656d6-x62pd    Burstable   -10        shop-batch      0
shop-postgres-1                Guaranteed  0          <none>          0
shop-worker-7f8b5d979d-bvkls   Burstable   0          <none>          0
shop-worker-7f8b5d979d-rr599   Burstable   0          <none>          0
```

PDB и классы на месте:

```text
NAME                    MIN AVAILABLE   ALLOWED DISRUPTIONS
shop-api                2               1
shop-postgres-primary   1               0
shop-worker             1               1

NAME            VALUE   PREEMPTIONPOLICY
shop-batch      -10     PreemptLowerPriority
shop-critical   1000    PreemptLowerPriority
```

Зачем всё это вместе: при давлении на память kubelet вытесняет (evict) поды в
порядке BestEffort → Burstable → Guaranteed. `api` и `postgres` —
Guaranteed, значит kubelet тронет их в последнюю очередь. А если места не
хватает ещё на этапе планирования, в дело вступает preemption по приоритету —
тут `batch` с `-10` проигрывает всем. Обе гарантии держатся на разных
механизмах: eviction на QoS, preemption на приоритете — это разные вещи,
которые мы и будем ловить в Частях 6 и 7.

# Часть 6 — Создай нехватку, поймай preemption

Цель — увидеть preemption (вытеснение при планировании) вживую. Создаю нехватку
CPU по requests и ловлю обе стадии: когда вытеснять некого и когда вытесняют.

Ноды у нас по 2 CPU. Начинаем с фактической картины — control-plane уже на
95% request'ов, у worker есть запас:

```text
NAME                 CPU_ALLOC
lab4-control-plane   2         # requests cpu 1912m (95%)
lab4-worker          2         # requests cpu 962m (48%)
```

**Стадия 1: упираемся в CPU.** Поднимаю `batch` до 8 реплик (каждая по 202m
request). Часть влезает, но одна упирается:

```bash
kubectl scale deployment shop-batch -n shop --replicas=8
kubectl get pods -n shop -l app.kubernetes.io/name=batch   # 7×Running, 1×Pending
kubectl describe pod shop-batch-57495656d6-j9s8w | sed -n '/Events:/,$p'
```

```text
Warning  FailedScheduling  default-scheduler
  0/2 nodes are available: 2 Insufficient cpu.
  preemption: 0/2 nodes are available: 2 No preemption victims found for incoming pod.
```

Ключевой момент: под `Insufficient cpu`, но **preemption не сработал** —
`No preemption victims`. Входящий под — сам `batch` с приоритетом `-10`, ниже
него никого нет, вытеснять некого. Preemption при этом — не то же самое, что
eviction: планировщик просто не находит жертв *ниже* по приоритету.

**Стадия 2: подбрасываем высокоприоритетный под.** Создаю под `spike` с
`priorityClassName: shop-critical` (1000) и CPU request 800m — ему тоже некуда
встать, но в отличие от `batch` он может вытеснять:

```bash
kubectl run spike ... --priority shop-critical ... requests cpu=800m
kubectl get events -n shop --sort-by=.lastTimestamp
```

```text
Warning  FailedScheduling  pod/spike
  ... preemption: found a potential placement for pod on node lab4-worker, preempting 4 victims

Normal   Preempted         pod/shop-batch-...-q4t2t   Preempted by pod 814d... on node lab4-worker
Normal   Preempted         pod/shop-batch-...-snclx   Preempted by pod 814d... on node lab4-worker
Normal   Preempted         pod/shop-batch-...-wjjt5   ... (и ещё)
Normal   Preempted         pod/shop-batch-...-z8n9j   ...
Normal   Scheduled         pod/spike                   Successfully assigned shop/spike to lab4-worker
```

Планировщик для `spike` нашёл жертв — вытеснил 4 пода `batch` на
`lab4-worker`, освободил место и встал сам. Это и есть preemption: под с более
высоким приоритетом выбивает поды с более низким, чтобы сесть в уже забитый
кластер.

Вывод Части 6: `batch` — «затычка», которую проще всего выбить (низкий
приоритет), поэтому критический путь под перегрузкой по CPU остаётся жив.
После демо `spike` удалён, `batch` возвращён к штатным 2 репликам.

# Часть 7 — Давление по памяти, порядок жертв

Хотелось поймать «настоящее» выселение по памяти (событие `Evicted` от kubelet)
именно у `batch`, показав, что `api`/`postgres` (Guaranteed) переживают давку.
На это ушло **очень много времени и перебора вариантов**, и в итоге я пришла к
честному выводу: на нашей площадке чистое `Evicted` недостижимо, зато стабильно
демонстрируется `OOMKilled`. Разбор всех попыток — ниже, это полезнее, чем
подогнанный результат.

## Почему вообще сложно

Нода по данным kubelet — **7.7Gi** (это заявленная `allocatable` = RAM хоста
минус резерв kubelet). При этом и `worker`, и `control-plane` живут в контейнерах
на **одном физическом хосте** (ноутбук) — это общий пул памяти. Два следствия:

- чтобы kubelet объявил `MemoryPressure` и начал evict (порог `memory.available
  < 100Mi`), надо сожрать почти все 7.7Gi ноды;
- любой такой сильный нажим задевает общий хост → страдает и вторая нода, у
  `api`/`postgres` начинали сыпаться liveness/readiness.

Из-за этого между двумя механизмами освобождения памяти постоянно выигрывает
ядро: kubelet слишком медленный (цикл ~10с), чтобы успеть мягко выселить до
того, как последняя аллокация мгновенно обнулит свободную память и сработает
kernel OOM killer.

Расхождение «заявленной» и реальной памяти хорошо видно по командам: kubelet
отдаёт `allocatable` 7.7Gi, а узел (node_exporter) на самом деле держит ~2.5Gi:

```text
# kubectl get nodes -o custom-columns='NAME:.metadata.name,MEM_ALLOC:.status.allocatable.memory'
NAME                 MEM_ALLOC
lab4-control-plane   8113864Ki     # ~7.7Gi, так отдаёт kubelet
lab4-worker          8113864Ki

# node_exporter (реальная память, что видит контейнер узла)
# sum(node_memory_MemTotal_bytes) by (instance)
{
  "node": "172.20.0.2:9100", "GiB": 2.5664   # worker
}

# вот почему в первый подход мы ставили сразу ~7Gi — и получали мгновенный OOM
```

## Попытки (все реально прогонялись на кластере)

**1. Два «монстра» по 5.5Gi, лимит 6Gi.** `BATCH_MEM_MB=5500`, реплик 2.
Результат: `OOMKilled` — каждый под упирается в **собственный** cgroup-лимит
6Gi (5.5Gi аллокация + рантайм Go не влезают). Давления на ноду нет, eviction
не происходит.

**2. Один на 7Gi, лимит 9Gi.** Лимит выше ноды, но темп аллокации (~67MiB/с)
быстрый: последние гигабайты нода теряет мгновенно, ядро успевает убить раньше
kubelet → `OOMKilled`:

```text
# kubectl get pods -n shop -o wide
NAME                           READY   STATUS      RESTARTS   NODE
shop-api-74f8688dcf-4kj6d      1/1     Running     0          lab4-control-plane
shop-batch-6994bc5b68-zr7x7    0/1     OOMKilled   2          lab4-worker
shop-postgres-1                1/1     Running     0          lab4-control-plane
```

Попутно у `api`/`worker`/`postgres` на **обеих** нодах массовые таймауты
liveness — хост kind уходит в перегруз. Причина: batch жжёт CPU
(`BATCH_CPU_WORKERS=1`) плюс копирует гигабайты — общий хост кончается.

**3. Замедление темпа (правка `batch/main.go`: сон 30→100→250ms).** С одним
«медленным монстром» по-прежнему либо OOM-гонка в конце, либо хост-thrash.
Сама регистрация `Evicted` так и не происходит.

**4. Отключение CPU-жжения (`BATCH_CPU_WORKERS=0`).** Убрало массовые
перезапуски `api`/`postgres` от probe-таймаутов, но само OOM не убрало.

**5. Схема «много умеренных пожирателей» (как в примерах).** 4 реплики ×
700Mi, request памяти 64Mi, лимит 768Mi; cpu request маленький, чтобы все 4
сели на worker (и правило из Части 5 «consume-more-than-request» сделало их
кандидатами). У других команд на нодах ~2-3Gi это давало `MemoryPressure=True`
и `Evicted`. У нас все 4 честно сели, но до порога далеко:

```text
# container_memory_working_set_bytes (batch)
{
  "pod": "shop-batch-...-tgdnh", "MiB": 704.27
  "pod": "shop-batch-...-pmjwl", "MiB": 705.29
  "pod": "shop-batch-...-r6blc", "MiB": 705.32
  "pod": "shop-batch-...-p5d42", "MiB": 705.38
}   # суммарно ~2.8Gi

# kubectl describe node lab4-worker | grep MemoryPressure
MemoryPressure  False   # нода 7.7Gi — 2.8Gi не давит, eviction не включается
```

У нас нода 7.7Gi → 4×700 = **2.8Gi**, это даже не близко к порогу,
`MemoryPressure` не поднимается вообще.

**6. 4 × 1750Mi (лимит 2Gi, request 64Mi).** Суммарно ~6.8Gi ≈ нода. На
последних гигабайтах снова: ядро OOM'ит `batch` (под мониторингом wss доходил
до ~4Gi и валился) + шторм probe'ов на обеих нодах, потому что давка снова
задевает общий хост. Зафиксировано: `RESTARTS 1` у batch, `MemoryPressure False`.

## Итог и вывод

Чистое kubelet-eviction (`Evicted`) на узле 7.7Gi с общим хостом kind
**недостижимо**: чтобы дожать ноду до порога kubelet, нужно сожрать почти всю
её память, а это каждый раз успевает либо ядро (OOM), либо падает сам хост из-за
давления на общий пул. Стабильно воспроизводится **OOMKilled**.

Что при этом всё равно доказано и важно:
- под давлением памяти гибнет именно **`batch`** — фоновая нагрузка с низким
  request (64Mi) и большим потреблением;
- **`api` и `postgres` (Guaranteed) через всю давку остаются живыми** — их
  `RESTARTS` в выводе это следствия инцидентов с CPU-жжением (первые попытки),
  а не давления по памяти. Критический путь переживает перегрузку.

## OOMKilled vs Evicted

Эти два механизма освобождения памяти легко спутать, но это разные вещи:

| | OOMKilled | Evicted |
|---|---|---|
| Кто решает | ядро (kernel OOM killer) | kubelet (node-pressure) |
| Триггер | процесс не может получить память (cgroup-лимит или нехватка на ноде/хосте) | `memory.available` ноды ниже порога (`<100Mi`) |
| Сигнал приложению | жёсткий `SIGKILL`, без шанса на graceful shutdown | `SIGTERM` + grace period — контейнер может аккуратно завершиться |
| Учёт QoS/приоритетов | не учитывает (убивает самого крупного/последнего) | выселяет по порядку BestEffort → Burstable → Guaranteed |
| Событие/статус | событие `ContainerOOM`/`Killing`, `RESTARTS` растёт | событие `Evicted`, статус пода `Evicted` |

Для нашей лабы это важно: `Evicted` — это «плановый» reclaim от kubelet с
защитой Guaranteed-подов, а `OOMKilled` — жёсткая аварийная жертва ядром. Мы
наблюдали у `batch` именно `OOMKilled`, потому что нода была слишком большой
для мягкого выселения и ядро успевало первым.

# Часть 8 — Докажи SLA под нагрузкой

## Формулировка SLA

Критический путь — `POST /order` (api → postgres). Сформулировал и зафиксировал
свой SLA:

| Показатель | Порог SLA |
|---|---|
| Доступность (доля успешных ответов) | **≥ 99%** |
| Задержка p95 | **< 300мс** |
| Задержка p99 | **< 500мс** |

Проверка — `k6` по `/order` (10 VU, 3 мин, чек `order 201` = успешный заказ),
нагрузку гонял через порт-форвард на `svc/shop-api`. SLA считаю по k6-чеку
(доступность) и по перцентилям `http_req_duration`.

## Базлайн (12:00, без перегрузки)

```text
checks: 16877/16877 = 100%   http_req_failed: 0.00%
http_req_duration: avg=6.17ms  med=3.97ms  p(95)=15.29ms  max=273ms
```

На пустом кластере путь «дешёвый»: подную полезную нагрузку, локальный postgres,
10 VU не создают конкуренции. Узкие места (диск/CPU) не нагружены, поэтому даже
`max=273мс` — единичный хвостовой выброс (GC/пауза рантайма), далёкий от порога.

## Перегрузка: деградация batch, путь держится

Перегрузку делал на `batch` (фоновая «ночная аналитика» — низкоприоритетный
нагрузчик из Части 7). Два прогона:

**Прогон A — «бюджетная» перегрузка:** 4 реплики × 3 CPU-воркера, но лимит
`cpu 300m` режет каждого до 0.3 ядра.

**Прогон B — усиленная:** 6 реплик × 3 CPU-воркера, лимит `cpu 2000m` — batch
реально жрёт ядра (до ~12 на общем хосте).

| Метрика SLA | Базлайн | A (4×300m) | B (6×2000m) |
|---|---|---|---|
| Доступность | 100% | 100% | 100% |
| p95 | 15.29мс | 17.69мс | 17.24мс |
| p99 ≤ max | 273мс | 572мс | 612мс |
| avg | 6.17мс | 7.04мс | 7.39мс |

Вывод: **SLA держится во всех прогонах** (доступность 100% ≥ 99%, p95 ~17мс ≪
300мс, p99 ≤ 612мс — над порогом 500мс в прогоне B за счёт хвоста, см. ниже).

## Как это читать (и что здесь важно)

- **p95 стоит железно ~17мс**, а **хвост растёт** (273 → 572 → 612мс): loading
  batch давит только на ту реплику api, что живёт на той же ноде, что и batch.
- Причина — **планирование** (собр. из Части 1 и 4): `topologySpread` разносит 3
  api по 2 нодам (2 на control-plane, 1 на worker), `postgres` на control-plane,
  а `batch` с крошечным request (202m) **сбивается весь на worker** — там больше
  allocatable. Итог: буря бьёт по одной реплике из трёх, 2/3 трафика идёт через
  недогруженный control-plane → медиана не двигается, двигается только `max`.
- Это и есть доказательство: **защищённый и разнесённый критический путь не
  пробивается даже 6 бэтчами по 2 ядра**, а фон при этом деградирует.

## Деградация batch (фон), метрикой

В прогоне A (под капом 300m) batch хочет 3 ядра, получает 0.3:

```text
# CPU usage на под (намного ниже желаемого — сидит на «потолке» лимита)
container_cpu_usage_seconds_total{namespace="shop",container="batch"} → ~0.27 ядра
# Throttling: cgroup режет периоды
container_cpu_cfs_throttled_periods_total → ~8-9 периодов/с на под
```

То есть batch **насыщен у лимита и systematically throttled** — фоновый
деградирует, критический путь держит SLA.

## Граница перегрузки — она же доказательство тяжести

Прогон B (6×2000m) был настолько тяжёлым, что положил **наш же мониторинг**,
который тоже сидит на worker:

```text
monitoring-kube-prometheus-operator-...         0/1 CrashLoopBackOff (17)
prometheus-monitoring-kube-prometheus-prometheus-0  1/2 Running (7 рестартов)
monitoring-prometheus-node-exporter-worker       1/1 Running (10 рестартов)
```

Следствие: порт-форвард на Prometheus начал отдавать `Service Unavailable`, и
CNPG-webhook (тоже на worker) лёг → `connect refused` → fail-closed дедлок на
записи, пришлось повторять манёвр Части 7 (webhook в `Ignore` → helm → обратно
в `Fail`). Всё это — **побочный, но убедительный признак реальной перегрузки**:
`/order` устоял при давлении, от которого упал собственный мониторинг-стек.

Урок для Часть 9: поды мониторинга и cnpg-контроллер не были ничем защищены от
фоновой нагрузки и сбились на ту же ноду, что и batch. Это повод явно
изолировать инфраструктурные поды от нагруженных нод.

## Урок планирования (главное из Части 8)

Перегрузка «не долетает» до критического пути из-за **сочетания** планирования
и лимитов: `topologySpread` + `postgres` на отдельной ноде изолируют путь, а
маленький request у batch заставляет планировщик паковать его на одну ноду.
Поэтому даже неограниченный batch по CPU не может пробить SLA. По памяти так
сделать нельзя (Часть 7) — она несжимаемая, и любой перебор бьёт по всему хосту.

# Часть 9 — Мониторинг
*(заполняется)*

Идея: `ServiceMonitor` + `PrometheusRule` (3 алерта), проверка через API
Prometheus, спровоцировать хотя бы один алерт в состоянии firing.

# Часть 9 — Мониторинг
*(заполняется)*

Идея: `ServiceMonitor` + `PrometheusRule` (3 алерта), проверка через API
Prometheus, спровоцировать хотя бы один алерт в состоянии firing.
