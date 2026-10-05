## Часть 0
Сервис написан на языке Go
## Часть 1
Запуск своего сервиса, результат `/health`

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ ./api
2026/09/26 16:53:44 listening on :8080 (pid=41839, uid=1000)
2026/09/26 16:53:46 /health ok
```

## Часть 2 - namespaces

Процесс сначала помещён в свои namespaces через `unshare`, а проверка изоляции делается через `nsenter`: он подключается к уже созданным namespaces процесса и запускает команду уже «внутри» них.

### Изнутри процесс - PID 1, чужих процессов не видит

Подключаемся к pid/mount-namespaces процесса и смотрим список процессов изнутри:

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ PID=$(pgrep -x api)
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo nsenter -t $PID -p -m -- ps -e -o pid,ppid,comm
    PID    PPID COMMAND
      1       0 api
     14       0 ps
```

`api` виден как **PID 1** (ppid 0), а кроме него и текущего `ps` в namespace никого нет - сервис не видит системные процессы хоста. Это свой pid-namespace плюс свежий `/proc` (mount-namespace): именно поэтому `ps` показывает только процессы внутри.

### Своё имя хоста

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ hostname
docker-lab
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo nsenter -t $PID -u -- hostname
sandbox
```

Снаружи хост зовётся `docker-lab`, а внутри процесса - `sandbox`. Это uts-namespace: он изолирует hostname и domain от остальной системы.

### Root внутри - непривилегированный пользователь снаружи

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ ps -o pid,uid,user,comm -p $PID
    PID   UID USER     COMMAND
  19559  1000 bob      api
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo nsenter -t $PID -U -- id
uid=0(root) gid=0(root) groups=0(root)
```

На хосте процесс принадлежит пользователю `bob` (uid 1000), а внутри user-namespace видит себя `root`. Это суть user namespace: `--map-root-user` отображает uid 0 внутри namespace на непривилегированного пользователя хоста, поэтому «root внутри» не даёт привилегий снаружи.

### Что изолирует каждый namespace

| namespace | флаг `unshare` | что изолирует |
|---|---|---|
| PID | `--pid` | номера процессов: внутри свой набор PID |
| Mount | `--mount` (+`--mount-proc`) | точки монтирования и вид файловой системы |
| Network | `--net` | сетевой стек: интерфейсы, маршруты, iptables |
| UTS | `--uts` | hostname и domain |
| IPC | `--ipc` | объекты межпроцессного взаимодействия (очереди, семафоры) |
| User | `--user` (+`--map-root-user`) | отображение UID/GID пользователей |

## Часть 3 - cgroups

Цель - навесить на процесс лимиты cgroup v2 и проверить каждый через `api`. Лимиты заданы `mydocker.sh`: `memory.max = 100M`, `cpu.max = 50000/100000`, `pids.max = 15`.

### Память - поймать OOM

Запускаем под лимитами; сервис поднимается (`pid=1` внутри namespace), но при превышении потолка памяти OOM-killer его убивает - в отчёт приходит `Убито`:

```shell
bash mydocker.sh 
2026/09/26 13:02:43 listening on :8080 (pid=1, uid=0)
mydocker.sh: строка 41: 19558 Убито   unshare --pid ... -- ./sandbox "$1"
```

Дёргаем `/eat?mb=150` - просим выделить 150 MB при потолке 100 MB (через `nsenter -n` заходим в сетевой namespace процесса, чтобы попасть в его loopback):

```shell
bob@docker-lab:~$ sudo nsenter -t $PID -n -- curl "http://localhost:8080/eat?mb=150"
curl: (52) Empty reply from server
```

`curl: Empty reply` - процесс убит OOM-killer'ом при попытке выделить больше лимита, соединение оборвалось. Это то же самое `OOMKilled`, что Kubernetes показывает для контейнера, исчерпавшего `limit` памяти.

### CPU - найти throttling

`/burn` нагружает одно ядро в бесконечном цикле, а лимит `cpu.max = 50000/100000` разрешает только половину ядра. После нагрузки читаем статистику cgroup:

```shell
bob@docker-lab:~$ sudo nsenter -t $PID -n -- curl "http://localhost:8080/burn"
bob@docker-lab:~$ cat "$CG/cpu.stat"
usage_usec 26744107
user_usec 26548010
system_usec 196096
nice_usec 0
core_sched.force_idle_usec 0
nr_periods 536
nr_throttled 533
throttled_usec 83144685
nr_bursts 0
burst_usec 0
```

`nr_periods 536` - сколько плановых интервалов прошло, `nr_throttled 533` - почти в каждом периоде процесс упирался в лимит и принудительно приостанавливался (`throttled_usec ~83 сек` суммарно). Это и есть throttling: процесс хотел больше CPU, чем разрешает лимит, поэтому ядро тормозило его до честной доли.

### Процессы - форк-бомба

`pids.max = 15` ограничивает число задач в cgroup. Форк-бомба - `stress-ng --fork 20` порождает по 20 процессов:

```shell
unshare \
    --pid --fork --mount --mount-proc --net --uts --ipc --user --map-root-user \
    -- /bin/bash -c '
        stress-ng --temp-path /tmp --fork 20 --timeout 10s
        "$HOSTNAME_IN" "$API_BIN"
    '
```

```shell
bob@docker-lab:~$ bash mydocker.sh 
stress-ng: info:  [2] setting to a 10 secs run per stressor
stress-ng: info:  [2] dispatching hogs: 20 fork
^Cstress-ng: info:  [2] stopping 11 stressors
stress-ng: info:  [2] skipped: 0
stress-ng: info:  [2] passed: 11: fork (11)
stress-ng: info:  [2] failed: 0
stress-ng: info:  [2] successful run completed in 56.39 secs
/bin/bash: строка 3: : command not found
```

Запуск расплодил меньше, чем просил stressor, и был прерван (`^C`, «stopping») - ядро перестало давать форкаться сверх `pids.max`, расползание процессов остановлено. Финальная ошибка `command not found` - артефакт: после остановки stress-ng контрольный процесс вёл себя не по-ожидаемому, но главное видно: бесконечного расплода не случилось.

## Часть 4 - права

Цель - оставить процессу минимум прав: сбросить лишние capabilities и ограничить системные вызовы seccomp-профилем, показав на деле, что привилегированные действия перестают проходить.

### Capabilities - сброшены, привилегированное действие отклонено

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo setpriv \
--inh-caps=-all \
--ambient-caps=-all \
--bounding-set=-all \
--no-new-privs \
-- ./api
2026/09/26 13:22:26 listening on :8080 (pid=39313, uid=0)
2026/09/26 13:22:42 /set-time: operation not permitted
```

`setpriv` снимает у процесса все capabilities (наследуемые, ambient и bounding set) и выставляет no-new-privileges. Сервис поднимается, но смена системного времени - привилегированное действие - выдаёт `operation not permitted`. Capabilities - это дробление привилегий root'а на отдельные «права»: без нужной capability (тут - той, что позволяет менять время) действие не проходит, хотя процесс формально root.

### Seccomp - заблокированный системный вызов отклоняется

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ ./sandbox ./api
2026/09/26 13:23:41 listening on :8080 (pid=39573, uid=1000)
2026/09/26 13:23:44 /uname: uname failed: operation not permitted
```

`./sandbox` - подготовленная программа-обёртка (`sandbox.c`), которая вешает на процесс seccomp-фильтр: он разрешает всё, кроме системного вызова `uname`. Сервис работает, но вызов `uname` (например, получить имя/версию ядра) отклоняется с `operation not permitted`. В отличие от capabilities, seccomp фильтрует сами системные вызовы на пути к ядру - он ограничивает не «привилегии процесса», а то, какие запросы к ядру процессу вообще разрешены.

## Часть 5 - Собери свой Docker

Собранный скрипт `mydocker.sh` запускает `api` одной командой - в своих namespaces, с лимитами cgroup и урезанными правами. Сервис поднимается и отвечает:

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ bash mydocker.sh
2026/09/26 13:24:33 listening on :8080 (pid=1, uid=0)
2026/09/26 13:25:06 /eat ok
```

Запускаем тот же сервис через настоящий `docker run`, задав флаги, эквивалентные нашему скрипту:

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker run --rm \
--name=lab1 \
--hostname=sandbox \
--memory=100m \
--memory-swap=100m \
--publish=8081:8080 \
--cpus=0.5 \
--cap-drop=ALL \
--security-opt=no-new-privileges \
--pids-limit=15 \
--mount type=bind,source="$PWD/api",target=/api \
ubuntu:26.04 \
/api
2026/09/26 13:54:27 listening on :8080 (pid=1, uid=0)
```

### Что совпадает

Основные ограничения из собственного скрипта в Docker задаются напрямую:

| Ограничение / механизм | mydocker.sh | Docker |
|---|---|---|
| Ограничение памяти | `memory.max = 100M` | `--memory=100m` |
| Ограничение swap | `memory.swap.max = 0` | `--memory-swap=100m` |
| Ограничение CPU | `50000 / 100000 = 0.5 CPU` | `--cpus=0.5` |
| Ограничение процессов | `pids.max = 15` | `--pids-limit=15` |
| Capabilities | `setpriv` убирает capabilities | `--cap-drop=ALL` |
| No New Privileges | `--no-new-privs` | `--security-opt=no-new-privileges` |
| PID namespace | `unshare --pid` | Docker создаёт автоматически |
| Network namespace | `unshare --net` | Docker создаёт автоматически |
| UTS namespace | `unshare --uts` | Docker создаёт автоматически |
| IPC namespace | `unshare --ipc` | Docker создаёт автоматически |
| Mount namespace | `unshare --mount` | Docker создаёт автоматически |
| Отдельный hostname | `hostname sandbox` | `--hostname=sandbox` |
| Seccomp | у меня это `./sandbox` | у Docker по умолчанию встроен |

Таким образом, основные ограничения из собственного скрипта можно задать в Docker практически напрямую.

### Чего нет в mydocker.sh

Docker предоставляет несколько возможностей, которых в собственном скрипте нет или которые там не настроены.

1. **Container filesystem.** Docker автоматически создаёт файловую систему контейнера на основе образа `ubuntu:26.04`; в нашем случае `api` дополнительно подключается через bind mount `./api -> /api`. В `mydocker.sh` отдельного root filesystem из Docker-образа нет: команда запускается непосредственно в существующей системе.
2. **Управление сетью и публикация портов.** Docker автоматически создаёт контейнерную сеть и позволяет опубликовать порт `--publish=8081:8080` - поэтому `api` внутри контейнера слушает `8080`, а с хоста к нему можно обратиться `curl http://localhost:8081/health`. В `mydocker.sh` создаётся только network namespace (`unshare --net`), но настройка NAT и публикации порта не выполняется.

## Часть 6 - Образы

`mydocker.sh` запускает процесс в namespaces, но у него нет готовой файловой системы - её как раз и даёт образ. Проверяем многоступенчатую сборку, минимальную базу и жизненный цикл слоёв.

### Сборка двух образов

Собираем два варианта: `api:full` на полноценном golang-образе и многоступенчатый `api:scratch`, у которого финальный слой - пустой образ `scratch`:

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker build -f Dockerfile.full -t api:full .
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker build -t api:scratch .
```

### Размер

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker images api
IMAGE         ID             DISK USAGE   CONTENT SIZE   EXTRA
api:full      d3927440ca13       1.43GB          343MB        
api:scratch   b7162342181a       13.2MB         4.76MB
```

Разница огромная: `full` несёт весь golang-тулкит (под ~343 MB содержимого), а `scratch` - только сам скомпилированный бинарник (~4.76 MB). Это и есть смысл multi-stage: собрали в тяжёлом builder-образе, а в рантайм положили лишь результат сборки.

### Число слоёв

У `api:full` слоёв много - начиная с базовых слоёв golang-образа (apt-get, Go toolchain, ~650 MB одних только `RUN apt-get`) до наших шагов сборки:

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker history api:full
IMAGE          CREATED              CREATED BY                                      SIZE      COMMENT
d3927440ca13   About a minute ago   /bin/sh -c #(nop)  ENTRYPOINT ["/api"]          0B        
e881ee4cb960   About a minute ago   /bin/sh -c #(nop)  EXPOSE 8080                  0B        
5cc3d115ff9c   About a minute ago   /bin/sh -c CGO_ENABLED=0 GOOS=linux GOARCH=a…   104MB     
0e1c700b4e5a   About a minute ago   /bin/sh -c #(nop) COPY file:8fb2dd82a3f94934…   12.3kB    
5a7bde7ca1a8   About a minute ago   /bin/sh -c go mod download                      13.3MB    
1910cdde434c   About a minute ago   /bin/sh -c #(nop) COPY multi:4ee7add1d51cfc8…   16.4kB    
bdc25ff53e73   About a minute ago   /bin/sh -c #(nop) WORKDIR /src                  8.19kB    
6c2a5538f964   7 days ago           WORKDIR /go                                     4.1kB     buildkit.dockerfile.v0
<missing>      7 days ago           RUN /bin/sh -c mkdir -p "$GOPATH/src" "$GOPA…   16.4kB    buildkit.dockerfile.v0
<missing>      7 days ago           COPY /target/ / # buildkit                      282MB     buildkit.dockerfile.v0
<missing>      7 days ago           ENV PATH=/go/bin:/usr/local/go/bin:/usr/loca…   0B        buildkit.dockerfile.v0
<missing>      7 days ago           ENV GOPATH=/go                                  0B        buildkit.dockerfile.v0
<missing>      7 days ago           ENV GOTOOLCHAIN=local                           0B        buildkit.dockerfile.v0
<missing>      7 days ago           ENV GOLANG_VERSION=1.26.8                       0B        buildkit.dockerfile.v0
<missing>      7 days ago           RUN /bin/sh -c set -eux;  apt-get update;  a…   287MB     buildkit.dockerfile.v0
<missing>      7 days ago           RUN /bin/sh -c set -eux;  apt-get update;  a…   202MB     buildkit.dockerfile.v0
<missing>      7 days ago           RUN /bin/sh -c set -eux;  apt-get update;  a…   65MB      buildkit.dockerfile.v0
<missing>      8 days ago           # debian.sh --arch 'amd64' out/ 'trixie' '@1…   134MB     debuerreotype 0.17
```

У `api:scratch` же всего три слоя: COPY бинарника + EXPOSE + ENTRYPOINT :

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker history api:scratch
IMAGE          CREATED          CREATED BY                                      SIZE      COMMENT
b7162342181a   45 seconds ago   /bin/sh -c #(nop)  ENTRYPOINT ["/api"]          0B        
bd87d4c08988   46 seconds ago   /bin/sh -c #(nop)  EXPOSE 8080                  0B        
109e7d1a772e   46 seconds ago   /bin/sh -c #(nop) COPY file:22c3e9a0a4856eca…   8.47MB
```

### Переиспользование кэша

Повторная сборка `api:scratch`: шаги, чей контекст не изменился (`FROM`, `WORKDIR`, `COPY go.mod`, `go mod download`, `go build`), берутся из кэша состояний - почти весь лог помечен `Using cache`, и образ собирается заново только по факту новых слоёв (`FROM scratch` и COPY бинарника):

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker build -t api:scratch .

Sending build context to Docker daemon  8.613MB
Step 1/10 : FROM golang:1.26 AS builder
---> 6c2a5538f964
Step 2/10 : WORKDIR /src
---> Using cache
---> 4b0096d03018
Step 3/10 : COPY go.mod go.sum ./
---> Using cache
---> 2e729c4e0cdd
Step 4/10 : RUN go mod download
---> Using cache
---> 099199f0bcc6
Step 5/10 : COPY ./src/main.go main.go
---> Using cache
---> 67d188984c3f
Step 6/10 : RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /api .
---> Using cache
---> b0e904df6eb6
Step 7/10 : FROM scratch
--->
Step 8/10 : COPY --from=builder /api /api
---> Using cache
---> 109e7d1a772e
Step 9/10 : EXPOSE 8080
---> Using cache
---> bd87d4c08988
Step 10/10 : ENTRYPOINT ["/api"]
---> Using cache
---> b7162342181a
Successfully built b7162342181a
Successfully tagged api:scratch
```

### Файл внутри контейнера не переживает пересоздание

Кладём файл в контейнер, пересоздаём контейнер и смотрим, что файл исчез - всё, что пишется в рантайме, живёт в «писавшем» слое, который умирает вместе с контейнером:

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker run -d --name test-scratch api:scratch
ba69df4c4185aed36493131d7d639e714c491c4f58a5237b4cc2db33234736fb
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker cp lab.md test-scratch:/lab.md
Successfully copied 9.73kB to test-scratch:/lab.md
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker diff test-scratch
A /lab.md
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker rm -f test-scratch
test-scratch
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker run -d --name test-scratch api:scratch
bf8b3bf47ad4661321a077190eec5499cb90865584f6ad3d190eeaae6c7d6c5f
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker diff test-scratch
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$
```

`docker diff` после первого запуска показывал `A /lab.md` (файл добавлен - это изменения в слое контейнера), а после пересоздания - пусто: записанное в рантайме пропало вместе с контейнером.

> Часть про «повтори с томом - файл остался» пока не покрыта командами. Добавь запуск с `--volume`, запиши файл, пересоздай контейнер и покажи, что файл на месте - тогда впишу сюда раздел про тома (persistent storage за пределами слоёв контейнера).

## Часть 7 - Когда контейнера мало

Один и тот же образ `api:scratch` запускаем двумя рантаймами - обычным Docker (по умолчанию runc) и через gVisor (`--runtime=runsc`) - и смотрим, что приложение видит через `/uname`:

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker run --rm --runtime=runsc -p 8081:8080  api:scratch
2026/09/26 16:49:04 listening on :8080 (pid=1, uid=0)
2026/09/26 16:49:07 /uname: uname succeeded: Domainname= Machine=x86_64 Nodename=86be360c649a Release=4.19.0-gvisor Sysname=Linux Version=#1 SMP Sun Jan 10 15:06:54 PST 2016
```

```shell
bob@docker-lab:~/itmo/itmo-devops/lecture-1-docker$ sudo docker run --rm -p 8080:8080  api:scratch
2026/09/26 16:50:01 listening on :8080 (pid=1, uid=0)
2026/09/26 16:50:04 /uname: uname succeeded: Domainname=(none) Machine=x86_64 Nodename=a364ed8d10da Release=7.0.0-34-generic Sysname=Linux Version=#34-Ubuntu SMP PREEMPT_DYNAMIC Wed Sep  2 14:29:37 UTC 2026
```

### Сравнение изоляции

Для проверки использовался один и тот же образ `api:scratch`, запущенный обычным Docker (runc) и через gVisor (runsc). При вызове `/uname` обычный контейнер получил информацию о ядре хоста: `Release=7.0.0-34-generic`; при запуске через gVisor: `Release=4.19.0-gvisor`.

Это показывает основное отличие: gVisor предоставляет собственный userspace-слой, который реализует интерфейс Linux kernel и находится между приложением и ядром хоста. Обычный Docker использует namespaces, cgroups, capabilities и другие механизмы Linux для изоляции, но системные вызовы в конечном итоге обрабатываются общим ядром хоста. gVisor добавляет дополнительную границу изоляции: приложение взаимодействует с ядром через gVisor (Sentry), поэтому прямое взаимодействие контейнера с host kernel уменьшается. Поэтому gVisor считают более изолированным вариантом контейнеризации.

### Что общего с хостом

У обычного контейнера в любом случае остаётся общим ядро Linux. Контейнер не имеет собственного ядра, в отличие от виртуальной машины. Namespaces изолируют процессы, сеть, файловую систему и другие ресурсы, но не создают отдельную операционную систему. Именно общее ядро является фундаментальным пределом изоляции обычного контейнера: уязвимость в механизмах ядра или доступ к незащищённому kernel-интерфейсу потенциально может затронуть весь хост.

### Вывод

Обычный Docker обеспечивает изоляцию средствами самого Linux kernel. gVisor добавляет между контейнером и host kernel дополнительный слой, поэтому предоставляет более сильную границу изоляции при сохранении контейнерной модели.

