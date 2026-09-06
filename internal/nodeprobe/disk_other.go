//go:build !linux

package nodeprobe

import "fmt"

// diskUsage — на не-Linux место не считается: наблюдатель ставится рядом
// с Ollama на сервере, а это всегда Linux. Молчаливого нуля здесь быть
// не должно, поэтому честная ошибка.
func diskUsage(path string) (total, free int64, err error) {
	return 0, 0, fmt.Errorf("подсчёт места поддерживается только на Linux")
}
