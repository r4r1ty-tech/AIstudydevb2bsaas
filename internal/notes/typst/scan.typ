// Служебный шаблон: только собирает формулы конспекта, не рисуя их.
// Go запускает его через `typst eval`, когда основная сборка упала, и ищет формулу,
// на которой падает mitex.
#import "@preview/cmarker:0.1.10"

#cmarker.render(
  read("notes.md"),
  math: (body, block: false) => [#metadata(body) <tex>],
  scope: (
    image: (source, alt: none, ..args) => none,
    link: (dest, body) => if type(dest) == label { body } else { link(dest, body) },
  ),
  raw-typst: false,
)
