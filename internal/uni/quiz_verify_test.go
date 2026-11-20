package uni

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
)

// Меньшинство с высокой уверенностью должно побеждать большинство
// неуверенных: 0.95 против 0.25 + 0.25.
func TestConsensusWeightedConfidenceBeatsMajority(t *testing.T) {
	v := newConsensusVote()
	v.total = 3
	v.counts["c:0"] = 2
	v.counts["c:1"] = 1
	v.weights["c:0"] = 0.5
	v.weights["c:1"] = 1.0
	v.answers["c:0"] = moodle.QuizAnswer{Choice: "0"}
	v.answers["c:1"] = moodle.QuizAnswer{Choice: "1"}
	v.conf["c:1"] = 1.0
	v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "m"}
	v.targets["c:1"] = llm.Target{Provider: llm.OpenCodeGo, Model: "m"}

	q := moodle.QuizQuestion{Slot: 1, QType: "truefalse"}
	winners := map[int]consensusWinner{}
	confSyncer(66, 0.5).resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if got := winners[1].key; got != "c:1" {
		t.Fatalf("взвешенный лидер должен быть c:1, получили %q (n=%d)", got, winners[1].n)
	}
}

// Два уверенных голоса против одного неуверенного: вес 0.9+0.8 > 0.25.
func TestConsensusWeightedTwoConfidentWin(t *testing.T) {
	v := newConsensusVote()
	v.total = 3
	v.counts["c:0"] = 2
	v.counts["c:1"] = 1
	v.weights["c:0"] = 1.7
	v.weights["c:1"] = 0.25
	v.answers["c:0"] = moodle.QuizAnswer{Choice: "0"}
	v.answers["c:1"] = moodle.QuizAnswer{Choice: "1"}
	v.conf["c:0"] = 0.85
	v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "m"}
	v.targets["c:1"] = llm.Target{Provider: llm.Gemini, Model: "m"}

	q := moodle.QuizQuestion{Slot: 1, QType: "truefalse"}
	winners := map[int]consensusWinner{}
	confSyncer(66, 0.5).resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if got := winners[1].key; got != "c:0" {
		t.Fatalf("два уверенных голоса должны побеждать, получили %q", got)
	}
}

func consensusVerifyChain(t *testing.T) []llm.Target {
	t.Helper()
	chain := make([]llm.Target, 0, 4)
	for _, key := range []string{"k1", "k2", "k3"} {
		tg, ok := llm.ProviderWithUserKey(nil, llm.Gemini, key, 0)
		if !ok {
			t.Fatalf("не собрался target для ключа %s", key)
		}
		chain = append(chain, tg)
	}
	vt, ok := llm.ProviderWithUserKey(nil, llm.OpenCodeGo, "verifier-key", 0)
	if !ok {
		t.Fatal("не собрался target верификатора")
	}
	return append(chain, vt)
}

func verifyQuestion() moodle.QuizQuestion {
	return moodle.QuizQuestion{
		Slot:  1,
		QType: "multichoice",
		Text:  "Что такое бит?",
		Choices: []moodle.QuizChoice{
			{Name: "q1:1_answer", Value: "0", Text: "Единица информации"},
			{Name: "q1:1_answer", Value: "1", Text: "Двоичный разряд"},
		},
	}
}

func verifierJSONReply(pick int, conf float64) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]string{
				"content": fmt.Sprintf(`{"pick":%d,"confidence":%v,"why":"вариант точнее"}`, pick, conf),
			},
		}},
	}
}

// Спорный слот (лидер не добрал долю) уходит верификатору другого
// провайдера; его выбор принимается.
func TestConsensusDisputedSlotGoesToVerifier(t *testing.T) {
	var verifierCalls atomic.Int32
	var verifierAuth string
	vsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifierCalls.Add(1)
		verifierAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(verifierJSONReply(2, 0.95))
	}))
	defer vsrv.Close()

	old := llm.OpenCodeGoURL
	llm.OpenCodeGoURL = vsrv.URL
	defer func() { llm.OpenCodeGoURL = old }()

	// 0.9 против 0.25+0.25: доля 64% — ниже порога 66%, победителя нет.
	v := newConsensusVote()
	v.total = 3
	v.counts["c:0"] = 2
	v.counts["c:1"] = 1
	v.weights["c:0"] = 0.5
	v.weights["c:1"] = 0.9
	v.answers["c:0"] = moodle.QuizAnswer{Choice: "0"}
	v.answers["c:1"] = moodle.QuizAnswer{Choice: "1"}
	v.conf["c:1"] = 0.9
	v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "gemini-4-hydrogen"}
	v.targets["c:1"] = llm.Target{Provider: llm.Gemini, Model: "gemini-4-hydrogen"}

	q := verifyQuestion()
	sy := confSyncer(66, 0.5)
	winners := map[int]consensusWinner{}
	var res llm.Result
	sy.verifyDisputed(context.Background(), quizTag{TG: 1, CMID: 20}, 0, consensusVerifyChain(t),
		[]moodle.QuizQuestion{q}, map[int]moodle.QuizQuestion{1: q}, map[int]*consensusVote{1: v},
		winners, &res, time.Time{})

	got, ok := winners[1]
	if !ok {
		t.Fatal("верификатор должен заполнить спорный слот")
	}
	if got.answer.Choice != "0" {
		t.Fatalf("верификатор выбрал вариант 0, получили %q", got.answer.Choice)
	}
	if verifierCalls.Load() != 1 {
		t.Fatalf("верификатор должен быть вызван один раз, был %d", verifierCalls.Load())
	}
	if verifierAuth != "Bearer verifier-key" {
		t.Fatalf("верификация ушла не тому провайдеру: %q", verifierAuth)
	}
	if len(res.Attempts) != 1 {
		t.Fatalf("вызов верификатора должен попасть в Result, попыток %d", len(res.Attempts))
	}
}

// Ошибка верификатора не ломает страницу: остаётся взвешенный победитель.
func TestConsensusVerifierErrorKeepsWeightedWinner(t *testing.T) {
	vsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "verifier down", http.StatusInternalServerError)
	}))
	defer vsrv.Close()

	old := llm.OpenCodeGoURL
	llm.OpenCodeGoURL = vsrv.URL
	defer func() { llm.OpenCodeGoURL = old }()

	// Порог доли снижен до 50%: перевес 1.15 против 0.8 формально решён,
	// но тонкий (0.18 < 0.2) — слот всё равно спорный.
	v := newConsensusVote()
	v.total = 3
	v.counts["c:0"] = 2
	v.counts["c:1"] = 1
	v.weights["c:0"] = 1.15
	v.weights["c:1"] = 0.8
	v.answers["c:0"] = moodle.QuizAnswer{Choice: "0"}
	v.answers["c:1"] = moodle.QuizAnswer{Choice: "1"}
	v.conf["c:0"] = 0.9
	v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "gemini-4-hydrogen"}
	v.targets["c:1"] = llm.Target{Provider: llm.Gemini, Model: "gemini-4-hydrogen"}

	q := verifyQuestion()
	sy := confSyncer(50, 0.5)
	winners := map[int]consensusWinner{}
	sy.resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if got := winners[1].key; got != "c:0" {
		t.Fatalf("до проверки взвешенный победитель c:0, получили %q", got)
	}
	var res llm.Result
	sy.verifyDisputed(context.Background(), quizTag{TG: 1, CMID: 21}, 0, consensusVerifyChain(t),
		[]moodle.QuizQuestion{q}, map[int]moodle.QuizQuestion{1: q}, map[int]*consensusVote{1: v},
		winners, &res, time.Time{})

	if got := winners[1].answer.Choice; got != "0" {
		t.Fatalf("после ошибки верификатора должен остаться ответ 0, получили %q", got)
	}
}

// Верификатор не вызывается, когда слот решён уверенно.
func TestConsensusNoVerifierForSolidWinner(t *testing.T) {
	var verifierCalls atomic.Int32
	vsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifierCalls.Add(1)
		http.Error(w, "не должен вызываться", http.StatusInternalServerError)
	}))
	defer vsrv.Close()

	old := llm.OpenCodeGoURL
	llm.OpenCodeGoURL = vsrv.URL
	defer func() { llm.OpenCodeGoURL = old }()

	v := newConsensusVote()
	v.total = 3
	v.counts["c:0"] = 2
	v.counts["c:1"] = 1
	v.weights["c:0"] = 1.8
	v.weights["c:1"] = 0.6
	v.answers["c:0"] = moodle.QuizAnswer{Choice: "0"}
	v.answers["c:1"] = moodle.QuizAnswer{Choice: "1"}
	v.conf["c:0"] = 0.9
	v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "gemini-4-hydrogen"}
	v.targets["c:1"] = llm.Target{Provider: llm.Gemini, Model: "gemini-4-hydrogen"}

	q := verifyQuestion()
	sy := confSyncer(66, 0.5)
	winners := map[int]consensusWinner{}
	sy.resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	var res llm.Result
	sy.verifyDisputed(context.Background(), quizTag{TG: 1, CMID: 22}, 0, consensusVerifyChain(t),
		[]moodle.QuizQuestion{q}, map[int]moodle.QuizQuestion{1: q}, map[int]*consensusVote{1: v},
		winners, &res, time.Time{})

	if got := winners[1].answer.Choice; got != "0" {
		t.Fatalf("ожидали решённый весом ответ 0, получили %q", got)
	}
	if verifierCalls.Load() != 0 {
		t.Fatalf("для уверенного победителя верификатор вызываться не должен, был %d", verifierCalls.Load())
	}
}

func TestPickVerifierOtherProvider(t *testing.T) {
	chain := consensusVerifyChain(t)
	got, ok := pickVerifier(chain, llm.Gemini)
	if !ok {
		t.Fatal("должен найтись верификатор другого провайдера")
	}
	if got.Provider != llm.OpenCodeGo {
		t.Fatalf("ожидали OpenCode, получили %s", got.Provider)
	}
	if _, ok := pickVerifier(chain[:3], llm.Gemini); ok {
		t.Fatal("без другого провайдера верификатор не выбирается")
	}
}

func TestConsensusVerifyConfigDefaults(t *testing.T) {
	sy := &Syncer{Cfg: &config.Config{}}
	if got := sy.consensusShare(); got != 66 {
		t.Fatalf("доля по умолчанию должна быть 66, получили %d", got)
	}
	if got := sy.confidenceMin(); got != 0.5 {
		t.Fatalf("порог уверенности по умолчанию 0.5, получили %v", got)
	}
}

func TestSampleVoteWeight(t *testing.T) {
	sample := consensusSample{conf: map[int]float64{}}
	if got := sampleVoteWeight(sample, 1); got != 1 {
		t.Fatalf("без confidence вес должен быть 1, получили %v", got)
	}
	sample.conf[1] = 0.1
	if got := sampleVoteWeight(sample, 1); got != quizVoteWeightFloor {
		t.Fatalf("низкая уверенность должна упираться в минимум %v, получили %v", quizVoteWeightFloor, got)
	}
	sample.conf[1] = 0.7
	if got := sampleVoteWeight(sample, 1); got != 0.7 {
		t.Fatalf("вес должен быть равен уверенности, получили %v", got)
	}
}

// Не больше quizVerifyMaxSlots вызовов за раунд, по одному на слот.
func TestConsensusVerifierSlotCap(t *testing.T) {
	var calls atomic.Int32
	vsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(verifierJSONReply(1, 0.9))
	}))
	defer vsrv.Close()

	old := llm.OpenCodeGoURL
	llm.OpenCodeGoURL = vsrv.URL
	defer func() { llm.OpenCodeGoURL = old }()

	var qs []moodle.QuizQuestion
	votes := map[int]*consensusVote{}
	bySlot := map[int]moodle.QuizQuestion{}
	for slot := 1; slot <= 5; slot++ {
		q := moodle.QuizQuestion{
			Slot:  slot,
			QType: "multichoice",
			Text:  "Вопрос",
			Choices: []moodle.QuizChoice{
				{Name: "q:answer", Value: "0", Text: "А"},
				{Name: "q:answer", Value: "1", Text: "Б"},
			},
		}
		qs = append(qs, q)
		bySlot[slot] = q
		v := newConsensusVote()
		v.total = 3
		v.counts["c:0"] = 2
		v.counts["c:1"] = 1
		v.weights["c:0"] = 0.5
		v.weights["c:1"] = 0.9
		v.answers["c:0"] = moodle.QuizAnswer{Choice: "0"}
		v.answers["c:1"] = moodle.QuizAnswer{Choice: "1"}
		v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "gemini-4-hydrogen"}
		v.targets["c:1"] = llm.Target{Provider: llm.Gemini, Model: "gemini-4-hydrogen"}
		votes[slot] = v
	}

	sy := confSyncer(66, 0.5)
	winners := map[int]consensusWinner{}
	var res llm.Result
	sy.verifyDisputed(context.Background(), quizTag{TG: 1, CMID: 23}, 0, consensusVerifyChain(t),
		qs, bySlot, votes, winners, &res, time.Time{})
	if got := calls.Load(); got != quizVerifyMaxSlots {
		t.Fatalf("ожидали %d вызовов верификатора, было %d", quizVerifyMaxSlots, got)
	}
}
