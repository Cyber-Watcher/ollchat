#!/usr/bin/env bash
# Сборка образов проверки. Запускать на стенде: там Docker, ядра и место.
# Сеть нужна только здесь — сами проверки идут без неё.
# Имена файлов — «<имя>.Dockerfile», а не «Dockerfile.<имя>»: второе оканчивается
# на «.go» у образа Go, и тогда gofmt и go vet принимают Dockerfile за исходник.
set -euo pipefail
cd "$(dirname "$0")"

images=("${@:-go rust dotnet node devops}")
for name in ${images[@]}; do
  echo "── собираю olleval/$name ──"
  docker build -f "$name.Dockerfile" -t "olleval/$name" .
done
docker images --filter=reference='olleval/*' --format 'table {{.Repository}}\t{{.Size}}'
