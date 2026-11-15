package store

import (
	"testing"
	"time"
)

func TestQuizAnswerSaveAndLookupCountsHits(t *testing.T) {
	st := openTestLLMStore(t)

	if _, ok, err := st.LookupAnswer("h1"); err != nil || ok {
		t.Fatalf("пустая база: ok=%t err=%v", ok, err)
	}

	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	err := st.SaveVerifiedAnswer(VerifiedAnswer{
		Course: "Теория информации", CMID: 61422, QHash: "h1", QType: "multichoice",
		QuestionText: "Выберите коды", AnswerJSON: `{"choices":["11000100"]}`,
		VerifiedAt: at, SourceTG: 10,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	for i := int64(1); i <= 2; i++ {
		a, ok, err := st.LookupAnswer("h1")
		if err != nil || !ok {
			t.Fatalf("lookup %d: ok=%t err=%v", i, ok, err)
		}
		if a.Hits != i {
			t.Errorf("hits = %d, ожидали %d", a.Hits, i)
		}
		if a.AnswerJSON != `{"choices":["11000100"]}` || a.Course != "Теория информации" || a.CMID != 61422 {
			t.Errorf("запись: %+v", a)
		}
		if !a.VerifiedAt.Equal(at) {
			t.Errorf("verified_at = %v, ожидали %v", a.VerifiedAt, at)
		}
	}
}

func TestQuizAnswerUpsertReplacesAnswerKeepsHits(t *testing.T) {
	st := openTestLLMStore(t)
	base := VerifiedAnswer{QHash: "h2", QType: "shortanswer", QuestionText: "Единица", AnswerJSON: `{"text":"бит"}`, SourceTG: 1}
	if err := st.SaveVerifiedAnswer(base); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LookupAnswer("h2"); err != nil {
		t.Fatal(err)
	}

	base.AnswerJSON = `{"text":"байт"}`
	base.SourceTG = 2
	if err := st.SaveVerifiedAnswer(base); err != nil {
		t.Fatalf("повторная запись: %v", err)
	}
	a, ok, err := st.LookupAnswer("h2")
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%t err=%v", ok, err)
	}
	if a.AnswerJSON != `{"text":"байт"}` || a.SourceTG != 2 {
		t.Errorf("ответ не заменился: %+v", a)
	}
	if a.Hits != 2 {
		t.Errorf("hits = %d, ожидали 2 (счётчик сохраняется при замене)", a.Hits)
	}
}

func TestQuizAnswerRejectsEmptyKeyOrAnswer(t *testing.T) {
	st := openTestLLMStore(t)
	if err := st.SaveVerifiedAnswer(VerifiedAnswer{AnswerJSON: `{"text":"x"}`}); err == nil {
		t.Error("пустой qhash должен давать ошибку")
	}
	if err := st.SaveVerifiedAnswer(VerifiedAnswer{QHash: "h3"}); err == nil {
		t.Error("пустой ответ должен давать ошибку")
	}
}

func TestQuizPeekDoesNotCountHits(t *testing.T) {
	st := openTestLLMStore(t)
	if err := st.SaveVerifiedAnswer(VerifiedAnswer{QHash: "p1", AnswerJSON: `{"text":"бит"}`}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		a, ok, err := st.PeekAnswer("p1")
		if err != nil || !ok || a.AnswerJSON != `{"text":"бит"}` {
			t.Fatalf("peek: ok=%t err=%v a=%+v", ok, err, a)
		}
	}
	if a, _, _ := st.PeekAnswer("p1"); a.Hits != 0 {
		t.Errorf("peek увеличил hits до %d", a.Hits)
	}
	if _, ok, err := st.PeekAnswer("нет"); err != nil || ok {
		t.Errorf("peek несуществующего: ok=%t err=%v", ok, err)
	}
}

func TestQuizWrongAnswersStoredUniquely(t *testing.T) {
	st := openTestLLMStore(t)
	list, err := st.ListWrongAnswers("w1")
	if err != nil || len(list) != 0 {
		t.Fatalf("пусто: %v %v", list, err)
	}
	for _, j := range []string{`{"choice":"0"}`, `{"choice":"1"}`, `{"choice":"0"}`} {
		if err := st.SaveWrongAnswer("w1", j, 500, 10); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	list, err = st.ListWrongAnswers("w1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0] != `{"choice":"0"}` || list[1] != `{"choice":"1"}` {
		t.Errorf("неверные ответы = %v, ожидали два уникальных по порядку записи", list)
	}
	if err := st.SaveWrongAnswer("", `{"text":"x"}`, 1, 1); err == nil {
		t.Error("пустой qhash должен давать ошибку")
	}
}

func TestQuizDeleteAndCountHelpers(t *testing.T) {
	st := openTestLLMStore(t)
	if err := st.SaveVerifiedAnswer(VerifiedAnswer{QHash: "d1", CMID: 900, AnswerJSON: `{"text":"а"}`}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountVerifiedForCMID(900); err != nil || n != 1 {
		t.Fatalf("count=%d err=%v, ожидали 1", n, err)
	}
	if n, _ := st.CountVerifiedForCMID(901); n != 0 {
		t.Errorf("count чужого теста = %d", n)
	}
	if ok, err := st.DeleteVerifiedAnswer("d1"); err != nil || !ok {
		t.Fatalf("delete: ok=%t err=%v", ok, err)
	}
	if ok, _ := st.DeleteVerifiedAnswer("d1"); ok {
		t.Error("повторное удаление не должно находить запись")
	}
	if _, ok, _ := st.PeekAnswer("d1"); ok {
		t.Error("запись осталась после удаления")
	}

	if err := st.SaveWrongAnswer("d2", `{"choice":"x"}`, 1, 1); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.DeleteWrongAnswer("d2", `{"choice":"x"}`); err != nil || !ok {
		t.Fatalf("delete wrong: ok=%t err=%v", ok, err)
	}
	if list, _ := st.ListWrongAnswers("d2"); len(list) != 0 {
		t.Errorf("неверный ответ остался: %v", list)
	}
}
