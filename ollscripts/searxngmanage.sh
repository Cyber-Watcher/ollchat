#!/usr/bin/env bash
#
# searxngmanage.sh — установка, обновление и удаление SearXNG на Ubuntu.
#
# SearXNG — метапоисковик: сам ходит в Google, Bing, DuckDuckGo и ещё десятки
# источников, а наружу отдаёт единый ответ. Нужен ollchat, чтобы у модели
# появился инструмент web_search: без своего экземпляра поиск в сети
# невозможен — публичные отдают 403 и 429, а разбор чужой вёрстки перестаёт
# работать со второго запроса (проверено).
#
#   -del      удалить службу и контейнер
#   -restore  восстановить настройки из папки резервной копии
#   -p ПОРТ   порт (по умолчанию 8888)
#   -y        не задавать вопросов
#   -h        справка
#
# Правило то же, что у ollmanageubuntu.sh: **настройки не теряем никогда.**
# Удаление сносит контейнер и службу, но settings.yml сохраняется в резервную
# копию — там лежит секретный ключ, с которым сохранятся и ссылки на выдачу.
#
# Запускать через sudo. Резервные копии складываются в домашний каталог того,
# кто запустил скрипт, а не root: за этим следит SUDO_USER.

set -euo pipefail

readonly SCRIPT_NAME=$(basename "$0")
readonly SERVICE=searxng
readonly UNIT_FILE=/etc/systemd/system/${SERVICE}.service
readonly INSTALL_DIR=/opt/searxng
readonly IMAGE=docker.io/searxng/searxng:latest

ASSUME_YES=0
ACTION=update
RESTORE_DIR=""
PORT=8888

# ── Вывод ────────────────────────────────────────────────────────────────────

say()  { printf '%s\n' "$*"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()   { printf '  \033[32m✔\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
die()  { printf '\033[31mОшибка:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<EOF
$SCRIPT_NAME — установка, обновление и удаление SearXNG на Ubuntu

Использование:
  sudo ./$SCRIPT_NAME              установить, а если уже стоит — обновить образ
  sudo ./$SCRIPT_NAME -del         удалить, сохранив настройки в резервную копию
  sudo ./$SCRIPT_NAME -restore КАТАЛОГ
                                   восстановить настройки из резервной копии
  sudo ./$SCRIPT_NAME -p 8080      другой порт (по умолчанию $PORT)
  sudo ./$SCRIPT_NAME -y           не задавать вопросов
  sudo ./$SCRIPT_NAME -h           эта справка

После установки проверить:
  curl -s 'http://ЭТОТ_СЕРВЕР:$PORT/search?q=golang&format=json' | head -c 200

И вписать адрес в ollchat, раздел [web]:
  searxng_url = "http://ЭТОТ_СЕРВЕР:$PORT"
EOF
}

# ── Разбор аргументов ────────────────────────────────────────────────────────

while [ $# -gt 0 ]; do
    case "$1" in
        -del)     ACTION=remove ;;
        -restore) ACTION=restore; RESTORE_DIR=${2:-}; shift ;;
        -p)       PORT=${2:-}; shift ;;
        -y)       ASSUME_YES=1 ;;
        -h|--help) usage; exit 0 ;;
        *)        die "неизвестный ключ $1 (см. -h)" ;;
    esac
    shift
done

case "$PORT" in
    ''|*[!0-9]*) die "порт должен быть числом, получено: ${PORT:-пусто}" ;;
esac

# ── Общие проверки ───────────────────────────────────────────────────────────

require_root() {
    [ "$(id -u)" -eq 0 ] || die "нужны права root: запустите через sudo"
}

require_docker() {
    command -v docker >/dev/null 2>&1 || die "не найден docker: поставьте его и повторите"
    docker compose version >/dev/null 2>&1 || die "не найден docker compose (плагин v2)"
}

require_systemd() {
    command -v systemctl >/dev/null 2>&1 || die "systemd не найден: скрипт рассчитан на Ubuntu с systemd"
}

# invoking_user возвращает того, кто запустил sudo, а не root: резервные копии
# должны попадать в его домашний каталог, иначе их потом не найти.
invoking_user() {
    if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
        printf '%s' "$SUDO_USER"
    else
        printf '%s' root
    fi
}

user_home() {
    local u; u=$(invoking_user)
    getent passwd "$u" | cut -d: -f6
}

confirm() {
    [ "$ASSUME_YES" -eq 1 ] && return 0
    local answer
    read -r -p "$1 [y/N] " answer </dev/tty || answer=n
    case "$answer" in [yYдД]*) return 0 ;; *) return 1 ;; esac
}

port_taken() {
    ss -ltn 2>/dev/null | grep -q ":$PORT " || return 1
    # Наш же контейнер на этом порту — не помеха.
    docker ps --filter "name=^${SERVICE}$" --format '{{.Ports}}' 2>/dev/null | grep -q ":$PORT->" && return 1
    return 0
}

# ── Настройки ────────────────────────────────────────────────────────────────

# write_settings кладёт settings.yml с включённым JSON.
#
# Ради JSON всё и затевается: по умолчанию SearXNG отдаёт только HTML, и без
# этой строки инструмент web_search получит вёрстку вместо данных.
write_settings() {
    local secret=$1
    cat > "$INSTALL_DIR/settings.yml" <<EOF
# Настройки SearXNG для ollchat. Создано $SCRIPT_NAME.
use_default_settings: true

server:
  # Ключ подписывает ссылки выдачи. Меняется — расходятся сохранённые ссылки,
  # поэтому при переустановке его берут из резервной копии, а не создают заново.
  secret_key: "$secret"
  limiter: false          # ограничитель частоты нужен публичным экземплярам
  image_proxy: false

search:
  # Ради этой строки всё и делается: без json наружу идёт только HTML.
  formats:
    - html
    - json
  autocomplete: ""
  default_lang: ""

outgoing:
  request_timeout: 6.0
  max_request_timeout: 12.0
  pool_connections: 100
  pool_maxsize: 20
EOF
    chmod 0640 "$INSTALL_DIR/settings.yml"
}

write_compose() {
    cat > "$INSTALL_DIR/docker-compose.yml" <<EOF
# Создано $SCRIPT_NAME. Правьте settings.yml, а не этот файл.
services:
  searxng:
    image: $IMAGE
    container_name: $SERVICE
    restart: unless-stopped
    ports:
      - "$PORT:8080"
    volumes:
      - ./settings.yml:/etc/searxng/settings.yml:ro
    environment:
      - SEARXNG_BASE_URL=http://localhost:$PORT/
    cap_drop:
      - ALL
    cap_add:
      - CHOWN
      - SETGID
      - SETUID
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
EOF
}

write_unit() {
    cat > "$UNIT_FILE" <<EOF
[Unit]
Description=SearXNG для ollchat (docker compose)
Requires=docker.service
After=docker.service network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=$INSTALL_DIR
ExecStart=/usr/bin/docker compose up -d
ExecStop=/usr/bin/docker compose down
TimeoutStartSec=0

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
}

# ── Резервные копии ──────────────────────────────────────────────────────────

backup_settings() {
    [ -f "$INSTALL_DIR/settings.yml" ] || return 0
    local dir; dir="$(user_home)/searxngbackups/$(date +%Y%m%d-%H%M%S)"
    mkdir -p "$dir"
    cp -a "$INSTALL_DIR/settings.yml" "$dir/" 2>/dev/null || true
    [ -f "$INSTALL_DIR/docker-compose.yml" ] && cp -a "$INSTALL_DIR/docker-compose.yml" "$dir/"
    chown -R "$(invoking_user)":"$(invoking_user)" "$(user_home)/searxngbackups" 2>/dev/null || true
    ok "настройки сохранены в $dir"
}

# existing_secret достаёт ключ из установленных настроек, чтобы обновление
# не разорвало уже выданные ссылки.
existing_secret() {
    [ -f "$INSTALL_DIR/settings.yml" ] || return 1
    sed -n 's/^ *secret_key: *"\(.*\)"$/\1/p' "$INSTALL_DIR/settings.yml" | head -1
}

# ── Действия ─────────────────────────────────────────────────────────────────

do_install() {
    require_root; require_docker; require_systemd

    if port_taken; then
        die "порт $PORT занят другой службой; выберите другой ключом -p"
    fi

    step "Установка SearXNG"
    mkdir -p "$INSTALL_DIR"

    local secret
    if secret=$(existing_secret) && [ -n "$secret" ]; then
        ok "ключ подписи взят из прежних настроек"
    else
        secret=$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
        ok "создан новый ключ подписи"
    fi

    backup_settings
    write_settings "$secret"
    write_compose
    write_unit
    ok "файлы в $INSTALL_DIR"

    step "Загрузка образа"
    docker compose -f "$INSTALL_DIR/docker-compose.yml" pull
    ok "образ получен"

    step "Запуск службы"
    systemctl enable "$SERVICE" >/dev/null 2>&1 || true
    systemctl restart "$SERVICE"
    ok "служба $SERVICE запущена"

    step "Проверка"
    local i out
    for i in $(seq 1 30); do
        out=$(curl -s --max-time 5 "http://127.0.0.1:$PORT/search?q=golang&format=json" 2>/dev/null || true)
        case "$out" in
            *'"results"'*) ok "JSON отдаётся, поиск работает"; break ;;
        esac
        [ "$i" -eq 30 ] && { warn "служба поднялась, но JSON пока не отвечает"; warn "смотрите: docker logs $SERVICE"; break; }
        sleep 2
    done

    say
    say "Готово. Впишите в ollchat, раздел [web]:"
    say "  searxng_url = \"http://$(hostname -I | awk '{print $1}'):$PORT\""
}

do_remove() {
    require_root; require_docker
    step "Удаление SearXNG"
    confirm "Удалить службу и контейнер $SERVICE? Настройки будут сохранены." || die "отменено"

    backup_settings
    systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
    [ -f "$INSTALL_DIR/docker-compose.yml" ] && docker compose -f "$INSTALL_DIR/docker-compose.yml" down 2>/dev/null || true
    docker rm -f "$SERVICE" >/dev/null 2>&1 || true
    rm -f "$UNIT_FILE"
    systemctl daemon-reload
    ok "служба и контейнер удалены"
    warn "каталог $INSTALL_DIR оставлен: в нём настройки"
    say "Образ, если он больше не нужен: docker rmi $IMAGE"
}

do_restore() {
    require_root
    [ -n "$RESTORE_DIR" ] || die "укажите каталог резервной копии"
    [ -f "$RESTORE_DIR/settings.yml" ] || die "в $RESTORE_DIR нет settings.yml"
    step "Восстановление настроек"
    mkdir -p "$INSTALL_DIR"
    cp -a "$RESTORE_DIR/settings.yml" "$INSTALL_DIR/settings.yml"
    ok "настройки восстановлены"
    if systemctl is-enabled "$SERVICE" >/dev/null 2>&1; then
        systemctl restart "$SERVICE"
        ok "служба перезапущена"
    fi
}

case "$ACTION" in
    update)  do_install ;;
    remove)  do_remove ;;
    restore) do_restore ;;
esac
