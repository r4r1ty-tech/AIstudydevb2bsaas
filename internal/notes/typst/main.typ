// Шаблон конспекта. Markdown разбирает пакет cmarker, LaTeX-формулы переводит mitex.
// Заголовок и исходник лежат рядом файлами (title.txt, notes.md) и читаются через read(),
// поэтому текст конспекта никогда не становится кодом шаблона: экранировать его не нужно.
#import "@preview/cmarker:0.1.10"
#import "@preview/mitex:0.2.7": mitex

// Формулы, которые mitex не разобрал. Список готовит Go (bad.json) и только при сбое сборки.
// Такие формулы печатаем исходником красным, как KaTeX с throwOnError: false.
#let bad-formulas = json("bad.json")

#let formula(body, block: false) = {
  if body in bad-formulas {
    let src = if block { "$$" + body + "$$" } else { "$" + body + "$" }
    text(fill: rgb("#b00020"), raw(src, block: block))
  } else {
    mitex(body, block: block)
  }
}

// Внешние картинки и файлы, которых нет рядом, роняют сборку typst. Вместо картинки печатаем подпись.
#let no-image(source, alt: none, ..args) = {
  let label = if alt == none or alt == "" { "изображение" } else { "изображение: " + alt }
  text(fill: luma(90), style: "italic")[\[#label\]]
}

// Ссылки на якоря Markdown ([см.](#формулы)) дают метку Typst. Метки может не быть, и тогда typst
// роняет весь PDF. Якорные ссылки печатаем обычным текстом, внешние оставляем ссылками.
#let safe-link(dest, body) = if type(dest) == label { body } else { link(dest, body) }

#set document(title: read("title.txt"))
#set page(paper: "a4", margin: (x: 2cm, y: 2.2cm))
#set text(font: "Libertinus Serif", size: 11pt, lang: "ru")
#set par(justify: true)
#set heading(numbering: none)
#show heading.where(level: 1): set text(size: 16pt)
#show heading.where(level: 2): set text(size: 13.5pt)
#show heading.where(level: 3): set text(size: 12pt)
#set table(stroke: 0.5pt + luma(140), inset: 6pt)
#show raw.where(block: true): block.with(fill: luma(244), inset: 8pt, radius: 3pt, width: 100%)

#text(size: 20pt, weight: "bold", read("title.txt"))
#v(0.6em)

#cmarker.render(
  read("notes.md"),
  math: formula,
  scope: (image: no-image, link: safe-link),
  raw-typst: false,
)
