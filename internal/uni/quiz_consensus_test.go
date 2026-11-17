package uni

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
)

func TestConsensusMajorityThree(t *testing.T) {
	v := newConsensusVote()
	v.total = 3
	v.counts["c:0"] = 2
	v.counts["c:1"] = 1
	v.answers["c:0"] = moodle.QuizAnswer{Choice: "0"}
	v.answers["c:1"] = moodle.QuizAnswer{Choice: "1"}
	v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "m", KeyLabel: "a"}

	q := moodle.QuizQuestion{Slot: 1, QType: "truefalse"}
	winners := map[int]consensusWinner{}
	(&Syncer{}).resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if _, ok := winners[1]; !ok {
		t.Fatal("2 из 3 должны давать консенсус")
	}
	key, n, second := leader(v)
	if key != "c:0" || n != 2 || second != 1 {
		t.Fatalf("leader = %q %d %d, ожидали c:0 2 1", key, n, second)
	}
}

func TestConsensusWinnerFrozenAfterEscalation(t *testing.T) {
	v := newConsensusVote()
	v.total = 3
	v.counts["c:0"] = 2
	v.counts["c:1"] = 1
	v.answers["c:0"] = moodle.QuizAnswer{Choice: "0"}
	v.answers["c:1"] = moodle.QuizAnswer{Choice: "1"}
	v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "m"}
	v.targets["c:1"] = llm.Target{Provider: llm.Gemini, Model: "m"}
	q := moodle.QuizQuestion{Slot: 1, QType: "truefalse"}
	winners := map[int]consensusWinner{}
	sy := &Syncer{}
	sy.resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if got := winners[1].key; got != "c:0" {
		t.Fatalf("первый консенсус должен зафиксировать c:0, получили %q", got)
	}
	// 4-й ключ голосует иначе — уже принятый ответ не переголосуется.
	v.total = 4
	v.counts["c:1"] = 2
	sy.resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if got := winners[1].key; got != "c:0" {
		t.Fatalf("после эскалации победитель не должен меняться, получили %q", got)
	}
}

func TestConsensusTwoOfTwoUnanimous(t *testing.T) {
	v := newConsensusVote()
	v.total = 2
	v.counts["c:0"] = 2
	v.answers["c:0"] = moodle.QuizAnswer{Choice: "0"}
	v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "m"}
	q := moodle.QuizQuestion{Slot: 1, QType: "truefalse"}
	winners := map[int]consensusWinner{}
	(&Syncer{}).resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if _, ok := winners[1]; !ok {
		t.Fatal("2/2 единогласных должны считаться консенсусом, даже если третья модель упала")
	}
}

func TestConsensusTieDoesNotWin(t *testing.T) {
	v := newConsensusVote()
	v.total = 4
	v.counts["c:0"] = 2
	v.counts["c:1"] = 2
	v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "m"}
	v.targets["c:1"] = llm.Target{Provider: llm.Gemini, Model: "m"}
	q := moodle.QuizQuestion{Slot: 1, QType: "truefalse"}
	winners := map[int]consensusWinner{}
	(&Syncer{}).resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if _, ok := winners[1]; ok {
		t.Fatal("2/4 при равенстве не должны давать консенсус")
	}
}

func TestConsensusTwoVotesSplitDoesNotWin(t *testing.T) {
	v := newConsensusVote()
	v.total = 2
	v.counts["c:0"] = 1
	v.counts["c:1"] = 1
	v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "m"}
	v.targets["c:1"] = llm.Target{Provider: llm.Gemini, Model: "m"}
	q := moodle.QuizQuestion{Slot: 1, QType: "truefalse"}
	winners := map[int]consensusWinner{}
	(&Syncer{}).resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if _, ok := winners[1]; ok {
		t.Fatal("1/1 при двух голосах не должен давать консенсус")
	}
}

func TestAnswerVoteKeyNormalizesText(t *testing.T) {
	q := moodle.QuizQuestion{Slot: 1, QType: "shortanswer"}
	a := answerVoteKey(q, moodle.QuizAnswer{Text: "  БИТ "})
	b := answerVoteKey(q, moodle.QuizAnswer{Text: "бит"})
	if a == "" || a != b {
		t.Fatalf("текстовые ответы должны нормализоваться одинаково: %q vs %q", a, b)
	}
}

func TestConsensusVotesForBudget(t *testing.T) {
	if got := consensusVotesForBudget(3, 2*time.Minute); got != 3 {
		t.Fatalf("2 минуты: ожидали 3 голоса, получили %d", got)
	}
	if got := consensusVotesForBudget(3, 30*time.Second); got != 0 {
		t.Fatalf("30 секунд: ожидали обычную цепочку, получили %d", got)
	}
	if got := consensusVotesForBudget(0, 10*time.Minute); got != 0 {
		t.Fatalf("выключенный консенсус: ожидали 0, получили %d", got)
	}
}

func TestMergeFallbackKeepsConsensus(t *testing.T) {
	consensus := map[int]moodle.QuizAnswer{1: {Choice: "consensus"}}
	direct := map[int]moodle.QuizAnswer{1: {Choice: "direct"}, 2: {Choice: "filled"}}
	got := mergeFallbackAnswers(consensus, direct)
	if got[1].Choice != "consensus" {
		t.Fatalf("консенсусный ответ должен побеждать direct, получили %q", got[1].Choice)
	}
	if got[2].Choice != "filled" {
		t.Fatalf("direct должен заполнить пропуск, получили %q", got[2].Choice)
	}
}

func TestQuickTargetsOrdersBySpeed(t *testing.T) {
	chain := []llm.Target{
		{Provider: llm.Gemini, Model: "gemini-4-hydrogen"},
		{Provider: llm.Cheaper, Model: "muse-spark-1.3-contributor"},
		{Provider: llm.Gemini, Model: "gemini-3.5-flash-lite"},
		{Provider: llm.Gemini, Model: "gemini-3.6-flash"},
	}
	got := quickTargets(chain, 4)
	want := []string{"gemini-3.5-flash-lite", "gemini-3.6-flash", "gemini-4-hydrogen", "muse-spark-1.3-contributor"}
	for i := range want {
		if got[i].Model != want[i] {
			t.Fatalf("quickTargets[%d] = %q, ожидали %q", i, got[i].Model, want[i])
		}
	}
}

func TestPageOptsForWaveBudget(t *testing.T) {
	opts := pageOptsForWave(false, time.Now().Add(4*time.Minute), 4, 4)
	if opts.Budget < 3*time.Minute || opts.Budget > 4*time.Minute {
		t.Fatalf("одна волна: бюджет %s, ожидали около 4 минут", opts.Budget)
	}
	opts = pageOptsForWave(false, time.Now().Add(4*time.Minute), 8, 4)
	if opts.Budget < 90*time.Second || opts.Budget > 2*time.Minute {
		t.Fatalf("две волны: бюджет %s, ожидали около 2 минут", opts.Budget)
	}
	if opts := pageOptsForWave(false, time.Time{}, 4, 4); opts.Budget != 0 {
		t.Fatalf("без дедлайна бюджет должен быть 0, получили %s", opts.Budget)
	}
}

func TestAskPageConsensusAllModelsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	old := llm.GeminiURL
	llm.GeminiURL = srv.URL
	defer func() { llm.GeminiURL = old }()

	chain := make([]llm.Target, 0, 3)
	for _, key := range []string{"k1", "k2", "k3"} {
		tg, ok := llm.ProviderWithUserKey(nil, llm.Gemini, key, 0)
		if !ok {
			t.Fatalf("не собрался target для ключа %s", key)
		}
		chain = append(chain, tg)
	}
	qs := []moodle.QuizQuestion{{Slot: 1, QType: "truefalse", Choices: []moodle.QuizChoice{{Value: "0", Text: "Да"}}}}
	sy := &Syncer{}
	if _, _, _, err := sy.askPageConsensus(context.Background(), quizTag{TG: 1, CMID: 2}, 0, chain, "Курс", "", qs, nil, 3, 3, solvePageOpts{}); err == nil {
		t.Fatal("если все модели упали, должна быть ошибка, а не пустой успешный ответ")
	}
}

func TestAskPageConsensusMajorityOfThreeKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		choice := "0"
		if key == "k3" {
			choice = "1"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]string{"content": `{"answers":[{"slot":1,"choice":"` + choice + `"}]}`},
			}},
		})
	}))
	defer srv.Close()

	old := llm.GeminiURL
	llm.GeminiURL = srv.URL
	defer func() { llm.GeminiURL = old }()

	chain := make([]llm.Target, 0, 3)
	for _, key := range []string{"k1", "k2", "k3"} {
		tg, ok := llm.ProviderWithUserKey(nil, llm.Gemini, key, 0)
		if !ok {
			t.Fatalf("не собрался target для ключа %s", key)
		}
		chain = append(chain, tg)
	}
	qs := []moodle.QuizQuestion{{
		Slot:    1,
		QType:   "truefalse",
		Choices: []moodle.QuizChoice{{Name: "q1:1_answer", Value: "0", Text: "Да"}, {Name: "q1:1_answer", Value: "1", Text: "Нет"}},
	}}
	sy := &Syncer{}
	answers, _, res, err := sy.askPageConsensus(context.Background(), quizTag{TG: 1, CMID: 2, Page: 0}, 0, chain, "Курс", "", qs, nil, 3, 3, solvePageOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := answers[1].Choice; got != "0" {
		t.Fatalf("ожидали выбор большинства 0, получили %q", got)
	}
	if res.Used == nil {
		t.Fatal("в результате должен быть Used")
	}
}
