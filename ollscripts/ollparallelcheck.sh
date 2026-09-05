#!/usr/bin/env bash
# Проверка: обрабатывает ли сервер Ollama запросы к модели параллельно
# или ставит их в очередь.
#
# Зачем. Клиент может слать сколько угодно запросов разом, но Ollama решает
# сама, сколько обрабатывать одновременно. Число слотов задаётся переменной
# OLLAMA_NUM_PARALLEL на сервере, а для некоторых архитектур Ollama сводит его
# к единице независимо от настройки. Со стороны клиента разницы не видно:
# запросы уходят и возвращаются, просто медленно. Отсюда правило — прежде чем
# писать в отчёте «считали в четыре потока», это надо измерить.
#
#   ./ollparallelcheck.sh -m qwen3.8:latest
#   ./ollparallelcheck.sh -m glm-4.7-flash:q8_0 -u http://ollama.example:11434 -n 8
#
# Выход: 0 — параллельность работает, 1 — очередь, 2 — не удалось проверить.

set -uo pipefail

URL="${OLLAMA_URL:-http://localhost:11434}"
MODEL=""
JOBS=4
PREDICT=120

usage() {
	cat <<'USAGE'
Проверка параллельности обработки запросов сервером Ollama.

  -m МОДЕЛЬ   что проверять (обязательно), например qwen3.8:latest
  -u АДРЕС    сервер, по умолчанию $OLLAMA_URL или http://localhost:11434
  -n ЧИСЛО    сколько запросов слать разом, по умолчанию 4
  -p ЧИСЛО    длина ответа в токенах, по умолчанию 120
  -h          эта справка

Код возврата: 0 параллельность есть, 1 очередь, 2 проверить не удалось.
USAGE
}

while getopts "m:u:n:p:h" opt; do
	case "$opt" in
	m) MODEL="$OPTARG" ;;
	u) URL="$OPTARG" ;;
	n) JOBS="$OPTARG" ;;
	p) PREDICT="$OPTARG" ;;
	h)
		usage
		exit 0
		;;
	*)
		usage
		exit 2
		;;
	esac
done

if [ -z "$MODEL" ]; then
	echo "не задана модель: -m ИМЯ" >&2
	usage >&2
	exit 2
fi
command -v curl >/dev/null || {
	echo "нужен curl" >&2
	exit 2
}

URL="${URL%/}"
WORK=$(mktemp -d) || exit 2
trap 'rm -rf "$WORK"' EXIT

# Тело запроса. Температура низкая, чтобы длина ответа не плясала от запуска
# к запуску: мы меряем сервер, а не разнообразие модели.
# Запрос у всех один и тот же. Разный текст давал ответы разной длины, а вместе
# с ними разброс времени, который смазывает вердикт: мы меряем сервер, а не то,
# насколько модель разговорчива на разные темы.
body() {
	cat <<JSON
{"model":"$MODEL","stream":false,"think":false,
 "messages":[{"role":"user","content":"Опиши в одном абзаце, что такое сетевой протокол."}],
 "options":{"temperature":0.2,"num_predict":$PREDICT}}
JSON
}

ask() { # $1 — не используется, оставлен для читаемости вызова; $2 — файл времени
	local start finish
	start=$(date +%s%3N)
	curl -s -m 600 "$URL/api/chat" -H 'Content-Type: application/json' \
		-d "$(body)" -o /dev/null
	finish=$(date +%s%3N)
	echo $((finish - start)) >"$2"
}

ms() { awk -v v="$1" 'BEGIN{printf "%.1f", v/1000}'; }

echo "сервер: $URL"
ver=$(curl -s -m 10 "$URL/api/version")
[ -n "$ver" ] || {
	echo "сервер не отвечает" >&2
	exit 2
}
echo "версия Ollama: ${ver//[\{\}\"]/}"

if ! curl -s -m 20 "$URL/api/tags" | grep -q "\"$MODEL\""; then
	echo "модели $MODEL на сервере нет" >&2
	exit 2
fi
echo "модель: $MODEL, запросов разом: $JOBS, длина ответа: $PREDICT токенов"

# Прогрев: первый запрос грузит модель в память, и его время к делу
# не относится.
echo -n "прогрев (загрузка модели)... "
ask "прогрев" "$WORK/warm"
echo "$(ms "$(cat "$WORK/warm")") с"

# Опорное время меряем дважды и берём меньшее: на нём держится весь вывод,
# и один шумный замер испортил бы вердикт.
ask "один" "$WORK/single-1"
ask "один" "$WORK/single-2"
single=$(cat "$WORK"/single-* | sort -n | head -1)
echo "один запрос:      $(ms "$single") с (лучшее из двух)"

start=$(date +%s%3N)
for i in $(seq 1 "$JOBS"); do
	ask "запрос номер $i" "$WORK/job-$i" &
done
wait
finish=$(date +%s%3N)
total=$((finish - start))

fastest=$(cat "$WORK"/job-* | sort -n | head -1)
slowest=$(cat "$WORK"/job-* | sort -n | tail -1)
echo "$JOBS разом:        $(ms "$total") с (каждый $(ms "$fastest")–$(ms "$slowest") с)"

# Ускорение: во сколько раз пропускная способность выше одиночной. При честной
# параллельности стремится к числу слотов, при очереди держится около единицы.
speedup=$(awk -v s="$single" -v t="$total" -v n="$JOBS" 'BEGIN{printf "%.2f", (n*s)/t}')
echo "ускорение:        ${speedup}x из $JOBS возможных"

# Разброс времён — признак надёжнее ускорения. В очереди запросы возвращаются
# лесенкой: первый за время одиночного, последний — за N таких. При настоящей
# параллельности все заканчиваются почти вместе.
#
# Замеры 26.08.2026: очередь — ускорение 1.05–1.47 при разбросе 3.2; настоящая
# параллельность — ускорение 2.67 при разбросе 1.13 (четыре слота) и 3.02
# при 1.96 (восемь запросов на четыре слота). Порог по одному ускорению уже
# ошибался: 1.47 на очереди почти дотянулось до 1.5.
spread=$(awk -v f="$fastest" -v s="$slowest" 'BEGIN{printf "%.2f", (f>0)? s/f : 0}')
echo "разброс времён:   ${spread}x (в очереди стремится к $JOBS, при параллельности к 1)"
verdict=$(awk -v s="$speedup" -v d="$spread" \
	'BEGIN{print (s+0 >= 1.8 && d+0 <= 2.5) ? "yes" : "no"}')

echo
if [ "$verdict" = yes ]; then
	echo "ВЫВОД: сервер обрабатывает запросы к этой модели параллельно."
else
	echo "ВЫВОД: сервер ставит запросы В ОЧЕРЕДЬ — потоки на стороне клиента"
	echo "       ничего не ускорят."
fi

# Причину очереди сервер объясняет сам, но только в своём журнале и только
# на той машине, где он работает.
#
# Проверять «локальный ли сервер» по имени хоста нельзя: localhost:11111 сплошь
# и рядом оказывается туннелем ssh на чужую машину, и тогда журнал своей службы
# рассказывает про совсем другой сервер. Замер 26.08.2026: первая же проверка
# по адресу туннеля заявила «OLLAMA_NUM_PARALLEL не задан», тогда как на стенде
# он был выставлен в 4. Поэтому сверяем порт службы с портом запроса.
local_service=no
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet ollama 2>/dev/null; then
	svc_port=$(systemctl show ollama -p Environment 2>/dev/null |
		tr ' ' '\n' | sed -n 's/.*OLLAMA_HOST=[^:]*:\([0-9]*\).*/\1/p' | head -1)
	[ -z "$svc_port" ] && svc_port=11434
	req_host=${URL#*://}
	req_port=${req_host##*:}
	case "$req_host" in
	localhost*|127.0.0.1*) [ "$req_port" = "$svc_port" ] && local_service=yes ;;
	esac
fi

if [ "$verdict" = no ] && [ "$local_service" = no ]; then
	echo
	echo "Причину скажет журнал службы — на той машине, где работает сервер:"
	echo "  journalctl -u ollama --since '-10 min' | grep -i parallel"
	echo "  systemctl show ollama -p Environment | tr ' ' '\n' | grep PARALLEL"
	echo "Строка \"does not currently support parallel requests\" означает, что"
	echo "Ollama отказывает архитектуре модели, и настройка сервера тут ни при чём."
fi

if [ "$verdict" = no ] && [ "$local_service" = yes ] && command -v journalctl >/dev/null 2>&1; then
	echo
	echo "причина (журнал службы за последние 10 минут):"
	warn=$(journalctl -u ollama --since "-10 min" --no-pager 2>/dev/null |
		grep -m1 "does not currently support parallel")
	if [ -n "$warn" ]; then
		echo "  ${warn#*msg=}"
		echo "  Ollama отказывает этой архитектуре в параллельных запросах;"
		echo "  OLLAMA_NUM_PARALLEL на неё не влияет."
	else
		par=$(systemctl show ollama -p Environment 2>/dev/null |
			tr ' ' '\n' | grep OLLAMA_NUM_PARALLEL)
		if [ -z "$par" ]; then
			echo "  OLLAMA_NUM_PARALLEL в настройках службы не задан — по умолчанию"
			echo "  сервер держит один слот. Добавить в override.conf и перезапустить."
		else
			echo "  в службе задано $par, но параллельности нет — смотреть журнал целиком:"
			echo "  journalctl -u ollama --since '-10 min' | grep -i parallel"
		fi
	fi
fi

[ "$verdict" = yes ] && exit 0 || exit 1
