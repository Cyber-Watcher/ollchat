#!/usr/bin/env python3
# Независимая проверка «ollchat --graph-doctor»: те же числа, посчитанные
# по сырым файлам графа, без единой строки кода самой программы.
#
# Смысл: доктор — это суждение о состоянии, и верить ему на слово нельзя.
# Если два независимых счёта расходятся, виноват один из них, и это надо знать.
#
#   ollscripts/graphdoctorcheck.py [коллекция] [имя графа]
# Второй аргумент — именованный граф рядом с рабочим (lab → каталог graph-lab).
import json, pathlib, struct, sys, collections

name = sys.argv[1] if len(sys.argv) > 1 else "books"
gname = sys.argv[2] if len(sys.argv) > 2 else ""
base = pathlib.Path.home()/".local/share/ollchat/kb/collections"/name
gdir = base/("graph-" + gname if gname else "graph")
if not gname and not gdir.exists():
    # У коллекции только именованный граф (lab → graph-lab): берём его, если он один.
    named = sorted(base.glob("graph-*"))
    if len(named) == 1:
        gdir = named[0]

# --- понятия: живые номера и их число -----------------------------------------
ents, merged, broken = {}, set(), 0
for line in (gdir/"entities.jsonl").open(encoding="utf-8"):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except Exception:
        continue
    ents[r["id"]] = r.get("name", "")
mp = gdir/"merges.jsonl"
if mp.exists():
    for line in mp.open(encoding="utf-8"):
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        src = r.get("from", r.get("From"))
        if src is None:
            broken += 1
            continue
        merged.add(src)
live = set(ents) - merged
# 13.09.2026: здесь стояло `if r.get("drop")`, а поля `drop` в merges.jsonl нет
# вовсе (запись: from, to, cos, verdict, alias, why, level, at). Склейки не
# вычитались НИКОГДА, и строка печатала реестр целиком под видом «за вычетом
# склеенных» — то есть выглядела проверкой, ничего не проверяя. Теперь склейка
# считается по полю `from`, а запись без него — видимая поломка, а не тишина.
if broken:
    print(f"ВНИМАНИЕ: записей склеек без поля from: {broken} — формат merges.jsonl изменился")
if mp.exists() and not merged:
    print("ВНИМАНИЕ: merges.jsonl есть, но ни одной склейки не разобрано — проверьте формат")
print(f"понятий (по entities.jsonl, за вычетом склеенных): {len(live)}")
print(f"  из них поглощено склейкой: {len(merged)} (доктор называет это же число)")

# --- связи и упоминания -------------------------------------------------------
edges = (gdir/"edges.log").stat().st_size // 24
ments = (gdir/"mentions.log").stat().st_size // 12
print(f"связей (edges.log / 24 байта): {edges}")
print(f"упоминаний (mentions.log / 12 байт): {ments}")

# --- разбор кусков ------------------------------------------------------------
marks = {}
data = (gdir/"progress.log").read_bytes()
for i in range(len(data)//12):
    d, o, m = struct.unpack_from("<III", data, i*12)
    marks[(d, o)] = m
c = collections.Counter(marks.values())
print(f"размечено кусков: {len(marks)} · с понятиями {c[1]}, пусто {c[2]}, пропущено {c[3]}")

# --- темы ---------------------------------------------------------------------
comm = json.loads((gdir/"communities.json").read_text())
lst = comm["list"]
lvl0 = [x for x in lst if x.get("level") == 0]
cand = [x for x in lvl0 if len(x.get("members") or []) >= 5]
described = [x for x in cand if (x.get("summary") or "").strip()]
in_topic = set()
for x in lvl0:
    in_topic.update(x.get("members") or [])
uncovered = len(live - in_topic)
print(f"тем: {len(lst)} (нижнего уровня {len(lvl0)}), кандидатов {len(cand)}, с описанием {len(described)}")
print(f"понятий вне тем: {uncovered} ({100*uncovered//max(len(live),1)}%)")
print(f"разбиение считалось при понятиях: {comm.get('entities')}")

# --- векторы ------------------------------------------------------------------
vm = json.loads((gdir/"entities.vecmeta").read_text())
print(f"векторов понятий: {vm.get('count')} (модель {vm.get('model')}, размерность {vm.get('dim')})")
cm = json.loads((base/"meta.json").read_text()) if (base/"meta.json").exists() else {}
vmc = json.loads((base/"vectors.meta").read_text()) if (base/"vectors.meta").exists() else {}
if vmc:
    print(f"векторов кусков: {vmc.get('count')} (модель {vmc.get('model')})")
