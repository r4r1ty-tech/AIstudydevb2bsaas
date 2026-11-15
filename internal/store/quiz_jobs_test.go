package store

import (
	"path/filepath"
	"testing"
	"time"
)

var quizT0 = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

const quizLeaseTest = 10 * time.Minute

func enqueueOK(t *testing.T, s *Store, j QuizJob, now time.Time) bool {
	t.Helper()
	created, err := s.EnqueueQuizJob(j, now)
	if err != nil {
		t.Fatalf("EnqueueQuizJob(tg=%d cmid=%d): %v", j.TelegramID, j.CMID, err)
	}
	return created
}

func claimOK(t *testing.T, s *Store, worker string, now time.Time) *QuizJob {
	t.Helper()
	j, err := s.ClaimQuizJob(worker, now, quizLeaseTest)
	if err != nil {
		t.Fatalf("ClaimQuizJob(%s): %v", worker, err)
	}
	return j
}

func TestQuizEnqueueNoDuplicates(t *testing.T) {
	s := openTemp(t)
	j := QuizJob{TelegramID: 1, CMID: 900, Course: "ТФЯГ", Title: "Тест 2"}
	if !enqueueOK(t, s, j, quizT0) {
		t.Fatal("первая постановка должна создать задачу")
	}
	if enqueueOK(t, s, j, quizT0) {
		t.Fatal("повтор постановки создал дубль")
	}
	// Пока задача в queued, повтор не создаёт новую и с forced=false ничего не меняет.
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM quiz_jobs WHERE telegram_id = 1 AND cmid = 900`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("строк в quiz_jobs = %d (err %v), want 1", n, err)
	}

	// Задача в running тоже активна: новая постановка не создаётся.
	claimOK(t, s, "w1", quizT0)
	if enqueueOK(t, s, j, quizT0) {
		t.Fatal("постановка при running создала дубль")
	}
}

func TestQuizEnqueueForcedRaisesPriority(t *testing.T) {
	s := openTemp(t)
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 900}, quizT0)
	if enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 900, Forced: true}, quizT0) {
		t.Fatal("forced при конфликте не должен создавать задачу")
	}
	var forced int
	if err := s.db.QueryRow(`SELECT forced FROM quiz_jobs WHERE telegram_id = 1 AND cmid = 900`).Scan(&forced); err != nil || forced != 1 {
		t.Fatalf("forced = %d (err %v), want 1 после «Пройти»", forced, err)
	}
}

func TestQuizEnqueueAfterFinishedCreatesNew(t *testing.T) {
	s := openTemp(t)
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 900}, quizT0)
	j := claimOK(t, s, "w1", quizT0)
	if ok, err := s.FinishQuizJob(j.ID, "w1", QuizJobDone, "", quizT0); err != nil || !ok {
		t.Fatalf("FinishQuizJob = %v %v", ok, err)
	}
	if !enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 900}, quizT0) {
		t.Fatal("после done тест снова ставится новой задачей")
	}
}

func TestQuizClaimPriority(t *testing.T) {
	s := openTemp(t)
	early := quizT0.Add(24 * time.Hour)
	late := quizT0.Add(48 * time.Hour)
	// Разные аккаунты, чтобы «один тест на аккаунт» не мешал порядку.
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 101}, quizT0) // без срока, первым
	enqueueOK(t, s, QuizJob{TelegramID: 2, CMID: 102, Deadline: &late}, quizT0.Add(time.Second))
	enqueueOK(t, s, QuizJob{TelegramID: 3, CMID: 103, Deadline: &early}, quizT0.Add(2*time.Second))
	enqueueOK(t, s, QuizJob{TelegramID: 4, CMID: 104, Forced: true}, quizT0.Add(3*time.Second)) // «Пройти» — выше всех

	want := []int64{104, 103, 102, 101}
	now := quizT0.Add(time.Minute)
	for i, cmid := range want {
		j := claimOK(t, s, "w1", now)
		if j == nil {
			t.Fatalf("захват %d: очередь пуста, want cmid %d", i, cmid)
		}
		if j.CMID != cmid {
			t.Fatalf("захват %d: cmid %d, want %d", i, j.CMID, cmid)
		}
		if j.Status != QuizJobRunning || j.ClaimedBy != "w1" || j.Attempts != 1 || j.LeaseUntil == nil {
			t.Fatalf("захваченная задача: %+v", j)
		}
		if _, err := s.FinishQuizJob(j.ID, "w1", QuizJobDone, "", now); err != nil {
			t.Fatal(err)
		}
	}
	if j, err := s.ClaimQuizJob("w1", now, quizLeaseTest); err != nil || j != nil {
		t.Fatalf("пустая очередь: %+v %v", j, err)
	}
}

func TestQuizClaimOneRunningPerAccount(t *testing.T) {
	s := openTemp(t)
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 201}, quizT0)
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 202, Forced: true}, quizT0)
	enqueueOK(t, s, QuizJob{TelegramID: 2, CMID: 203}, quizT0.Add(time.Second))

	first := claimOK(t, s, "w1", quizT0)
	if first == nil || first.TelegramID != 1 {
		t.Fatalf("первый захват: %+v", first)
	}
	// У аккаунта 1 уже есть running с живой арендой: второй тест этого аккаунта не выдаётся.
	second := claimOK(t, s, "w2", quizT0)
	if second == nil || second.TelegramID != 2 {
		t.Fatalf("второй захват должен взять аккаунт 2, получили %+v", second)
	}
	if third := claimOK(t, s, "w3", quizT0); third != nil {
		t.Fatalf("третий захват выдал задачу занятого аккаунта: %+v", third)
	}
	// Когда тест аккаунта 1 (с «Пройти») завершён, остающийся тест того же аккаунта берётся.
	if ok, err := s.FinishQuizJob(first.ID, "w1", QuizJobDone, "", quizT0); err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	next := claimOK(t, s, "w3", quizT0)
	if next == nil || next.TelegramID != 1 || next.CMID != 201 {
		t.Fatalf("после завершения ждали тест 201 аккаунта 1, получили %+v", next)
	}
}

func TestQuizClaimTakesOverExpiredLease(t *testing.T) {
	s := openTemp(t)
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 301}, quizT0)
	first := claimOK(t, s, "w1", quizT0) // воркер «умер» после захвата: аренда не продлевается
	if first == nil {
		t.Fatal("захват не выдал задачу")
	}

	// Аренда живая: другой воркер не забирает.
	if j := claimOK(t, s, "w2", quizT0.Add(5*time.Minute)); j != nil {
		t.Fatalf("живая аренда перехвачена: %+v", j)
	}
	// Аренда истекла (SIGKILL): задача уходит другому воркеру, попытка растёт.
	took := claimOK(t, s, "w2", quizT0.Add(11*time.Minute))
	if took == nil || took.ID != first.ID {
		t.Fatalf("просроченная аренда не перехвачена: %+v", took)
	}
	if took.ClaimedBy != "w2" || took.Attempts != 2 {
		t.Fatalf("после перехвата claimed_by=%q attempts=%d, want w2 и 2", took.ClaimedBy, took.Attempts)
	}
	// Старый воркер, вернувшись, не может ни продлить, ни завершить чужую задачу.
	if ok, err := s.RenewQuizLease(first.ID, "w1", quizT0.Add(12*time.Minute), quizLeaseTest); err != nil || ok {
		t.Fatalf("продление чужой задачи прошло: %v %v", ok, err)
	}
	if ok, err := s.FinishQuizJob(first.ID, "w1", QuizJobDone, "", quizT0.Add(12*time.Minute)); err != nil || ok {
		t.Fatalf("завершение чужой задачи прошло: %v %v", ok, err)
	}
}

func TestQuizRenewKeepsLease(t *testing.T) {
	s := openTemp(t)
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 401}, quizT0)
	j := claimOK(t, s, "w1", quizT0)
	// Продление на 8-й минуте держит аренду дальше 10-й минуты.
	if ok, err := s.RenewQuizLease(j.ID, "w1", quizT0.Add(8*time.Minute), quizLeaseTest); err != nil || !ok {
		t.Fatalf("RenewQuizLease = %v %v", ok, err)
	}
	if got := claimOK(t, s, "w2", quizT0.Add(12*time.Minute)); got != nil {
		t.Fatalf("продлённая аренда перехвачена: %+v", got)
	}
	if got := claimOK(t, s, "w2", quizT0.Add(19*time.Minute)); got == nil || got.ID != j.ID {
		t.Fatalf("после истечения продлённой аренды задача не перехвачена: %+v", got)
	}
}

func TestQuizReleaseReturnsToQueue(t *testing.T) {
	s := openTemp(t)
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 501}, quizT0)
	j := claimOK(t, s, "w1", quizT0)
	if ok, err := s.ReleaseQuizJob(j.ID, "w2", quizT0); err != nil || ok {
		t.Fatalf("release чужой задачи: %v %v", ok, err)
	}
	if ok, err := s.ReleaseQuizJob(j.ID, "w1", quizT0); err != nil || !ok {
		t.Fatalf("ReleaseQuizJob = %v %v", ok, err)
	}
	got, err := s.QuizJobByID(j.ID)
	if err != nil || got == nil || got.Status != QuizJobQueued || got.ClaimedBy != "" || got.LeaseUntil != nil {
		t.Fatalf("после release: %+v %v", got, err)
	}
	// Задача снова доступна сразу, а не через истечение аренды, и попытка растёт.
	again := claimOK(t, s, "w2", quizT0)
	if again == nil || again.ID != j.ID || again.Attempts != 2 {
		t.Fatalf("после release захват = %+v", again)
	}
}

func TestQuizFinishValidatesStatus(t *testing.T) {
	s := openTemp(t)
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 601}, quizT0)
	j := claimOK(t, s, "w1", quizT0)
	if _, err := s.FinishQuizJob(j.ID, "w1", QuizJobRunning, "", quizT0); err == nil {
		t.Fatal("FinishQuizJob принял нефинальный статус")
	}
	if ok, err := s.FinishQuizJob(j.ID, "w1", QuizJobFailed, "попытка не завершилась", quizT0); err != nil || !ok {
		t.Fatalf("FinishQuizJob failed = %v %v", ok, err)
	}
	got, err := s.QuizJobByID(j.ID)
	if err != nil || got == nil || got.Status != QuizJobFailed || got.LastError != "попытка не завершилась" {
		t.Fatalf("после failed: %+v %v", got, err)
	}
}

func TestQuizJobByIDMissing(t *testing.T) {
	s := openTemp(t)
	if j, err := s.QuizJobByID(999); err != nil || j != nil {
		t.Fatalf("missing = %+v %v", j, err)
	}
}

func TestQuizEnqueueRejectsEmptyKeys(t *testing.T) {
	s := openTemp(t)
	if _, err := s.EnqueueQuizJob(QuizJob{CMID: 1}, quizT0); err == nil {
		t.Fatal("пустой аккаунт принят")
	}
	if _, err := s.EnqueueQuizJob(QuizJob{TelegramID: 1}, quizT0); err == nil {
		t.Fatal("пустой тест принят")
	}
}

func TestQuizMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	enqueueOK(t, s1, QuizJob{TelegramID: 1, CMID: 701}, quizT0)
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("повторное открытие базы: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if enqueueOK(t, s2, QuizJob{TelegramID: 1, CMID: 701}, quizT0) {
		t.Fatal("задача потерялась при повторной миграции")
	}
}

func TestQuizActiveJobLookup(t *testing.T) {
	s := openTemp(t)
	if j, err := s.ActiveQuizJob(1, 801); err != nil || j != nil {
		t.Fatalf("пустая очередь: %+v %v", j, err)
	}
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 801}, quizT0)
	j, err := s.ActiveQuizJob(1, 801)
	if err != nil || j == nil || j.Status != QuizJobQueued {
		t.Fatalf("queued: %+v %v", j, err)
	}
	claimOK(t, s, "w1", quizT0)
	if j, err := s.ActiveQuizJob(1, 801); err != nil || j == nil || j.Status != QuizJobRunning {
		t.Fatalf("running: %+v %v", j, err)
	}
	if ok, err := s.FinishQuizJob(j.ID, "w1", QuizJobDone, "", quizT0); err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	if j, err := s.ActiveQuizJob(1, 801); err != nil || j != nil {
		t.Fatalf("после done задача активна: %+v %v", j, err)
	}
}

func TestQuizPruneRemovesOnlyOldFinished(t *testing.T) {
	s := openTemp(t)
	const day = 24 * time.Hour
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 901}, quizT0) // A: done, старое
	enqueueOK(t, s, QuizJob{TelegramID: 2, CMID: 902}, quizT0) // B: failed, старое
	enqueueOK(t, s, QuizJob{TelegramID: 3, CMID: 903}, quizT0) // C: queued, не трогаем

	a := claimOK(t, s, "w1", quizT0)
	b := claimOK(t, s, "w1", quizT0)
	if a == nil || a.CMID != 901 || b == nil || b.CMID != 902 {
		t.Fatalf("захват: %+v %+v", a, b)
	}
	if ok, err := s.FinishQuizJob(a.ID, "w1", QuizJobDone, "", quizT0); err != nil || !ok {
		t.Fatalf("finish A: %v %v", ok, err)
	}
	if ok, err := s.FinishQuizJob(b.ID, "w1", QuizJobFailed, "не решён", quizT0); err != nil || !ok {
		t.Fatalf("finish B: %v %v", ok, err)
	}

	// D: завершено недавно (20 дней спустя), берётся первым благодаря forced.
	later := quizT0.Add(20 * day)
	enqueueOK(t, s, QuizJob{TelegramID: 4, CMID: 904, Forced: true}, later)
	d := claimOK(t, s, "w1", later)
	if d == nil || d.CMID != 904 {
		t.Fatalf("захват D: %+v", d)
	}
	if ok, err := s.FinishQuizJob(d.ID, "w1", QuizJobDone, "", later); err != nil || !ok {
		t.Fatalf("finish D: %v %v", ok, err)
	}

	// Порог: 14 дней назад от момента, когда D ещё свежая.
	n, err := s.PruneQuizJobs(quizT0.Add(14 * day))
	if err != nil || n != 2 {
		t.Fatalf("PruneQuizJobs = %d %v, want 2 (A и B)", n, err)
	}
	if j, _ := s.QuizJobByID(a.ID); j != nil {
		t.Fatalf("старая done не удалена: %+v", j)
	}
	if j, _ := s.QuizJobByID(b.ID); j != nil {
		t.Fatalf("старая failed не удалена: %+v", j)
	}
	if j, err := s.ActiveQuizJob(3, 903); err != nil || j == nil || j.Status != QuizJobQueued {
		t.Fatalf("queued задача тронута: %+v %v", j, err)
	}
	if j, err := s.QuizJobByID(d.ID); err != nil || j == nil {
		t.Fatalf("свежая done удалена: %+v %v", j, err)
	}
	// Повторная чистка ничего не находит.
	if n, err := s.PruneQuizJobs(quizT0.Add(14 * day)); err != nil || n != 0 {
		t.Fatalf("повторная чистка = %d %v, want 0", n, err)
	}
}

// Задача, которую процесс «роняет» QuizMaxClaims раз подряд (аренда истекает), на следующем захвате
// переходит в failed с понятной причиной и больше не выдаётся.
func TestQuizClaimLimitFailsPoisonedJob(t *testing.T) {
	s := openTemp(t)
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 1001}, quizT0)
	var id int64
	for i := 0; i < QuizMaxClaims; i++ {
		// Каждый раз аренда истекает до следующего захвата: воркер «падает» на задаче.
		now := quizT0.Add(time.Duration(i) * 11 * time.Minute)
		got := claimOK(t, s, "w-crash", now)
		if got == nil || got.Attempts != i+1 {
			t.Fatalf("захват %d: %+v", i+1, got)
		}
		id = got.ID
	}
	now := quizT0.Add(QuizMaxClaims * 11 * time.Minute)
	if got := claimOK(t, s, "w-new", now); got != nil {
		t.Fatalf("задача выдана после %d захватов: %+v", QuizMaxClaims, got)
	}
	j, err := s.QuizJobByID(id)
	if err != nil || j == nil || j.Status != QuizJobFailed || j.LastError != QuizClaimLimitError {
		t.Fatalf("после лимита %+v %v, want failed с причиной", j, err)
	}
	if j.LeaseUntil != nil || j.ClaimedBy != "" {
		t.Fatalf("у failed осталась аренда: %+v", j)
	}
}

// Задача, которую возвращали в queued QuizMaxClaims раз, тоже не уходит в очередь сверх лимита.
func TestQuizClaimLimitFailsQueuedJob(t *testing.T) {
	s := openTemp(t)
	enqueueOK(t, s, QuizJob{TelegramID: 2, CMID: 1002}, quizT0)
	var id int64
	for i := 0; i < QuizMaxClaims; i++ {
		now := quizT0.Add(time.Duration(i) * time.Minute)
		got := claimOK(t, s, "w1", now)
		if got == nil {
			t.Fatalf("захват %d пуст", i+1)
		}
		id = got.ID
		if ok, err := s.ReleaseQuizJob(id, "w1", now); err != nil || !ok {
			t.Fatalf("release %d: %v %v", i+1, ok, err)
		}
	}
	if got := claimOK(t, s, "w1", quizT0.Add(time.Hour)); got != nil {
		t.Fatalf("задача в queued выдана сверх лимита: %+v", got)
	}
	j, err := s.QuizJobByID(id)
	if err != nil || j == nil || j.Status != QuizJobFailed || j.LastError != QuizClaimLimitError {
		t.Fatalf("queued после лимита: %+v %v", j, err)
	}
}

// Исключения: задачи, которые воркер сам ещё решает, не выдаются, даже когда их аренда истекла.
func TestQuizClaimExceptSkipsOwnRunningJob(t *testing.T) {
	s := openTemp(t)
	enqueueOK(t, s, QuizJob{TelegramID: 1, CMID: 1101}, quizT0)
	enqueueOK(t, s, QuizJob{TelegramID: 2, CMID: 1102}, quizT0.Add(time.Second))
	first, err := s.ClaimQuizJob("w1", quizT0, quizLeaseTest)
	if err != nil || first == nil || first.CMID != 1101 {
		t.Fatalf("первый захват: %+v %v", first, err)
	}
	// Аренда истекла, но задача всё ещё в работе у этого же воркера: исключаем её и берём другую.
	later := quizT0.Add(11 * time.Minute)
	got, err := s.ClaimQuizJobExcept("w1", later, quizLeaseTest, []int64{first.ID})
	if err != nil || got == nil || got.CMID != 1102 {
		t.Fatalf("захват с исключением: %+v %v, want задача 1102", got, err)
	}
	// Без исключения та же задача забрала бы себя снова (это и есть повторное решение).
	if again, err := s.ClaimQuizJobExcept("w1", later, quizLeaseTest, []int64{first.ID, got.ID}); err != nil || again != nil {
		t.Fatalf("захват при всех исключённых: %+v %v", again, err)
	}
}
