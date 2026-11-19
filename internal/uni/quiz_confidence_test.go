package uni

import (
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
)

func confSyncer(share int, minConf float64) *Syncer {
	return &Syncer{Cfg: &config.Config{
		QuizConsensus:      3,
		QuizConsensusMax:   9,
		QuizConsensusShare: share,
		QuizConfidenceMin:  minConf,
	}}
}

func voteWithAnswer(total int, n int, conf float64) *consensusVote {
	v := newConsensusVote()
	v.total = total
	v.counts["c:0"] = n
	if total > n {
		v.counts["c:1"] = total - n
		v.answers["c:1"] = moodle.QuizAnswer{Choice: "1"}
		v.targets["c:1"] = llm.Target{Provider: llm.Gemini, Model: "m"}
	}
	v.answers["c:0"] = moodle.QuizAnswer{Choice: "0"}
	v.targets["c:0"] = llm.Target{Provider: llm.Gemini, Model: "m"}
	if conf > 0 {
		v.conf["c:0"] = conf
	}
	return v
}

func TestConsensusThreeOfFiveIsNotEnough(t *testing.T) {
	q := moodle.QuizQuestion{Slot: 1, QType: "truefalse"}
	v := voteWithAnswer(5, 3, 0.9)
	winners := map[int]consensusWinner{}
	confSyncer(66, 0.5).resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if _, ok := winners[1]; ok {
		t.Fatal("3 из 5 — меньше двух третей, консенсуса быть не должно")
	}
}

func TestConsensusFourOfFiveWins(t *testing.T) {
	q := moodle.QuizQuestion{Slot: 1, QType: "truefalse"}
	v := voteWithAnswer(5, 4, 0.9)
	winners := map[int]consensusWinner{}
	confSyncer(66, 0.5).resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if _, ok := winners[1]; !ok {
		t.Fatal("4 из 5 должны давать консенсус")
	}
}

func TestConsensusLowConfidenceDoesNotWin(t *testing.T) {
	q := moodle.QuizQuestion{Slot: 1, QType: "truefalse"}
	v := voteWithAnswer(3, 2, 0.2)
	winners := map[int]consensusWinner{}
	confSyncer(66, 0.5).resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if _, ok := winners[1]; ok {
		t.Fatal("ответ с уверенностью 0.2 не должен приниматься без единогласия")
	}
}

func TestConsensusUnanimousBeatsConfidenceFloor(t *testing.T) {
	q := moodle.QuizQuestion{Slot: 1, QType: "truefalse"}
	v := voteWithAnswer(2, 2, 0.1)
	winners := map[int]consensusWinner{}
	confSyncer(66, 0.5).resolveConsensus([]moodle.QuizQuestion{q}, map[int]*consensusVote{1: v}, winners, 3)
	if _, ok := winners[1]; !ok {
		t.Fatal("единогласие принимается даже с низкой уверенностью")
	}
}

func TestQuizEffort(t *testing.T) {
	cases := []struct {
		name string
		opts solvePageOpts
		want string
	}{
		{"обычный режим — максимум", solvePageOpts{Budget: 5 * time.Minute}, "max"},
		{"тесный бюджет — medium", solvePageOpts{Budget: 90 * time.Second}, "medium"},
		{"быстрый режим — low", solvePageOpts{FastMode: true, Budget: 5 * time.Minute}, "low"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quizEffort(tc.opts); got != tc.want {
				t.Fatalf("quizEffort=%q, ожидали %q", got, tc.want)
			}
		})
	}
}
