#!/usr/bin/env python3
"""Logo do cais em pixel art, redesenhada a partir da referencia (Logo CAIS
com Guindaste Portuario): guindaste de uma perna com a diagonal, a lanca,
a garra com o conteiner amarelo, a pilha (verde em cima; azul, verde, azul),
o pier com quatro estacas e a onda. Um grid gera o SVG e o terminal
(meio-bloco colorido). Saidas: cais.svg/.ans (com o nome) e
cais-icone.svg/.ans (so a figura), e o mapa de cada uma pro app
(internal/ui/logo/*.txt: um caractere por pixel, embutido no binario).

Rode daqui: python3 gen.py (gera tudo nesta pasta e o mapa no
internal/ui/logo)."""
import os

P = {
    "o": "#FFA500", "oD": "#E07B00",          # guindaste
    "y": "#FBBF24", "yD": "#8A6508",          # conteiner amarelo
    "g": "#3DB559", "gD": "#1D7A36",          # verde
    "b": "#1E90F0", "bD": "#0B5AA6",          # azul
    "p": "#A3ACB9", "pD": "#7C8696",          # pier, cabo, garra
    "w": "#1E90F0",                           # onda
    "t": "#FFFFFF",                           # texto
    "bg": "#060E25",                          # fundo da referencia (so no SVG)
}


class Grid:
    def __init__(self, w, h):
        self.w, self.h = w, h
        self.px = [[None] * w for _ in range(h)]

    def set(self, x, y, c):
        if 0 <= x < self.w and 0 <= y < self.h:
            self.px[y][x] = c

    def rect(self, x, y, w, h, c):
        for j in range(y, y + h):
            for i in range(x, x + w):
                self.set(i, j, c)

    def container(self, x, y, w, h, c):
        """cantos arredondados e nervuras escuras, como na referencia"""
        self.rect(x, y, w, h, c)
        for cx, cy in ((x, y), (x + w - 1, y), (x, y + h - 1), (x + w - 1, y + h - 1)):
            self.set(cx, cy, None)
        n = 4
        span = w - 4
        for k in range(n):
            bx = x + 2 + round(k * (span - 1) / (n - 1))
            self.rect(bx, y + 2, 1, h - 4, c + "D")


def scene(g, ox=0, oy=0):
    R = lambda x, y, w, h, c: g.rect(ox + x, oy + y, w, h, c)
    S = lambda x, y, c: g.set(ox + x, oy + y, c)
    # lanca e o topo inclinado
    R(1, 3, 54, 3, "o")
    R(1, 6, 54, 1, "oD")
    S(1, 3, None)
    S(54, 3, None)
    for row, start in ((0, 13), (1, 12), (2, 11)):
        R(start, row, 20 - start, 1, "o")
    # perna dupla com a travessa e a diagonal
    R(5, 7, 3, 27, "o")
    R(13, 7, 3, 27, "o")
    R(5, 7, 1, 27, "oD")
    R(5, 20, 11, 2, "o")
    # diagonal entre as pernas: do pe da esquerda (na travessa) ao topo da direita
    for y in range(8, 20):
        x = 8 + round((19 - y) * 5 / 11)
        R(x, y, 3, 1, "o")
    R(5, 7, 1, 27, "oD")
    R(3, 33, 16, 2, "o")
    R(3, 34, 16, 1, "oD")
    S(3, 33, None)
    S(18, 33, None)
    # cabo, garra e o conteiner amarelo
    R(43, 7, 2, 5, "p")
    R(41, 12, 6, 2, "p")
    S(40, 14, "pD")
    S(47, 14, "pD")
    g.container(ox + 37, oy + 15, 14, 7, "y")
    # pilha
    g.container(ox + 24, oy + 21, 12, 7, "g")
    g.container(ox + 19, oy + 28, 11, 7, "b")
    g.container(ox + 31, oy + 28, 11, 7, "g")
    g.container(ox + 43, oy + 28, 11, 7, "b")
    # pier e estacas
    R(0, 35, 56, 2, "p")
    S(0, 35, None)
    S(55, 35, None)
    for x in (5, 19, 33, 47):
        R(x, 37, 2, 3, "pD")
    # onda
    import math
    prev = None
    for i in range(1, 55):
        y = 41 + round(math.sin(i / 2.6) * 1.2)
        R(i, y, 1, 2, "w")
        if prev is not None and abs(prev - y) > 1:  # sem buraco na subida
            R(i, min(prev, y) + 1, 1, 1, "w")
        prev = y


# letras do nome, 11x11, traco de 3 (cantos cortados como na referencia)
FONT = {
    "C": [
        "  ████████ ",
        " █████████ ",
        "████       ",
        "███        ",
        "███        ",
        "███        ",
        "███        ",
        "███        ",
        "████       ",
        " █████████ ",
        "  ████████ ",
    ],
    "A": [
        "    ███    ",
        "   █████   ",
        "   ██ ██   ",
        "  ███ ███  ",
        "  ██   ██  ",
        " ███   ███ ",
        " █████████ ",
        " █████████ ",
        "███     ███",
        "███     ███",
        "███     ███",
    ],
    "I": [
        "███",
        "███",
        "███",
        "███",
        "███",
        "███",
        "███",
        "███",
        "███",
        "███",
        "███",
    ],
    "S": [
        "  █████████",
        " ██████████",
        "████       ",
        "███        ",
        " ████████  ",
        "  █████████",
        "        ███",
        "        ███",
        "       ████",
        "██████████ ",
        "█████████  ",
    ],
}


def text(g, word, x, y, gap=3):
    for ch in word:
        rows = FONT[ch]
        for j, row in enumerate(rows):
            for i, c in enumerate(row):
                if c != " ":
                    g.set(x + i, y + j, "t")
        x += len(rows[0]) + gap


def svg(g, bg):
    out = [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {g.w} {g.h}" '
           f'width="{g.w*12}" height="{g.h*12}" shape-rendering="crispEdges">']
    if bg:
        out.append(f'<rect width="{g.w}" height="{g.h}" fill="{P["bg"]}"/>')
    for j in range(g.h):
        i = 0
        while i < g.w:
            c, k = g.px[j][i], i
            while k < g.w and g.px[j][k] == c:
                k += 1
            if c:
                out.append(f'<rect x="{i}" y="{j}" width="{k-i}" height="1" fill="{P[c]}"/>')
            i = k
    return "\n".join(out + ["</svg>"]) + "\n"


def rgb(c):
    h = P[c].lstrip("#")
    return ";".join(str(int(h[i:i + 2], 16)) for i in (0, 2, 4))


def ansi(g):
    lines = []
    for j in range(0, g.h, 2):
        s = ""
        for i in range(g.w):
            top = g.px[j][i]
            bot = g.px[j + 1][i] if j + 1 < g.h else None
            if top is None and bot is None:
                s += "\x1b[0m "
            elif bot is None:
                s += f"\x1b[0;38;2;{rgb(top)}m▀"
            elif top is None:
                s += f"\x1b[0;38;2;{rgb(bot)}m▄"
            else:
                s += f"\x1b[38;2;{rgb(top)};48;2;{rgb(bot)}m▀"
        lines.append(s.rstrip() + "\x1b[0m")
    return "\n".join(lines) + "\n"


# so a figura
icon = Grid(56, 44)
scene(icon)
# figura e o nome embaixo, centralizado
full = Grid(56, 58)
scene(full)
word_w = sum(len(FONT[c][0]) for c in "CAIS") + 3 * 3
text(full, "CAIS", (56 - word_w) // 2, 47)

# mapa pro app: a cor de cada pixel numa letra (maiuscula = tom escuro)
KEY = {"o": "o", "oD": "O", "y": "y", "yD": "Y", "g": "g", "gD": "G", "b": "b", "bD": "B",
       "p": "p", "pD": "P", "w": "w", "t": "t", None: "."}


def charmap(g):
    return "\n".join("".join(KEY[c] for c in row) for row in g.px) + "\n"


here = os.path.dirname(os.path.abspath(__file__))
app = os.path.join(here, "..", "..", "internal", "ui", "logo")
for name, g in (("cais-icone", icon), ("cais", full)):
    open(os.path.join(here, name + ".svg"), "w").write(svg(g, bg=False))
    open(os.path.join(here, name + "-fundo.svg"), "w").write(svg(g, bg=True))
    open(os.path.join(app, name + ".txt"), "w").write(charmap(g))
