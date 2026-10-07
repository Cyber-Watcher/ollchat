#!/usr/bin/env bash
#
# build-dist.sh — собирает выпуск под все поддерживаемые системы.
#
# Один и тот же скрипт гоняют GitHub Actions и человек у себя: проверять сборку
# только в CI неудобно, а держать две разные сборки — верный способ получить
# «у меня работает».
#
#   ./scripts/build-dist.sh                     сборка с версией из git
#   VERSION=v1.2.0 ./scripts/build-dist.sh      с заданной версией
#   TARGETS="linux/amd64" ./scripts/build-dist.sh   только одна цель
#
# Итог складывается в dist/: архив на каждую цель плюс checksums.txt.
#
# CGO выключен намеренно: тогда бинарь статический и не тянет за собой
# системных библиотек — его можно просто положить в PATH. Разбор PDF и EPUB
# в проекте свой, на чистом Go, поэтому терять на этом нечего.

set -euo pipefail
# Несовпавший шаблон должен исчезать, а не превращаться в имя файла: иначе
# сборка спотыкается о лицензию, которой нет ни под одним из принятых имён.
shopt -s nullglob

readonly ROOT=$(cd "$(dirname "$0")/.." && pwd)
readonly DIST="$ROOT/dist"

# Цели выпуска — Linux и macOS. Windows и FreeBSD убраны решением владельца
# 07.10.2026: ими никто не пользуется, а замки базы знаний на Windows не работают
# (живость процесса там не проверяется), и выпуск под неё обещал бы то, чего нет.
# olldiagtools кладётся только под linux: он меряет видеопамять на сервере
# с Ollama, а серверы эти — линуксовые.
TARGETS=${TARGETS:-"
linux/amd64
linux/arm64
darwin/amd64
darwin/arm64
"}

VERSION=${VERSION:-}
if [ -z "$VERSION" ]; then
    VERSION=$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)
fi

say() { printf '  %s\n' "$*"; }

rm -rf "$DIST"
mkdir -p "$DIST"

for target in $TARGETS; do
    goos=${target%/*}
    goarch=${target#*/}
    name="ollchat_${VERSION}_${goos}_${goarch}"
    stage="$DIST/$name"
    mkdir -p "$stage"

    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o "$stage/ollchat" "$ROOT/cmd/ollchat"

    # Вторая программа — только под linux, см. комментарий к TARGETS.
    if [ "$goos" = linux ]; then
        CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
            -trimpath -ldflags "-s -w" \
            -o "$stage/olldiagtools" "$ROOT/olldiagtools"

        # ollmcp — служба MCP для сторонних клиентов. Только linux: её ставят
        # на сервер, а не на рабочее место. Служба знаний для самого ollchat
        # отдельного бинаря не требует — она поднимается ключом --serve.
        CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
            -trimpath \
            -ldflags "-s -w" \
            -o "$stage/ollmcp" "$ROOT/ollmcp"
    fi

    cp "$ROOT/README.md" "$stage/"
    # Лицензию кладём под любым принятым именем: LICENSE, LICENSE.txt, LICENSE.md.
    for lic in "$ROOT"/LICENSE*; do
        cp "$lic" "$stage/"
    done

    (cd "$DIST" && tar -czf "${name}.tar.gz" "$name")
    rm -rf "$stage"
    say "собрано: $target"
done

# Суммы считаются по именам файлов без пути: иначе `sha256sum -c` из каталога
# с архивами их не найдёт.
(
    cd "$DIST"
    archives=(*.tar.gz)
    sha256sum "${archives[@]}" > checksums.txt
)

say ""
say "версия $VERSION, файлы в dist/:"
ls -1sh "$DIST" | sed 's/^/    /'
