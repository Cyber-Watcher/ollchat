package config

import (
	"strings"
	"testing"
)

// Подстановка первым словом — частая опечатка: человек пишет "{file}"
// вместо имени программы. Отказ обязан объяснять, чего ждут.
func TestViewerNeedsProgramFirst(t *testing.T) {
	c := Default()
	c.Viewers.PDF = "{file}"
	err := c.finalize()
	if err == nil {
		t.Fatal("подстановка вместо программы должна быть ошибкой")
	}
	if !strings.Contains(err.Error(), "viewers.pdf") {
		t.Errorf("в ошибке нет имени настройки: %v", err)
	}
}

// По умолчанию просмотрщики не настроены — открывает система.
func TestViewersEmptyByDefault(t *testing.T) {
	c := Default()
	if c.Viewers.PDF != "" || c.Viewers.EPUB != "" || c.Viewers.MD != "" {
		t.Error("по умолчанию просмотрщики должны быть пусты: открывает система")
	}
	if err := c.finalize(); err != nil {
		t.Fatalf("умолчания должны проходить проверку: %v", err)
	}
}
