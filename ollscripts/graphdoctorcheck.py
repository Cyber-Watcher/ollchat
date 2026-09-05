#!/usr/bin/env python3
# Независимая проверка «ollchat --graph-doctor»: те же числа, посчитанные
# по сырым файлам графа, без единой строки кода самой программы.
#
# Смысл: доктор — это суждение о состоянии, и верить ему на слово нельзя.
# Если два независимых счёта расходятся, виноват один из них, и это надо знать.
#
#   ollscripts/graphdoctorcheck.py [коллекция]
import json, pathlib, struct, sys, collections

name = sys.argv[1] if len(sys.argv) > 1 else "books"
base = pathlib.Path.home()/".local/share/ollchat/kb/collections"/name
gdir = base/"graph"

# --- понятия: живые номера и их число -----------------------------------------
ents, merged = {}, set()
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
        if r.get("drop"):
            merged.add(r.get("from") or r.get("From"))
live = set(ents) - merged
print(f"понятий (по entities.jsonl, за вычетом склеенных): {len(live)}")

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
