package uni

import (
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/quizsolver"
)

// Расчёт из quizsolver должен попадать в поле ответа Moodle, а не теряться.
func TestDetAnswerShortAnswer(t *testing.T) {
	q := moodle.QuizQuestion{Slot: 1, QType: "shortanswer", InputName: "q1:1_answer"}
	a, ok := detAnswer(q, quizsolver.Result{Answer: "кость_коса_оса_око"})
	if !ok || a.Text != "кость_коса_оса_око" {
		t.Fatalf("shortanswer: ok=%t answer=%+v", ok, a)
	}
}

func TestDetAnswerMultichoiceByText(t *testing.T) {
	q := moodle.QuizQuestion{Slot: 2, QType: "multichoice", Choices: []moodle.QuizChoice{
		{Name: "q1:2_answer", Value: "0", Text: "пиксель"},
		{Name: "q1:2_answer", Value: "1", Text: "вектор"},
	}}
	a, ok := detAnswer(q, quizsolver.Result{Answer: "Пиксель "})
	if !ok || a.Choice != "0" {
		t.Fatalf("multichoice: ok=%t answer=%+v", ok, a)
	}
}

func TestDetAnswerMatchNotGuessed(t *testing.T) {
	q := moodle.QuizQuestion{Slot: 3, QType: "match", MatchOptions: []moodle.MatchOption{{Name: "q1:3_sub0", Question: "формат"}}}
	if _, ok := detAnswer(q, quizsolver.Result{Answer: "tif"}); ok {
		t.Fatal("match нельзя заполнять текстом расчёта")
	}
}
