package ollama

import "testing"

// TestRunnerDied — падение обработчика модели внутри Ollama приходит под кодом
// 400, хотя повторить запрос имеет полный смысл: запрос не изменился,
// оборвалась связь сервера с его собственным процессом модели.
//
// Найдено на 74% векторизации библиотеки: час работы едва не пропал из-за того,
// что 400 считался окончательным приговором.
func TestRunnerDied(t *testing.T) {
	yes := []string{
		`{"error":"do embedding request: Post \"http://127.0.0.1:38271/v1/embeddings\": EOF"}`,
		`{"error":"Post \"http://localhost:41111/v1/embeddings\": connection refused"}`,
		`{"error":"Post \"http://127.0.0.1:9/embed\": broken pipe"}`,
	}
	for _, s := range yes {
		if !runnerDied(s) {
			t.Fatalf("падение обработчика не распознано: %s", s)
		}
	}
	// Настоящие 400 повторять нельзя: имя модели от повтора не исправится.
	no := []string{
		`{"error":"model \"bge-m4\" not found, try pulling it first"}`,
		`{"error":"invalid input: empty"}`,
		`{"error":"unexpected EOF while parsing request body"}`,
	}
	for _, s := range no {
		if runnerDied(s) {
			t.Fatalf("обычная ошибка принята за падение обработчика: %s", s)
		}
	}
}
