package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerateReportFallback(t *testing.T) {
	reporter := NewReporter("", "", "")
	transcript := "Здравствуйте. Сегодня на занятии решаем тесты в Moodle."

	rep, err := reporter.GenerateReport(context.Background(), "Высшая математика", transcript)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !rep.HasTestAlert {
		t.Error("expected HasTestAlert to be true")
	}

	if rep.Discipline != "Высшая математика" {
		t.Errorf("expected discipline 'Высшая математика', got %q", rep.Discipline)
	}
}

func TestGenerateReportLLM(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"choices": [
				{
					"message": {
						"content": "📌 **Тема:** Дифференциальные уравнения.\n🔑 **Термины:** Интеграл, производная."
					}
				}
			]
		}`))
	}))
	defer mockServer.Close()

	reporter := NewReporter("test-key", mockServer.URL, "gpt-4o-mini")
	rep, err := reporter.GenerateReport(context.Background(), "Матанализ", "Интеграл функции...")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rep.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

func TestChunkText(t *testing.T) {
	short := "Hello World"
	if ChunkText(short, 20) != short {
		t.Error("expected short text unmodified")
	}

	long := "12345678901234567890"
	chunked := ChunkText(long, 5)
	if len(chunked) < 5 {
		t.Errorf("unexpected chunked length %d", len(chunked))
	}
}
