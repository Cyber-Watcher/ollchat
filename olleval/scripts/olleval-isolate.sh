#!/usr/bin/env bash
# Переводит Ollama на время прогона в localhost и возвращает обратно.
#
# Зачем: пока идёт замер, чужой запрос поделил бы карту и испортил цифры —
# а перезапуск службы посреди его работы оборвал бы ему ответ. Поэтому сервер
# закрывается только после того, как карта признана свободной, и открывается
# обратно в семь утра при любом исходе ночи.
#
# Как. Закрыть — положить службе drop-in: ссылку на заранее приготовленный
# root-файл с OLLAMA_HOST=127.0.0.1:11434. Открыть — убрать ссылку. Свой
# override.conf владельца скрипт не правит вовсе, поэтому открытие возвращает
# ровно тот адрес, что стоял до прогона. Прежде скрипт правил override.conf
# sed'ом и при открытии всегда писал 0.0.0.0:11434: сервер, слушавший один
# адрес, после ночи слушал все. И просил sudo на sed и cp, а это root
# целиком: sed умеет запускать команды, cp — переписать любой файл.
#
# Каждая команда через sudo здесь — с неизменными доводами, и в sudoers её
# разрешают ровно такой; строки и разовая подготовка — в olleval/README.md.
# sudo -n — потому что прогон идёт в tmux, где у sudo есть терминал: без -n
# он ждал бы пароля, которого ночью никто не введёт.
#
#   olleval-isolate.sh on      закрыть на localhost
#   olleval-isolate.sh off     вернуть прежний адрес
#   olleval-isolate.sh status  показать, как слушает сейчас
#   olleval-isolate.sh is-on   код 0 — сервер закрыт этим скриптом
set -euo pipefail

SERVICE=ollama
# Шаблон закрытия кладёт владелец стенда один раз; файл принадлежит root,
# и учётка прогона может лишь сослаться на него, но не поменять ни строки.
TEMPLATE=/etc/olleval/ollama-localhost.conf
# Drop-in читаются по алфавиту, и побеждает последний OLLAMA_HOST: «zz-»
# ставит наш после override.conf.
DROPIN=/etc/systemd/system/$SERVICE.service.d/zz-olleval-localhost.conf
# Копия override.conf, которую оставляла прежняя версия скрипта: есть она,
# а сервер закрыт — закрыт он ещё по-старому (см. off).
LEGACY="${OLLEVAL_ROOT:-$HOME/ollevals}/state/override.conf.orig"

# configured — OLLAMA_HOST, с которым служба запустится: systemd уже свёл
# юнит и все его drop-in. Пусто — не задан, Ollama слушает 127.0.0.1:11434.
configured() {
  systemctl show "$SERVICE" --property=Environment --value 2>/dev/null |
    tr ' ' '\n' | tr -d '"' | sed -n 's/^OLLAMA_HOST=//p' | tail -n 1 || true
}

# port — порт из OLLAMA_HOST; без порта — умолчание Ollama.
port() {
  local p
  p="$(configured)"
  p="${p##*:}"
  case "$p" in
    '' | *[!0-9]*) echo 11434 ;;
    *) echo "$p" ;;
  esac
}

# listening — на каких адресах порт слушают на самом деле: настройка говорит,
# что задумано, а сокет — что вышло. Проверяется сокет, а не ответ API:
# по ответу не видно, на каком адресе сервер слушает.
listening() { ss -Hltn "sport = :$(port)" 2>/dev/null | awk '{print $4}'; }

loopback_addr() {
  case "$1" in
    127.* | "[::1]:"* | "[::ffff:127."*) return 0 ;;
    *) return 1 ;;
  esac
}

loopback_host() {
  case "$1" in
    '' | 127.* | localhost* | "[::1]"* | ::1* | *://127.* | *://localhost* | *://\[::1\]*) return 0 ;;
    *) return 1 ;;
  esac
}

# only_loopback — порт слушают, и только на localhost.
only_loopback() {
  local a addrs
  addrs="$(listening)"
  [ -n "$addrs" ] || return 1
  for a in $addrs; do
    loopback_addr "$a" || return 1
  done
}

# closed — сервер закрыт этим скриптом: стоит наш drop-in или прежняя версия
# оставила правленый override.conf и его копию.
closed() {
  if [ -e "$DROPIN" ] || [ -L "$DROPIN" ]; then
    return 0
  fi
  [ -f "$LEGACY" ] && only_loopback
}

# restart перечитывает юнит, перезапускает службу и ждёт, пока порт снова
# слушают.
restart() {
  local _
  sudo -n systemctl daemon-reload
  sudo -n systemctl restart "$SERVICE"
  for _ in $(seq 1 30); do
    [ -n "$(listening)" ] && return 0
    sleep 1
  done
  echo "Ollama не поднялась после перезапуска: порт $(port) никто не слушает" >&2
  return 1
}

where() { echo "OLLAMA_HOST=$(configured), слушает: $(listening | tr '\n' ' ')"; }

case "${1:-status}" in
  on)
    # Погашенную службу не поднимаем: её гасят руками, когда видеопамять нужна
    # под другое — например, под обучение модели питоновскими скриптами.
    # Прогон в такой момент отобрал бы карту посреди чужой работы. Проверка
    # дублирует правило номер один намеренно: этот скрипт запускают и вручную.
    if ! systemctl is-active --quiet "$SERVICE"; then
      echo "служба $SERVICE остановлена ($(systemctl is-active "$SERVICE")) — не поднимаю её за спиной" >&2
      exit 1
    fi
    if [ ! -r "$TEMPLATE" ]; then
      echo "нет шаблона закрытия $TEMPLATE — его кладёт владелец стенда один раз, см. olleval/README.md" >&2
      exit 1
    fi
    if [ -L "$DROPIN" ] && only_loopback; then
      echo "уже закрыт: $(where)"
      exit 0
    fi
    sudo -n ln -sfn "$TEMPLATE" "$DROPIN"
    restart
    # Проверяется то, что вышло: OLLAMA_HOST могут задать и в обход drop-in
    # (EnvironmentFile=), и тогда «закрыто» было бы только на словах.
    if ! only_loopback; then
      echo "сервер слушает не только localhost — $(where); закрыть не удалось" >&2
      exit 1
    fi
    echo "закрыт на localhost: $(where)"
    ;;
  off)
    # Идемпотентно и без лишних перезапусков: если сервер закрыт не нами,
    # службу не трогаем — вдруг за ней кто-то работает.
    if [ ! -e "$DROPIN" ] && [ ! -L "$DROPIN" ]; then
      if [ -f "$LEGACY" ] && only_loopback; then
        echo "сервер закрыт прежней версией скрипта, правкой override.conf. Вернуть адрес можно" >&2
        echo "только вручную, один раз, проверив копию $LEGACY — см. olleval/README.md" >&2
        exit 1
      fi
      echo "не закрыт: $(where)"
      exit 0
    fi
    sudo -n rm -f "$DROPIN"
    restart
    if ! loopback_host "$(configured)" && only_loopback; then
      echo "после перезапуска сервер слушает только localhost, хотя задано $(where); открыть не удалось" >&2
      exit 1
    fi
    echo "возвращён прежний адрес: $(where)"
    ;;
  status)
    if closed; then
      echo "закрыт на localhost: $(where)"
    else
      echo "не закрыт: $(where)"
    fi
    ;;
  is-on)
    closed
    ;;
  *)
    echo "использование: $0 {on|off|status|is-on}" >&2
    exit 2
    ;;
esac
