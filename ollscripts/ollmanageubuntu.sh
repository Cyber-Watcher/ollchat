#!/usr/bin/env bash
#
# ollmanageubuntu.sh — установка, обновление и удаление Ollama на Ubuntu.
#
# Без ключей: если Ollama нет — ставит последнюю версию; если есть — сверяет
# версию с последней выпущенной и обновляет, когда есть что обновлять.
#
#   -del      удалить Ollama (пакетную — через apt, ручную — вручную)
#   -restore  восстановить конфиги из папки резервной копии
#   -y        не задавать вопросов
#   -h        справка
#
# Главное правило скрипта: **каталог с моделями не трогаем никогда.** Модели
# весят десятки и сотни гигабайт, качать их заново — часы. Поэтому удаление
# сносит программу, но не данные, а установка подхватывает старый каталог.
#
# Запускать через sudo. Резервные копии складываются в домашний каталог того,
# кто запустил скрипт, а не root: за этим следит SUDO_USER.

set -euo pipefail

readonly SCRIPT_NAME=$(basename "$0")
readonly SERVICE=ollama
readonly UNIT_FILE=/etc/systemd/system/${SERVICE}.service
readonly OVERRIDE_DIR=/etc/systemd/system/${SERVICE}.service.d
readonly SERVICE_USER=ollama
readonly DEFAULT_MODELS=/usr/share/ollama/.ollama/models

# Значения по умолчанию для переопределений службы.
#
# OLLAMA_HOST: без него Ollama слушает только 127.0.0.1, и по сети к ней
# не подключиться — а сервер обычно затем и ставят, чтобы ходить на него
# с других машин.
#
# OLLAMA_CONTEXT_LENGTH: окно контекста для запросов, которые его не задали.
# Ноль (умолчание Ollama) означает «выбрать по видеопамяти», и на большой карте
# сервер берёт максимум модели — 256k у нынешних. Крупная модель с таким окном
# перестаёт помещаться в видеопамять, часть слоёв уезжает в оперативную,
# и скорость падает в сотни раз. Замерено: у qwen3.5:122b при автовыборе
# 49 слоёв из 50 на карте, генерация вместо 82 ток/с — доли токена.
# Подробности — в описании параметров моделей в документации проекта.
#
# Внимание: имя переменной именно OLLAMA_CONTEXT_LENGTH. Похожей на вид
# OLLAMA_NUM_CTX не существует, и незнакомые переменные Ollama принимает молча —
# такая опечатка три месяца стояла в настройках стенда и ничего не делала.
readonly DEFAULT_OLLAMA_HOST=0.0.0.0:11434
readonly DEFAULT_CONTEXT_LENGTH=32768
readonly RELEASES_API=https://api.github.com/repos/ollama/ollama/releases/latest
readonly DOWNLOAD_BASE=https://github.com/ollama/ollama/releases/download

ASSUME_YES=0
ACTION=update
RESTORE_DIR=""
TMP_DIRS=()

cleanup() {
    local d
    for d in "${TMP_DIRS[@]:-}"; do
        [ -n "$d" ] && [ -d "$d" ] && rm -rf "$d"
    done
}
trap cleanup EXIT

# ── Вывод ────────────────────────────────────────────────────────────────────

say()  { printf '%s\n' "$*"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()   { printf '  \033[32m✔\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
die()  { printf '\033[31mОшибка:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<EOF
$SCRIPT_NAME — установка, обновление и удаление Ollama на Ubuntu

Использование:
  sudo ./$SCRIPT_NAME              установить, а если уже стоит — обновить
  sudo ./$SCRIPT_NAME -del         удалить Ollama, сохранив конфиги и модели
  sudo ./$SCRIPT_NAME -restore КАТАЛОГ
                                   восстановить конфиги из резервной копии
  ./$SCRIPT_NAME -h                эта справка

Ключи:
  -y    не задавать вопросов (для неинтерактивного запуска)

Что скрипт делает и чего не делает:
  • перед установкой проверяет драйверы NVIDIA и ставит рекомендованные,
    если карта есть, а драйверов нет;
  • ставит Ollama службой systemd, включает автозапуск;
  • перед удалением сохраняет конфиги в ~/ollobackups/ГГГГ-ММ-ДД_ЧЧ_ММ
    того пользователя, который запустил скрипт через sudo;
  • НИКОГДА не удаляет каталог с моделями и не удаляет системного
    пользователя $SERVICE_USER — иначе модели осиротеют по правам доступа.
EOF
}

# ── Разбор аргументов ────────────────────────────────────────────────────────

while [ $# -gt 0 ]; do
    case "$1" in
        -del|--del|--delete|--remove) ACTION=remove ;;
        -restore|--restore)
            ACTION=restore
            shift || true
            [ $# -gt 0 ] || die "-restore требует путь к каталогу резервной копии"
            RESTORE_DIR=$1
            ;;
        -y|--yes) ASSUME_YES=1 ;;
        -h|--help) usage; exit 0 ;;
        *) die "неизвестный ключ: $1 (см. $SCRIPT_NAME -h)" ;;
    esac
    shift
done

# ── Общие проверки ───────────────────────────────────────────────────────────

require_root() {
    [ "$(id -u)" -eq 0 ] || die "нужны права root: запустите через sudo"
}

require_systemd() {
    command -v systemctl >/dev/null 2>&1 || die "systemd не найден, а служба нужна именно systemd"
}

# invoking_user — тот, кто запустил sudo, а не root. Резервные копии должны
# попасть в его домашний каталог и остаться ему доступными.
invoking_user() {
    if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
        printf '%s' "$SUDO_USER"
        return
    fi
    # Запуск от root напрямую: попробуем угадать по владельцу терминала.
    local guess
    guess=$(logname 2>/dev/null || true)
    if [ -n "$guess" ] && [ "$guess" != root ]; then
        printf '%s' "$guess"
        return
    fi
    printf 'root'
}

user_home() {
    local u=$1 home
    home=$(getent passwd "$u" | cut -d: -f6)
    [ -n "$home" ] || home=/root
    printf '%s' "$home"
}

confirm() {
    [ "$ASSUME_YES" -eq 1 ] && return 0
    local answer
    printf '%s [y/N] ' "$1"
    read -r answer </dev/tty || answer=n
    case "$answer" in [yYдД]*) return 0 ;; *) return 1 ;; esac
}

arch_suffix() {
    case "$(uname -m)" in
        x86_64|amd64) printf 'amd64' ;;
        aarch64|arm64) printf 'arm64' ;;
        *) die "неподдерживаемая архитектура: $(uname -m)" ;;
    esac
}

fetch() {
    # fetch URL FILE — скачать архив.
    #
    # Архив весит больше гигабайта, и обрыв связи на середине — не редкость:
    # так и случилось при первом боевом прогоне, на 40%. Поэтому включены
    # повторы и докачка с места обрыва (-C -), а прогресс показывается только
    # человеку за терминалом: в журнале от него одна каша.
    local url=$1 out=$2 progress
    if command -v curl >/dev/null 2>&1; then
        if [ -t 1 ]; then progress=--progress-bar; else progress=--no-progress-meter; fi
        curl -fL "$progress" \
            --retry 5 --retry-delay 3 --retry-all-errors \
            --connect-timeout 30 \
            -C - -o "$out" "$url"
    elif command -v wget >/dev/null 2>&1; then
        wget -q --tries=5 --waitretry=3 --continue -O "$out" "$url"
    else
        die "нужен curl или wget, но не найден ни один"
    fi
}

fetch_text() {
    local url=$1
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url"
    else
        wget -qO- "$url"
    fi
}

# ── Состояние установки ──────────────────────────────────────────────────────

# install_kind — apt, manual или none.
install_kind() {
    if dpkg-query -W -f='${Status}' "$SERVICE" 2>/dev/null | grep -q "install ok installed"; then
        printf 'apt'
        return
    fi
    local bin
    bin=$(command -v ollama 2>/dev/null || true)
    if [ -n "$bin" ] || [ -x /usr/local/bin/ollama ]; then
        printf 'manual'
        return
    fi
    printf 'none'
}

current_version() {
    local bin
    bin=$(command -v ollama 2>/dev/null || true)
    [ -n "$bin" ] || bin=/usr/local/bin/ollama
    [ -x "$bin" ] || return 1
    "$bin" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1
}

latest_version() {
    fetch_text "$RELEASES_API" 2>/dev/null \
        | grep -m1 '"tag_name"' \
        | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' \
        | head -1
}

# asset_url — ссылка на архив под нашу архитектуру.
#
# Имя файла угадывать нельзя: в старых выпусках это ollama-linux-amd64.tgz,
# в свежих — ollama-linux-amd64.tar.zst. Поэтому берём список файлов выпуска
# и выбираем подходящий, предпочитая .tgz как более переносимый.
# Варианты rocm, mlx и jetpack пропускаем: это сборки под другое железо.
asset_url() {
    local arch=$1 json
    json=$(fetch_text "$RELEASES_API" 2>/dev/null) || return 1

    local urls
    urls=$(printf '%s' "$json" \
        | grep -oE '"browser_download_url": *"[^"]+"' \
        | cut -d'"' -f4 \
        | grep -E "ollama-linux-${arch}\.(tgz|tar\.zst)$" || true)

    local u
    for u in $urls; do
        case "$u" in *.tgz) printf '%s' "$u"; return 0 ;; esac
    done
    for u in $urls; do
        printf '%s' "$u"; return 0
    done
    return 1
}

# unpack — распаковать архив в /usr/local, разобравшись со сжатием.
unpack() {
    local file=$1
    case "$file" in
        *.tgz|*.tar.gz)
            tar -tzf "$file" >/dev/null 2>&1 || die "файл не является архивом tar.gz"
            tar -xzf "$file" -C /usr/local
            ;;
        *.tar.zst)
            if tar --help 2>&1 | grep -q -- --zstd; then
                tar --zstd -tf "$file" >/dev/null 2>&1 || die "файл не является архивом tar.zst"
                tar --zstd -xf "$file" -C /usr/local
            elif command -v zstd >/dev/null 2>&1; then
                zstd -dc "$file" | tar -x -C /usr/local
            else
                die "архив сжат zstd, но ни tar --zstd, ни утилиты zstd нет: apt install zstd"
            fi
            ;;
        *) die "неизвестный формат архива: $file" ;;
    esac
}

# models_dir — где лежат модели. Порядок важен: явная настройка службы
# главнее домашнего каталога службы, а он главнее умолчания.
models_dir() {
    local from_env
    from_env=$(systemctl show "$SERVICE" -p Environment 2>/dev/null \
        | tr ' ' '\n' | grep -m1 '^OLLAMA_MODELS=' | cut -d= -f2- || true)
    if [ -n "$from_env" ]; then
        printf '%s' "$from_env"
        return
    fi
    local home
    home=$(getent passwd "$SERVICE_USER" 2>/dev/null | cut -d: -f6 || true)
    if [ -n "$home" ] && [ -d "$home/.ollama/models" ]; then
        printf '%s' "$home/.ollama/models"
        return
    fi
    printf '%s' "$DEFAULT_MODELS"
}

show_state() {
    local kind ver
    kind=$(install_kind)
    case "$kind" in
        apt)    say "  установка: из пакета apt" ;;
        manual) say "  установка: вручную ($(command -v ollama 2>/dev/null || echo /usr/local/bin/ollama))" ;;
        none)   say "  установка: не найдена" ;;
    esac
    if ver=$(current_version); then
        say "  версия:    $ver"
    fi
    if [ "$kind" != none ]; then
        say "  служба:    $(systemctl is-active "$SERVICE" 2>/dev/null || echo неактивна), автозапуск $(systemctl is-enabled "$SERVICE" 2>/dev/null || echo выключен)"
        local md
        md=$(models_dir)
        say "  модели:    $md ($(du -sh "$md" 2>/dev/null | cut -f1 || echo '?'))"
    fi
}

# ── Драйверы NVIDIA ──────────────────────────────────────────────────────────

has_nvidia_gpu() {
    command -v lspci >/dev/null 2>&1 || return 1
    lspci | grep -qi 'nvidia'
}

ensure_nvidia_driver() {
    step "Драйверы NVIDIA"

    if command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi >/dev/null 2>&1; then
        ok "драйвер работает: версия $(nvidia-smi --query-gpu=driver_version --format=csv,noheader | head -1)"
        return 0
    fi
    if ! has_nvidia_gpu; then
        warn "видеокарта NVIDIA не обнаружена — Ollama будет считать на процессоре"
        return 0
    fi

    warn "карта NVIDIA есть, а рабочего драйвера нет"
    if ! confirm "  Установить рекомендованный драйвер?"; then
        warn "пропускаю установку драйвера по вашему решению"
        return 0
    fi

    say "  ставлю ubuntu-drivers-common…"
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ubuntu-drivers-common

    say "  доступные драйверы:"
    ubuntu-drivers devices 2>/dev/null | sed 's/^/    /' || true

    say "  ставлю рекомендованный драйвер…"
    if ubuntu-drivers install 2>&1 | sed 's/^/    /'; then
        ok "драйвер установлен"
        warn "после установки драйвера нужна ПЕРЕЗАГРУЗКА сервера,"
        warn "иначе Ollama не увидит карту и уйдёт считать на процессор"
    else
        die "не удалось установить драйвер — поставьте вручную и запустите скрипт снова"
    fi
}

# ── Резервная копия конфигов ─────────────────────────────────────────────────

# backup_configs кладёт путь копии в BACKUP_DIR, а не печатает его: иначе
# перехват вывода ради пути проглатывал бы весь ход работы.
BACKUP_DIR=""

backup_configs() {
    local user home stamp dest
    user=$(invoking_user)
    home=$(user_home "$user")
    stamp=$(date '+%Y-%m-%d_%H_%M')   # год-месяц-день_часы_минуты, 24 часа
    dest="$home/ollobackups/$stamp"

    step "Резервная копия настроек"
    mkdir -p "$dest"

    local saved=0
    if [ -f "$UNIT_FILE" ]; then
        cp -a "$UNIT_FILE" "$dest/"; saved=1
        ok "unit-файл службы"
    fi
    if [ -d "$OVERRIDE_DIR" ]; then
        cp -a "$OVERRIDE_DIR" "$dest/"; saved=1
        ok "переопределения службы ($(ls "$OVERRIDE_DIR" | tr '\n' ' '))"
    fi
    for extra in /etc/default/ollama /etc/ollama; do
        if [ -e "$extra" ]; then
            cp -a "$extra" "$dest/"; saved=1
            ok "$extra"
        fi
    done

    # Манифест: то, что не файлы, но без чего восстановление вслепую.
    {
        echo "# Слепок установки Ollama, снят $(date '+%Y-%m-%d %H:%M:%S %Z')"
        echo "host=$(hostname)"
        echo "install_kind=$(install_kind)"
        echo "version=$(current_version || echo неизвестна)"
        echo "models_dir=$(models_dir)"
        echo "service_user=$SERVICE_USER"
        echo "service_uid=$(id -u "$SERVICE_USER" 2>/dev/null || echo нет)"
        echo "service_active=$(systemctl is-active "$SERVICE" 2>/dev/null || echo нет)"
        echo "service_enabled=$(systemctl is-enabled "$SERVICE" 2>/dev/null || echo нет)"
        echo
        echo "# Переменные окружения службы"
        systemctl show "$SERVICE" -p Environment 2>/dev/null || true
        echo
        echo "# Модели на момент снятия копии"
        if command -v ollama >/dev/null 2>&1; then
            ollama list 2>/dev/null || echo "(список получить не удалось — служба остановлена?)"
        fi
    } > "$dest/manifest.txt"
    ok "манифест: версия, пути, переменные, список моделей"

    [ "$saved" -eq 1 ] || warn "файлов настроек не нашлось — сохранён только манифест"

    # Права: каталог должен остаться доступным тому, кто запускал скрипт.
    local group
    group=$(id -gn "$user" 2>/dev/null || echo "$user")
    chown -R "$user:$group" "$home/ollobackups"
    chmod 755 "$home/ollobackups" "$dest"
    find "$dest" -type f -exec chmod 644 {} +

    say ""
    ok "копия: $dest (владелец $user)"
    BACKUP_DIR=$dest
}

# newest_backup — самая свежая копия того, кто запустил скрипт.
newest_backup() {
    local user home dir
    user=$(invoking_user)
    home=$(user_home "$user")
    dir="$home/ollobackups"
    [ -d "$dir" ] || return 1
    # Сортируем по времени изменения, а не по имени. Имя в формате
    # год-месяц-день сортируется и само, но время — это факт, а имя
    # можно переименовать руками, и тогда «самая свежая» найдётся неверно.
    find "$dir" -maxdepth 1 -mindepth 1 -type d -printf '%T@ %p\n' 2>/dev/null \
        | sort -n | tail -1 | cut -d' ' -f2-
}

restore_configs() {
    local src=$1
    [ -d "$src" ] || die "каталог резервной копии не найден: $src"

    step "Восстановление настроек из $src"
    if [ -f "$src/ollama.service" ]; then
        cp -a "$src/ollama.service" "$UNIT_FILE"
        ok "unit-файл службы"
    fi
    if [ -d "$src/ollama.service.d" ]; then
        mkdir -p "$OVERRIDE_DIR"
        cp -a "$src/ollama.service.d/." "$OVERRIDE_DIR/"
        ok "переопределения службы"
    fi
    for extra in ollama; do
        [ -e "$src/$extra" ] && [ ! -e /etc/default/ollama ] && cp -a "$src/$extra" /etc/default/ollama && ok "/etc/default/ollama"
    done
    systemctl daemon-reload
}

# ── Установка ────────────────────────────────────────────────────────────────

ensure_service_user() {
    if id "$SERVICE_USER" >/dev/null 2>&1; then
        return 0
    fi
    say "  создаю системного пользователя $SERVICE_USER…"
    useradd -r -s /bin/false -U -m -d /usr/share/ollama "$SERVICE_USER"
    ok "пользователь $SERVICE_USER создан"
}

write_unit() {
    # Если unit уже есть, не трогаем: в нём могли быть правки под этот сервер.
    if [ -f "$UNIT_FILE" ]; then
        ok "unit-файл уже есть, оставляю как есть: $UNIT_FILE"
        return 0
    fi
    cat > "$UNIT_FILE" <<EOF
[Unit]
Description=Ollama Service
After=network-online.target

[Service]
ExecStart=/usr/local/bin/ollama serve
User=$SERVICE_USER
Group=$SERVICE_USER
Restart=always
RestartSec=3
Environment="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

[Install]
WantedBy=default.target
EOF
    chmod 644 "$UNIT_FILE"
    ok "создан unit-файл: $UNIT_FILE"
}

write_override() {
    # Переменные кладём не в сам unit, а в переопределения: установщик Ollama
    # перезаписывает unit при обновлении, а этот каталог не трогает.
    if [ -f "$OVERRIDE_DIR/override.conf" ]; then
        ok "переопределения уже есть, оставляю как есть: $OVERRIDE_DIR/override.conf"
        return 0
    fi
    mkdir -p "$OVERRIDE_DIR"
    cat > "$OVERRIDE_DIR/override.conf" <<EOF
[Service]
Environment="OLLAMA_HOST=$DEFAULT_OLLAMA_HOST"
Environment="OLLAMA_CONTEXT_LENGTH=$DEFAULT_CONTEXT_LENGTH"
EOF
    chmod 644 "$OVERRIDE_DIR/override.conf"
    ok "созданы переопределения: $OVERRIDE_DIR/override.conf"
    say "  слушает: $DEFAULT_OLLAMA_HOST · окно по умолчанию: $DEFAULT_CONTEXT_LENGTH токенов"
    say "  проверить, что сервер их принял: journalctl -u $SERVICE | grep 'server config'"
}

install_ollama() {
    local version=$1 arch tmp url file
    arch=$(arch_suffix)
    tmp=$(mktemp -d)
    TMP_DIRS+=("$tmp")

    url=$(asset_url "$arch") || die "в выпуске нет архива под архитектуру $arch"
    file="$tmp/$(basename "$url")"
    say "  качаю $(basename "$url") версии $version…"
    fetch "$url" "$file" || die "не удалось скачать архив версии $version"

    # Службу останавливаем только теперь, когда архив уже на диске: простой
    # длится распаковку, а не всю закачку, и сорвавшаяся сеть не оставит
    # сервер без работающей Ollama.
    if systemctl is-active --quiet "$SERVICE" 2>/dev/null; then
        say "  останавливаю службу на время замены файлов…"
        systemctl stop "$SERVICE"
    fi

    say "  распаковываю в /usr/local…"
    # Старые библиотеки убираем: в них остаются файлы от прошлой версии,
    # а Ollama грузит из этого каталога всё подряд.
    rm -rf /usr/local/lib/ollama
    unpack "$file"
    chmod 755 /usr/local/bin/ollama

    local got
    got=$(/usr/local/bin/ollama --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)
    [ -n "$got" ] || die "установленный бинарь не запускается"
    ok "распакована версия $got"
}

start_service() {
    systemctl daemon-reload
    systemctl enable "$SERVICE" >/dev/null 2>&1 || true
    systemctl restart "$SERVICE"

    # Дать службе время подняться, прежде чем отчитываться об успехе.
    local i
    for i in $(seq 1 20); do
        if systemctl is-active --quiet "$SERVICE"; then
            ok "служба запущена"
            return 0
        fi
        sleep 1
    done
    warn "служба не поднялась за 20 секунд — смотрите: journalctl -u $SERVICE -n 50"
    return 1
}

fix_models_ownership() {
    local md
    md=$(models_dir)
    [ -d "$md" ] || return 0
    local owner
    owner=$(stat -c %U "$md" 2>/dev/null || echo '?')
    if [ "$owner" != "$SERVICE_USER" ]; then
        warn "каталог моделей принадлежит $owner, а служба работает от $SERVICE_USER"
        say "  выправляю владельца, иначе модели не прочитаются…"
        chown -R "$SERVICE_USER:$SERVICE_USER" "$md"
        # Родительский .ollama тоже принадлежит службе: там манифесты и ключи.
        local parent
        parent=$(dirname "$md")
        [ "$(basename "$parent")" = .ollama ] && chown "$SERVICE_USER:$SERVICE_USER" "$parent"
        ok "владелец каталога моделей исправлен"
    fi
}

report_after_install() {
    local md
    md=$(models_dir)
    step "Готово"
    say "Версия:            $(current_version || echo '?')"
    say "Служба systemd:    $SERVICE"
    say ""
    say "Файлы настроек:"
    say "  unit-файл:       $UNIT_FILE"
    say "  переопределения: $OVERRIDE_DIR/override.conf"
    say "                   (сюда кладут OLLAMA_HOST, OLLAMA_MODELS и прочие переменные)"
    say "  бинарь:          /usr/local/bin/ollama"
    say "  библиотеки:      /usr/local/lib/ollama"
    say "  модели:          $md"
    say ""
    say "Управление службой:"
    say "  запуск:          sudo systemctl start $SERVICE"
    say "  остановка:       sudo systemctl stop $SERVICE"
    say "  перезапуск:      sudo systemctl restart $SERVICE"
    say "  состояние:       systemctl status $SERVICE"
    say "  журнал:          journalctl -u $SERVICE -f"
    say "  автозапуск:      sudo systemctl enable|disable $SERVICE"
    say ""
    say "Изменить настройки:"
    say "  sudo systemctl edit $SERVICE     # правит $OVERRIDE_DIR/override.conf"
    say "  sudo systemctl restart $SERVICE  # применить"
    say ""
    if command -v ollama >/dev/null 2>&1; then
        say "Модели на месте:"
        ollama list 2>/dev/null | sed 's/^/  /' || say "  (служба ещё поднимается)"
    fi
}

do_install_or_update() {
    require_root
    require_systemd

    step "Что уже установлено"
    show_state

    local kind current latest
    kind=$(install_kind)
    latest=$(latest_version) || true
    [ -n "$latest" ] || die "не удалось узнать последнюю версию с github (нет сети?)"
    say "  последняя выпущенная версия: $latest"

    if [ "$kind" = none ]; then
        ensure_nvidia_driver
        step "Установка Ollama $latest"
        ensure_service_user
        install_ollama "$latest"
        write_unit

        # Если конфиги остались от прошлой установки — вернём их: без этого
        # потеряются OLLAMA_HOST и прочее, и сервер станет слушать только себя.
        if [ ! -d "$OVERRIDE_DIR" ]; then
            local backup
            if backup=$(newest_backup); then
                if [ -d "$backup/ollama.service.d" ]; then
                    warn "нашлась резервная копия настроек: $backup"
                    if confirm "  Восстановить из неё переопределения службы?"; then
                        restore_configs "$backup"
                    fi
                fi
            fi
        fi

        # Восстановленные настройки не трогаем — write_override уходит ни с чем,
        # если файл уже на месте. А если копии не было, кладём разумные значения:
        # без них сервер слушал бы только себя и брал окно автовыбором.
        write_override
        systemctl daemon-reload

        fix_models_ownership
        start_service || true
        report_after_install
        return 0
    fi

    current=$(current_version || echo 0.0.0)
    if [ "$current" = "$latest" ]; then
        step "Обновление не требуется"
        ok "установлена последняя версия $current"
        return 0
    fi

    step "Обновление $current → $latest"
    if [ "$kind" = apt ]; then
        say "  установка пакетная, обновляю через apt…"
        DEBIAN_FRONTEND=noninteractive apt-get update -qq
        DEBIAN_FRONTEND=noninteractive apt-get install -y --only-upgrade "$SERVICE"
    else
        # Обновление ручной установки — это замена файлов. Конфиги и модели
        # остаются на месте, поэтому копию снимаем на всякий случай, а не ради
        # восстановления.
        backup_configs
        install_ollama "$latest"
    fi
    fix_models_ownership
    start_service || true
    report_after_install
}

# ── Удаление ─────────────────────────────────────────────────────────────────

do_remove() {
    require_root

    step "Что будет удалено"
    show_state

    local kind md
    kind=$(install_kind)
    if [ "$kind" = none ]; then
        die "Ollama не установлена — удалять нечего"
    fi
    md=$(models_dir)

    say ""
    say "Будет удалено: программа и служба."
    say "НЕ будет удалено:"
    say "  • каталог моделей $md — там $(du -sh "$md" 2>/dev/null | cut -f1 || echo '?') данных;"
    say "  • системный пользователь $SERVICE_USER — иначе модели осиротеют по правам."
    say ""
    if ! confirm "Удалить Ollama?"; then
        say "отменено"
        exit 0
    fi

    backup_configs
    local backup=$BACKUP_DIR

    step "Удаление"
    systemctl stop "$SERVICE" 2>/dev/null || true
    systemctl disable "$SERVICE" >/dev/null 2>&1 || true
    ok "служба остановлена и снята с автозапуска"

    if [ "$kind" = apt ]; then
        say "  установка пакетная — удаляю через apt…"
        DEBIAN_FRONTEND=noninteractive apt-get purge -y "$SERVICE"
        DEBIAN_FRONTEND=noninteractive apt-get autoremove -y
        ok "пакет удалён"
    else
        say "  установка ручная — убираю файлы…"
        rm -f /usr/local/bin/ollama
        rm -rf /usr/local/lib/ollama
        ok "удалены /usr/local/bin/ollama и /usr/local/lib/ollama"
    fi

    rm -f "$UNIT_FILE"
    rm -rf "$OVERRIDE_DIR"
    systemctl daemon-reload
    systemctl reset-failed "$SERVICE" 2>/dev/null || true
    ok "unit-файлы удалены"

    step "Готово"
    say "Ollama удалена."
    say "Сохранено:"
    say "  настройки: $backup"
    say "  модели:    $md (не тронуты)"
    say ""
    say "Поставить заново: sudo ./$SCRIPT_NAME"
    say "Скрипт предложит вернуть настройки из копии, а модели подхватятся сами."
}

# ── Точка входа ──────────────────────────────────────────────────────────────

main() {
    case "$ACTION" in
        update)  do_install_or_update ;;
        remove)  do_remove ;;
        restore)
            require_root
            restore_configs "$RESTORE_DIR"
            systemctl restart "$SERVICE" 2>/dev/null || true
            ok "настройки восстановлены, служба перезапущена"
            ;;
    esac
}

# Запускаем действие только при прямом вызове: так функции можно подключить
# в тесте (source) и проверить разбор состояния, ничего не меняя в системе.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    main
fi
