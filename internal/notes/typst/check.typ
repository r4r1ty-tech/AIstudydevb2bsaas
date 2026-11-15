// Служебный шаблон: проверяет, что mitex разбирает каждую формулу из formulas.json.
// Go компилирует его для списка формул, чтобы найти те, что ломают сборку.
#import "@preview/mitex:0.2.7": mitex

#for f in json("formulas.json") {
  mitex(f)
}
