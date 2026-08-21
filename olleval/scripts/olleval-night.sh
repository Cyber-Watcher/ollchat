#!/usr/bin/env bash
# Прогон целиком: дождаться свободной карты, закрыть сервер, прогнать наборы,
# выгрузить модели, вернуть сервер людям.
#
# Запускается таймером systemd раз в четверть часа и сам решает, его ли сейчас
# время: вне окна прогонов выходит сразу, при живом прогоне — тоже. Так одна
# и та же обвязка годится и для ночи по будням, и для суточных выходных окон,
# и заодно поднимает прогон обратно, если он почему-то умер посреди окна.
set -uo pipefail

# Расписание и правила проверки живут в конфиге (~/ollevals/olleval.toml),
# а не здесь: держать те же числа в скрипте значит однажды развести их
# с настройками прогона.
ROOT="${OLLEVAL_ROOT:-$(olleval config --get root)}"
TZONE="$(olleval config --get schedule.timezone)"
NIGHT="${OLLEVAL_NIGHT:-$(TZ=$TZONE date +%F)}"
SUITE="${OLLEVAL_SUITE:-$(olleval config --get run.suites)}"
# Предел задач берётся из текущего окна самим olleval: по будням это 06:45,
# в выходные — конец промежутка из настроек.
CHECKS="$(olleval config --get guard.free_checks)"
POLL="$(olleval config --get guard.poll)"
WAIT="${OLLEVAL_WAIT:-$(olleval config --get schedule.wait)}"
RUNDIR="$ROOT/runs/$NIGHT"
mkdir -p "$RUNDIR" "$ROOT/logs" "$ROOT/state"
LOG="$RUNDIR/run.log"
# Общий журнал решений: по нему видно за все дни разом, почему прогон начался
# или не начался. Журнал ночи (run.log) подробный, но лежит внутри своей ночи —
# для вопроса «а что было позавчера» это неудобно.
NIGHTS="$ROOT/logs/nights.log"

say() { echo "$(TZ=$TZONE date '+%F %H:%M:%S %Z')  $*" | tee -a "$LOG"; }
decide() { echo "$(TZ=$TZONE date '+%F %H:%M:%S %Z')  $NIGHT  $*" >> "$NIGHTS"; }

# Первым делом — окно. Скрипт закрывает Ollama на localhost, и запуск среди
# рабочего дня отбирает сервер у людей. Один такой случай уже был: пробный
# запуск в 09:32 совпал с освобождением карты, прогон принял это за законный
# старт и закрыл сервер. OLLEVAL_FORCE=1 снимает проверку осознанно.
if [ "${OLLEVAL_FORCE:-}" != "1" ] && ! olleval window --quiet; then
  decide "не начинаю: вне окна прогонов"
  exit 0
fi

# Второй прогон поверх идущего испортил бы обоим замер: карта одна.
if olleval running --quiet; then
  decide "не начинаю: прогон уже идёт ($(olleval running))"
  exit 0
fi

say "прогон $NIGHT начинается, наборы: $SUITE, $(olleval window)"

# 1. Правило номер один: ждём три свободные проверки подряд, опрос раз в минуту.
# --start-service: службу гасят под обучение и забывают включить обратно;
# после подтверждённого простоя карты поднимаем её сами.
if ! olleval guard --free-checks "$CHECKS" --poll "$POLL" --wait "$WAIT" --start-service 2>&1 | tee -a "$LOG"; then
  say "карта так и не освободилась — прогон пропускается"
  decide "пропуск: карта занята (подробности в logs/guard.log)"
  exit 0
fi
decide "карта свободна, начинаю"

# 2. Изоляция сервера. Возврат обязан случиться при любом выходе.
trap 'say "возвращаю сервер в общий доступ"; olleval-isolate.sh off 2>&1 | tee -a "$LOG"' EXIT INT TERM
if ! olleval-isolate.sh on 2>&1 | tee -a "$LOG"; then
  say "изоляция не удалась — прогон не начинается"
  decide "пропуск: не удалось закрыть сервер на localhost"
  exit 0
fi

# 3. Наблюдение за картой: по этому файлу потом видно, не выехала ли модель
#    в оперативную память — цифры ночи после такого выезда ничего не стоят.
( while sleep 30; do
    echo "$(date -Is) $(nvidia-smi --query-gpu=memory.used,utilization.gpu --format=csv,noheader)"
  done ) >> "$RUNDIR/gpu.log" 2>&1 &
WATCH=$!
trap 'kill $WATCH 2>/dev/null; say "возвращаю сервер в общий доступ"; olleval-isolate.sh off 2>&1 | tee -a "$LOG"' EXIT INT TERM

# 4. Сам прогон. Карта уже проверена, поэтому ждать повторно незачем.
olleval run --suite "$SUITE" --night "$NIGHT" --free-checks 1 2>&1 | tee -a "$LOG"

say "прогон закончен"
decide "прогон закончен"
olleval report --night "$NIGHT" 2>&1 | tee -a "$LOG"
