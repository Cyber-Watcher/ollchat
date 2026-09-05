# Образ проверки задач на Go.
#
# Контейнер проверки работает без сети (--network=none), поэтому всё, что нужно
# для сборки и тестов, обязано лежать внутри заранее. Пустой модуль-заготовка
# нужен затем, чтобы `go build` в каталоге с одним файлом не спотыкался
# об отсутствие go.mod, если задача его не приложила.
FROM golang:1.26

ENV GOFLAGS=-mod=mod \
    GOTOOLCHAIN=local \
    CGO_ENABLED=0 \
    GOCACHE=/tmp/gocache \
    GOPATH=/tmp/gopath

# Прогреваем кеш сборки стандартной библиотеки: без сети и с холодным кешем
# первая же сборка в контейнере стоила бы десятки секунд на каждой задаче.
RUN mkdir -p /opt/warm && cd /opt/warm && \
    go mod init warm && \
    printf 'package main\nimport ("fmt";"os";"strings";"testing";"time";"encoding/json")\nvar _ = []any{fmt.Sprint, os.Exit, strings.TrimSpace, testing.Short, time.Now, json.Marshal}\nfunc main(){}\n' > main.go && \
    go build ./... && go vet ./... && chmod -R a+rwX /tmp/gocache /tmp/gopath

WORKDIR /w
