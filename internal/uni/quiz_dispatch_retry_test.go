package uni

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// Ошибка постановки в очередь (база занята) не теряет открывшийся тест: метка «ожидает повтора»
// заставит следующий синк поставить его снова, а «Открылся тест» не повторяется.
func TestQuizOpenedDispatchErrorRetriedOnNextSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bot.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	v, err := vault.Load("", filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	sy := &Syncer{
		Store: st, Vault: v, Loc: samara, QuizMode: QuizModeService,
		Notify: func(_ context.Context, _ int64, text string) { notes = append(notes, text) },
	}
	now := at(14, 45, 30)
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = time.Now })

	cli, err := moodle.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	acc := model.Account{TelegramID: 1, Kind: model.AccountMoodle}
	closed := watchQuiz(900, at(14, 45, 0), at(15, 10, 0), watchClosedTodo)
	open := watchQuiz(900, at(14, 45, 0), at(15, 10, 0), "Разрешено попыток: 1")
	ctx := context.Background()

	// Очередь «занята»: вставка задачи падает на каждом обращении.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`CREATE TRIGGER quiz_jobs_busy BEFORE INSERT ON quiz_jobs BEGIN SELECT RAISE(ABORT, 'database is locked'); END`); err != nil {
		t.Fatal(err)
	}

	sy.detectAndHandleOpenedQuizzes(ctx, acc, cli, []model.MoodleItem{closed}, []model.MoodleItem{open})
	sy.Wait()
	if len(notes) != 1 {
		t.Fatalf("уведомлений %d, want 1 («Открылся тест»)", len(notes))
	}
	if j, _ := st.ActiveQuizJob(1, 900); j != nil {
		t.Fatalf("задача поставлена при занятой очереди: %+v", j)
	}
	if v, ok, _ := st.GetSetting(quizFailKey(1, 900)); !ok || v == "" {
		t.Fatalf("тест не помечен как ожидающий повтора (quiz_fail пуст)")
	}

	// База снова доступна: следующий синк (тест уже открыт в прошлом синке) ставит задачу.
	if _, err := raw.Exec(`DROP TRIGGER quiz_jobs_busy`); err != nil {
		t.Fatal(err)
	}
	sy.detectAndHandleOpenedQuizzes(ctx, acc, cli, []model.MoodleItem{open}, []model.MoodleItem{open})
	sy.Wait()
	if j, _ := st.ActiveQuizJob(1, 900); j == nil {
		t.Fatal("тест не поставлен в очередь на следующем синке")
	}
	if len(notes) != 1 {
		t.Fatalf("«Открылся тест» повторён: уведомлений %d, want 1", len(notes))
	}
}
