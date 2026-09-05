package graph

import (
	"crypto/sha256"
	"encoding/hex"
)

// Версия промптов графа.
//
// Промпт извлечения — исполняемый артефакт с тем же профилем риска, что
// и код: «System prompts, few-shot examples, and output schemas are executable
// artifacts with the same risk profile as application code… A prompt change
// without a corresponding commit is an untracked configuration change»
// («The Ultimate AI Guide for Linux Engineers», 2026, стр. 292). Коммит у нас
// был всегда (промпт лежит в репозитории), а прослеживаемости не было: по
// данным графа нельзя было сказать, каким промптом разобран участок.
//
// PromptID — первые восемь знаков SHA-256 от текстов трёх промптов. Он
// пишется в паспорт графа при создании и первой сборке, и сборка другим
// промптом отказывается продолжать — иначе половина графа окажется собрана
// одной схемой, половина другой, и различить их потом будет нечем (ровно та
// же беда, что со сменой модели извлечения). Заведено этапом 91 (R1.1).
var PromptID = promptID(SystemPrompt, SummaryPrompt, FindingsPrompt)

// PromptIDV2 — версия промптов графа формата 2: свой промпт извлечения
// (prompts/extract2.txt), остальные общие. Отдельный файл нужен затем, чтобы
// ужесточение правил для опытного графа не меняло PromptID рабочего: иначе
// ночная сборка формата 1 отказалась бы продолжаться «другим промптом».
var PromptIDV2 = promptID(SystemPromptV2, SummaryPrompt, FindingsPrompt)

// PromptIDFor — версия промптов для графа данного формата.
func PromptIDFor(version int) string {
	if version >= FormatV2 {
		return PromptIDV2
	}
	return PromptID
}

// SystemPromptFor — промпт извлечения для графа данного формата.
func SystemPromptFor(version int) string {
	if version >= FormatV2 {
		return SystemPromptV2
	}
	return SystemPrompt
}

func promptID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0}) // граница: «ab»+«c» и «a»+«bc» — разные наборы
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}
