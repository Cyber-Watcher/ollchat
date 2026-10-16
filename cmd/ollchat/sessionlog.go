package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/Cyber-Watcher/ollchat/internal/buildinfo"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// sessionStep — запись начала сеанса в журнал шагов: сборка, отпечатки
// системного промпта, правил разрешений и набора инструментов, модель
// (этап 112, Е6, Ц2). Промпт, правила и описания инструментов живут в
// разных местах — в конфиге и в бинаре — и единой версии у них нет; по
// отпечаткам видно, что между двумя сеансами поменялось, а что нет.
// Сами тексты не пишутся: только хеш, частные данные в журнал не уходят.
func sessionStep(cfg *config.Config, srv *config.Server, model string, reg *tools.Registry) steplog.Step {
	perm, _ := json.Marshal(cfg.Permissions)
	extra := map[string]any{
		"build":         buildinfo.Describe(version),
		"server":        srv.Name,
		"system_prompt": fingerprint(srv.SystemPrompt),
		"permissions":   fingerprint(string(perm)),
		// Модели поиска (этап 112, Л4): от них зависят выдержки, поданные модели.
		"embed_model":  cfg.KB.EmbedModel,
		"rerank_model": cfg.KB.RerankModel,
	}
	if reg != nil {
		specs, _ := json.Marshal(reg.Specs())
		extra["tools"] = fingerprint(string(specs))
		extra["tools_n"] = len(reg.Names())
	}
	return steplog.Step{Kind: steplog.KindSession, Model: model, Extra: extra}
}

// fingerprint — первые 12 шестнадцатеричных знаков SHA-256: хватает отличить
// версии, не раскрывая текст.
func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}
