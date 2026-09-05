#!/usr/bin/env bash
# Переводит Ollama на время прогона в localhost и возвращает обратно.
#
# Зачем: пока идёт замер, чужой запрос поделил бы карту и испортил цифры —
# а перезапуск службы посреди его работы оборвал бы ему ответ. Поэтому сервер
# закрывается только после того, как карта признана свободной, и открывается
# обратно в семь утра при любом исходе ночи.
#
#   olleval-isolate.sh on      закрыть на localhost
#   olleval-isolate.sh off     вернуть в общий доступ
#   olleval-isolate.sh status  показать, как слушает сейчас
set -euo pipefail

CONF=/etc/systemd/system/ollama.service.d/override.conf
STATE="${OLLEVAL_ROOT:-$HOME/ollevals}/state"
BACKUP="$STATE/override.conf.orig"

current() { sudo grep -oP 'OLLAMA_HOST=\K[^"]+' "$CONF" || echo "не задан"; }

apply() { # $1 — новое значение OLLAMA_HOST
  sudo sed -i "s|OLLAMA_HOST=[^\"]*|OLLAMA_HOST=$1|" "$CONF"
  sudo systemctl daemon-reload
  sudo systemctl restart ollama
  for _ in $(seq 1 30); do
    curl -sf --max-time 2 http://127.0.0.1:11434/api/version >/dev/null && return 0
    sleep 1
  done
  echo "Ollama не поднялась после перезапуска" >&2
  return 1
}

case "${1:-status}" in
  on)
    # Погашенную службу не поднимаем: её гасят руками, когда видеопамять нужна
    # под другое — например, под обучение модели питоновскими скриптами.
    # Прогон в такой момент отобрал бы карту посреди чужой работы. Проверка
    # дублирует правило номер один намеренно: этот скрипт запускают и вручную.
    if ! systemctl is-active --quiet ollama; then
      echo "служба ollama остановлена ($(systemctl is-active ollama)) — не поднимаю её за спиной" >&2
      exit 1
    fi
    mkdir -p "$STATE"
    [ -f "$BACKUP" ] || sudo cp "$CONF" "$BACKUP"
    if [[ "$(current)" == 127.0.0.1:* ]]; then
      echo "уже закрыт: $(current)"; exit 0
    fi
    apply "127.0.0.1:11434"
    echo "закрыт на localhost: $(current)"
    ;;
  off)
    # Идемпотентно и без лишних перезапусков: если сервер уже открыт,
    # службу не трогаем — вдруг за ней кто-то работает.
    if [[ "$(current)" != 127.0.0.1:* ]]; then
      echo "уже открыт: $(current)"; exit 0
    fi
    apply "0.0.0.0:11434"
    echo "возвращён в общий доступ: $(current)"
    ;;
  status)
    echo "OLLAMA_HOST=$(current)"
    ;;
  *)
    echo "использование: $0 {on|off|status}" >&2; exit 2
    ;;
esac
