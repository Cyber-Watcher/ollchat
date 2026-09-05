#!/usr/bin/env python3
"""Собирает резервные шрифты для сборки PDF.

Зачем: в Liberation нет знака рубля (U+20BD), галочек, звёзд, двойных и жирных
рамок псевдографики. Модель их пишет, а в документе на их месте оказывались
пустые прямоугольники. Резервный шрифт подставляется по одному символу — вид
основного текста от этого не меняется.

Что делает: берёт из DejaVu только те знаки нужных блоков Unicode, которых нет
в соответствующем Liberation, и сохраняет три урезанных шрифта. Гарнитура
переименовывается: лицензия Bitstream Vera прямо запрещает распространять
изменённые шрифты под именами DejaVu и Bitstream Vera.

Запускается вручную и редко — при сборке проекта не нужен ни он, ни fontTools:

    python3 internal/pdfout/fonts/make_fallback.py

Проверить результат: go test ./internal/pdfout/ -run Fallback
"""

import os
import sys

try:
    from fontTools import subset
    from fontTools.ttLib import TTFont
except ImportError:
    sys.exit("нужен fontTools: pip install fonttools")

HERE = os.path.dirname(os.path.abspath(__file__))
DEJAVU = "/usr/share/fonts/truetype/dejavu"

# Блоки, которых стоит ждать от модели в техническом ответе. Кириллица,
# латиница, греческий и типографика тут не нужны — они есть в Liberation.
BLOCKS = [
    (0x2010, 0x205F, "типографские знаки"),
    (0x20A0, 0x20BF, "валюты — ради ₽ всё и затевалось"),
    (0x2100, 0x214F, "буквоподобные: №, ℃, ℹ"),
    (0x2190, 0x21FF, "стрелки"),
    (0x2200, 0x22FF, "математика"),
    (0x2300, 0x23FF, "технические: ⌘, ⏎, ⏱"),
    (0x2500, 0x257F, "рамки — двойные и жирные, одинарные есть в Liberation"),
    (0x2580, 0x259F, "блоки заливки: ░▒▓█"),
    (0x25A0, 0x25FF, "геометрия"),
    (0x2600, 0x26FF, "разное: ☑, ☺, ⚠"),
    (0x2700, 0x27BF, "дингбаты: ✓, ✗, ✦"),
    (0x2B00, 0x2BFF, "дополнительные стрелки и звёзды"),
]

# Резерв повторяет три начертания основного шрифта. Курсив своего резерва
# не имеет: знак валюты в наклонном тексте берётся прямой, и это лучше,
# чем ещё 80 КБ ради разницы, которую никто не заметит.
JOBS = [
    ("LiberationSans-Regular.ttf", "DejaVuSans.ttf", "OllchatFallback-Regular.ttf", "Regular"),
    ("LiberationSans-Bold.ttf", "DejaVuSans-Bold.ttf", "OllchatFallback-Bold.ttf", "Bold"),
    ("LiberationMono-Regular.ttf", "DejaVuSansMono.ttf", "OllchatFallback-Mono.ttf", "Regular"),
]

FAMILY = "ollchat Fallback"


def coverage(path):
    """Множество кодов, которые шрифт умеет рисовать."""
    font = TTFont(path, lazy=True)
    cps = set()
    for table in font["cmap"].tables:
        if table.isUnicode():
            cps |= set(table.cmap.keys())
    font.close()
    return cps


def rename(font, subfamily):
    """Переименовывает гарнитуру: производный шрифт не вправе зваться DejaVu."""
    full = FAMILY if subfamily == "Regular" else f"{FAMILY} {subfamily}"
    ps = full.replace(" ", "")
    for record in font["name"].names:
        if record.nameID == 1:
            record.string = FAMILY
        elif record.nameID == 2:
            record.string = subfamily
        elif record.nameID == 4:
            record.string = full
        elif record.nameID == 6:
            record.string = ps
        elif record.nameID == 16:
            record.string = FAMILY
        elif record.nameID == 17:
            record.string = subfamily


def main():
    wanted = set()
    for start, end, _ in BLOCKS:
        wanted |= set(range(start, end + 1))

    total = 0
    for base_name, dejavu_name, out_name, subfamily in JOBS:
        base = os.path.join(HERE, base_name)
        dejavu = os.path.join(DEJAVU, dejavu_name)
        if not os.path.exists(dejavu):
            sys.exit(f"нет файла {dejavu} — поставьте пакет fonts-dejavu-core")

        need = sorted((wanted - coverage(base)) & coverage(dejavu))

        font = TTFont(dejavu)
        opts = subset.Options(layout_features=[], notdef_outline=True, name_IDs="*", name_languages="*")
        sub = subset.Subsetter(options=opts)
        sub.populate(unicodes=need)
        sub.subset(font)
        rename(font, subfamily)

        out = os.path.join(HERE, out_name)
        font.save(out)
        font.close()

        size = os.path.getsize(out)
        total += size
        print(f"{out_name:32} {len(need):4} знаков  {size // 1024:4} КБ")

    print(f"{'итого':32} {'':9} {total // 1024:4} КБ")


if __name__ == "__main__":
    main()
