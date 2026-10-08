#!/usr/bin/env python3
# Независимая проверка «ollchat --graph-doctor»: те же числа, посчитанные
# по сырым файлам графа, без единой строки кода самой программы.
#
# Смысл: доктор — это суждение о состоянии, и верить ему на слово нельзя.
# Если два независимых счёта расходятся, виноват один из них, и это надо знать.
#
#   ollscripts/graphdoctorcheck.py [коллекция] [имя графа] [--kb-dir КАТАЛОГ] [-c НАСТРОЙКИ]
# Второй аргумент — именованный граф рядом с рабочим (lab → каталог graph-lab).
#
# Каталог базы знаний ищется так же, как его находит сам ollchat: ключ --kb-dir,
# иначе kb.dir из файла настроек (-c, иначе переменная OLLCHAT_CONFIG, иначе
# ~/.config/ollchat/config.toml), иначе ~/.local/share/ollchat/kb. До 07.10.2026
# последний путь был вписан намертво, и на машине с другим kb.dir прибор
# сверял не ту базу — или падал, не найдя её.
import argparse, collections, json, os, pathlib, re, struct, sys, zlib


def config_path(flag):
    if flag:
        return pathlib.Path(flag).expanduser()
    env = os.environ.get("OLLCHAT_CONFIG")
    if env:
        return pathlib.Path(env).expanduser()
    xdg = os.environ.get("XDG_CONFIG_HOME")
    root = pathlib.Path(xdg) if xdg and os.path.isabs(xdg) else pathlib.Path.home()/".config"
    return root/"ollchat"/"config.toml"


def kb_dir_from_config(path):
    """kb.dir из настроек ollchat; None — файла нет или ключ не задан."""
    try:
        text = path.read_text(encoding="utf-8")
    except OSError:
        return None
    try:
        import tomllib
    except ImportError:
        tomllib = None
    if tomllib is not None:
        try:
            value = (tomllib.loads(text).get("kb") or {}).get("dir")
        except tomllib.TOMLDecodeError as e:
            sys.exit(f"настройки {path} не читаются: {e}")
        return value or None
    # Python до 3.11 без tomllib: достаточно строки dir в разделе [kb].
    section = None
    for line in text.splitlines():
        s = line.strip()
        if s.startswith("["):
            section = s.strip("[]").strip()
            continue
        if section == "kb":
            m = re.match(r"""dir\s*=\s*(?:"([^"]*)"|'([^']*)')""", s)
            if m:
                return (m.group(1) if m.group(1) is not None else m.group(2)) or None
    return None


ap = argparse.ArgumentParser(description="сверка ollchat --graph-doctor по сырым файлам графа")
ap.add_argument("name", nargs="?", default="books", help="коллекция (по умолчанию books)")
ap.add_argument("gname", nargs="?", default="", help="имя графа рядом с рабочим (lab → graph-lab)")
ap.add_argument("--kb-dir", help="каталог базы знаний (по умолчанию — kb.dir из настроек ollchat)")
ap.add_argument("-c", "--config", help="файл настроек ollchat (по умолчанию — как у самого ollchat)")
args = ap.parse_args()
name, gname = args.name, args.gname

kb_dir = args.kb_dir or kb_dir_from_config(config_path(args.config)) or "~/.local/share/ollchat/kb"
base = pathlib.Path(os.path.expanduser(kb_dir))/"collections"/name
gdir = base/("graph-" + gname if gname else "graph")
if not gname and not gdir.exists():
    # У коллекции только именованный граф (lab → graph-lab): берём его, если он один.
    named = sorted(base.glob("graph-*"))
    if len(named) == 1:
        gdir = named[0]

# Коллекции без графа — это норма, а не поломка: `projectdocs` по решению
# владельца живёт простым RAG (поиск по тексту и векторам), графа у неё нет
# и не должно быть. До 28.09.2026 прибор в этом случае падал трассировкой
# «FileNotFoundError: …/graph/entities.jsonl», и читать её приходилось как
# аварию — сбивает с толку на ровном месте.
if not base.exists():
    sys.exit(f"коллекции «{name}» нет: {base}")
if not (gdir/"entities.jsonl").exists():
    print(f"у коллекции «{name}» графа нет — сверять нечего.")
    print(f"  ожидался каталог: {gdir}")
    print("  если граф и не задуман (простой RAG, как у projectdocs) — это норма;")
    print("  если он должен быть — его не собрали или коллекция не та.")
    sys.exit(0)


def read_json(path):
    """(данные, None) или (None, почему не прочитан). Нет файла или он битый —
    строка отчёта, а не трассировка: 07.10.2026 прибор падал на графе без тем
    или без векторов понятий, хотя у доктора это обычные строки отчёта."""
    if not path.exists():
        return None, "нет"
    try:
        return json.loads(path.read_text(encoding="utf-8")), None
    except Exception as e:
        return None, f"НЕ ЧИТАЕТСЯ ({e})"


def size_of(path):
    return path.stat().st_size if path.exists() else 0


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
# Круги в журнале (03.10.2026): пара, записанная дважды навстречу (A→B и B→A),
# или тройка по кругу. В круге одно понятие выживает — программа берёт
# наименьший номер, — и вычитать его из живых нельзя. До этой правки вычитались
# все `from` подряд, и счёт сходился с доктором только потому, что доктор
# ошибался так же: оба занижали живые понятия на число кругов.
to = {}
if mp.exists():
    for line in mp.open(encoding="utf-8"):
        try:
            r = json.loads(line)
        except Exception:
            continue
        src, dst = r.get("from", r.get("From")), r.get("to", r.get("To"))
        if src and dst and src != dst:
            to[src] = dst
survivors = set()
for start in sorted(to):
    seen, cur = [], start
    while cur in to and cur not in seen:
        seen.append(cur)
        cur = to[cur]
    if cur in seen:                      # цепочка пришла в саму себя — круг
        survivors.add(min(seen[seen.index(cur):]))
merged -= survivors
if survivors:
    print(f"кругов в журнале склеек: {len(survivors)} (в каждом одно понятие выживает)")
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
edges = size_of(gdir/"edges.log") // 24
ments = size_of(gdir/"mentions.log") // 12
print(f"связей (edges.log / 24 байта): {edges}")
print(f"упоминаний (mentions.log / 12 байт): {ments}")

# --- разбор кусков ------------------------------------------------------------
# Тем же правилом, что доктор после правки 07.10.2026: по кускам ЖИВЫХ книг,
# а не по числу отметок в журнале. Прежде прибор печатал «размечено кусков»
# по всему журналу — вместе с отметками удалённых книг — и сходился с доктором
# потому, что доктор ошибался так же: оба называли разобранными куски книг,
# которых в коллекции давно нет.
marks = {}
data = (gdir/"progress.log").read_bytes() if (gdir/"progress.log").exists() else b""
for i in range(len(data)//12):
    d, o, m = struct.unpack_from("<III", data, i*12)
    marks[(d, o)] = m

# Книги — как их видит ollchat: последняя запись о пути побеждает,
# удалённые перечислены в deleted.ids.
by_path = {}
if (base/"docs.jsonl").exists():
    for line in (base/"docs.jsonl").open(encoding="utf-8"):
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        by_path[r.get("path", "")] = r.get("id")
deleted = set()
if (base/"deleted.ids").exists():
    for tok in (base/"deleted.ids").read_text().split():
        if tok.isdigit():
            deleted.add(int(tok))
alive_books = {i for i in by_path.values() if i is not None and i not in deleted}

# Куски — из chunks.idx (записи по 32 байта: книга, номер, страницы, признаки…),
# как их обходит сборка: куски удалённых книг пропускаются. Оглавление (16)
# и список литературы (32) сборка не берёт; «служебный» без такого признака —
# кусок, забытый чисткой, — она берёт снова.
TOC, REFS = 16, 32
idx = (base/"chunks.idx").read_bytes() if (base/"chunks.idx").exists() else b""
if len(idx) % 32:
    print(f"ВНИМАНИЕ: chunks.idx не делится на записи по 32 байта ({len(idx)} байт) — ollchat такую коллекцию не откроет")
total = pending = again = 0
kinds = collections.Counter()
counted = set()
for i in range(len(idx)//32):
    d, o = struct.unpack_from("<II", idx, i*32)
    flags = struct.unpack_from("<H", idx, i*32 + 12)[0]
    if d in deleted:
        continue
    total += 1
    m = marks.get((d, o))
    if m in (1, 2, 3, 4):
        kinds[m] += 1
        counted.add((d, o))
    if not flags & (TOC | REFS) and (m is None or m == 4):
        pending += 1
        if m == 4:
            again += 1
marked = sum(kinds.values())
gone = {k for k in marks if k[0] not in alive_books}
other = len(marks) - len(counted | gone)
print(f"разобрано кусков {marked} из {total} ({100*marked//max(total, 1)}%), осталось {pending}")
print(f"  с понятиями {kinds[1]}, пустых {kinds[2]}, не разобрала модель {kinds[3]}, служебных {kinds[4]}")
if again:
    print(f"  из разобранных сборка возьмёт снова {again} («служебные» без признака)")
if gone:
    print(f"  отметок книг, которых в коллекции НЕТ: {len(gone)} (в {len({k[0] for k in gone})} книгах)")
if other:
    print(f"  отметок у кусков, которых в коллекции нет, при живой книге: {other}")
print(f"  (числа — как у доктора, по кускам живых книг; отметок в журнале всего {len(marks)})")

# --- журналы на сдвиг записей -------------------------------------------------
# До 07.10.2026 после жёсткого обрыва (kill -9) в хвосте двоичного журнала
# оставался обрывок записи, а новые записи ложились ПОСЛЕ него: всё дописанное
# читается со сдвигом — мусорные номера понятий, книг, типов. Программа
# принимает любые значения, поэтому сдвиг ищется здесь по формату записей,
# своим разбором, без кода программы:
#   mentions.log, 12 байт: понятие, книга, кусок (u32 LE);
#   edges.log, 24 байта: начало, конец (u32), тип (u8, 1..7), три нулевых
#     байта, вес (f32), книга, кусок (u32);
#   progress.log, 12 байт: книга, кусок, признак (u32, 1..4).
# Три счёта, как у доктора:
#   недопустимые поля — признак сдвига: понятие 0 или дальше границы,
#     у связи тип вне 1..7, ненулевое дополнение, концы 0 или равные,
#     у отметки признак вне 1..4;
#   кусок без отметки — упоминание или связь, чей кусок не отмечен в
#     progress.log: при сдвиге упоминаний на 4 и 8 байт (настоящие обрывки:
#     65536 % 12 = 4) поля остаются допустимыми, а пара «книга, кусок» — мусор;
#   книга не из реестра — не сдвиг (перечитанные книги), считается отдельно.
# Запись с недопустимыми полями во второй и третий счёт не идёт. Граница
# номера понятия — своим чтением реестра: наибольший номер в entities.jsonl,
# отметка entities.maxid и число векторов по паспорту (если паспорт принят:
# тот же признак формата, данных не меньше, сумма сходится), плюс 65536.
def entity_limit():
    top = max((i for i in ents if isinstance(i, int)), default=0)
    mx, _ = read_json(gdir/"entities.maxid")
    if isinstance(mx, dict) and isinstance(mx.get("max_id"), int):
        top = max(top, mx["max_id"])
    vm, _ = read_json(gdir/"entities.vecmeta")
    if isinstance(vm, dict) and vm.get("magic") == "OLLGRV1":
        count, dim = vm.get("count") or 0, vm.get("dim") or 0
        want = count*dim
        vec = gdir/"entities.vec"
        # Паспорт важен, только когда он дальше реестра: тогда его и сверяем.
        if count > top and want > 0 and size_of(vec) >= want:
            with vec.open("rb") as fh:
                body = fh.read(want)
            if not vm.get("crc") or zlib.crc32(body) == vm["crc"]:
                top = count
    return top + 65536


def new_check(fname):
    return {"file": fname, "records": 0, "tail": 0, "bad": 0, "bad_first": -1, "bad_last": -1,
            "unmarked": 0, "unmarked_first": -1, "unknown": 0}


def records_of(fname, size, fmt):
    """(отчёт, записи с их смещениями) — целые записи, хвост отдельно."""
    c = new_check(fname)
    p = gdir/fname
    raw = p.read_bytes() if p.exists() else b""
    n = len(raw)//size
    c["records"], c["tail"] = n, len(raw) - n*size
    return c, ((i*size, rec) for i, rec in enumerate(struct.iter_unpack(fmt, memoryview(raw)[:n*size])))


def mark_bad(c, off):
    if c["bad"] == 0:
        c["bad_first"] = off
    c["bad"] += 1
    c["bad_last"] = off


def mark_chunk(c, off, doc, ord_):
    if (doc, ord_) not in good_marks:
        if c["unmarked"] == 0:
            c["unmarked_first"] = off
        c["unmarked"] += 1
    if doc not in registry:
        c["unknown"] += 1


def after(c, n, first, size):
    return c["records"] - first//size if n else 0


limit = entity_limit()
# Реестр — все записи о книгах, с удалёнными: отметка удалённой книги — след,
# а не мусор. Запись без номера у программы — книга 0.
registry = {v if v is not None else 0 for v in by_path.values()}
good_marks = set()
prog, recs = records_of("progress.log", 12, "<III")
for off, (d, o, m) in recs:
    if not 1 <= m <= 4:
        mark_bad(prog, off)
        continue
    good_marks.add((d, o))
    if d not in registry:
        prog["unknown"] += 1
ment, recs = records_of("mentions.log", 12, "<III")
for off, (e, d, o) in recs:
    if e == 0 or e > limit:
        mark_bad(ment, off)
        continue
    mark_chunk(ment, off, d, o)
edge, recs = records_of("edges.log", 24, "<IIBBBBxxxxII")
for off, (s, t, typ, p0, p1, p2, d, o) in recs:
    if s == 0 or s > limit or t == 0 or t > limit or s == t or not 1 <= typ <= 7 or p0 | p1 | p2:
        mark_bad(edge, off)
        continue
    if d == 0:                           # связь без подтверждения: кусок не назван
        continue
    mark_chunk(edge, off, d, o)

sizes = {"mentions.log": 12, "edges.log": 24, "progress.log": 12}
journals = [ment, edge, prog]
bad = sum(c["bad"] for c in journals)
print(f"журналы: записей с недопустимыми полями {bad} "
      f"(mentions.log {ment['bad']}, edges.log {edge['bad']}, progress.log {prog['bad']})")
for c in journals:
    if c["bad"]:
        a = after(c, c["bad"], c["bad_first"], sizes[c["file"]])
        print(f"  ВНИМАНИЕ: {c['file']} читается со сдвигом: первая недопустимая запись на смещении {c['bad_first']} байт, "
              f"последняя на {c['bad_last']}; после первой недопустимых {c['bad']} из {a} ({100*c['bad']//max(a, 1)}%)")
unmarked = ment["unmarked"] + edge["unmarked"]
print(f"журналы: записей из кусков без отметки разбора {unmarked} "
      f"(mentions.log {ment['unmarked']}, edges.log {edge['unmarked']})")
for c in (ment, edge):
    if c["unmarked"]:
        a = after(c, c["unmarked"], c["unmarked_first"], sizes[c["file"]])
        print(f"  {c['file']}: первая на смещении {c['unmarked_first']} байт, "
              f"после неё таких {c['unmarked']} из {a} ({100*c['unmarked']//max(a, 1)}%)")
unknown = sum(c["unknown"] for c in journals)
if unknown:
    print(f"журналы: записей с книгой не из реестра {unknown} "
          f"(mentions.log {ment['unknown']}, edges.log {edge['unknown']}, progress.log {prog['unknown']}) — не сдвиг")
for c in journals:
    if c["tail"]:
        print(f"журналы: нецелый хвост {c['file']} {c['tail']} байт — оборванная запись")

# --- темы ---------------------------------------------------------------------
comm, why = read_json(gdir/"communities.json")
if comm is None:
    print(f"темы: communities.json {why}" + (" — не размечены" if why == "нет" else ""))
else:
    lst = comm.get("list") or []
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
vm, why = read_json(gdir/"entities.vecmeta")
if vm is None:
    print(f"векторы понятий: entities.vecmeta {why}" + (" — не считались" if why == "нет" else ""))
else:
    print(f"векторов понятий: {vm.get('count')} (модель {vm.get('model')}, размерность {vm.get('dim')})")
vmc, why = read_json(base/"vectors.meta")
if vmc:
    print(f"векторов кусков: {vmc.get('count')} (модель {vmc.get('model')})")
elif why != "нет":
    print(f"векторы кусков: vectors.meta {why}")
