package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Perturbation — правка того, как инструмент описан модели, для замера
// «держится ли выбор инструмента на формулировке» (этап 112, О19; «Practical
// LLM Evaluation for Production Systems», 2026, стр. 329, `books/232#805`).
//
// Только для прогона набора `toolcalleval.py` по ключу `--tool-perturb`,
// не для рабочего бинаря: другое описание ломает кеш промпта.
type Perturbation struct {
	// Description — новое описание инструмента; пусто — прежнее.
	Description string `json:"description,omitempty"`
	// Rename — старое имя параметра → новое. Модель видит новое, инструмент
	// получает аргумент под старым: проверяется выбор, а не исполнение.
	Rename map[string]string `json:"rename,omitempty"`
	// Params — новые описания параметров; ключ — имя, которое видит модель.
	Params map[string]string `json:"params,omitempty"`
}

// LoadPerturbations читает файл JSON вида {"инструмент": Perturbation, …}.
func LoadPerturbations(path string) (map[string]Perturbation, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p map[string]Perturbation
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// Perturb заменяет перечисленные инструменты обёртками с другим описанием.
// Ошибка — инструмент не включён, параметра для переименования нет или новое
// имя уже занято: молча пропущенная правка дала бы замер «без разницы».
func (r *Registry) Perturb(p map[string]Perturbation) error {
	names := make([]string, 0, len(p))
	for n := range p {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		pp := p[name]
		t, ok := r.tools[name]
		if !ok {
			return fmt.Errorf("возмущение: инструмент %q не включён", name)
		}
		props := t.Spec().Function.Parameters.Properties
		seen := map[string]bool{}
		for old, nu := range pp.Rename {
			if _, ok := props[old]; !ok {
				return fmt.Errorf("возмущение %s: нет параметра %q", name, old)
			}
			if _, clash := props[nu]; clash || seen[nu] || nu == "" {
				return fmt.Errorf("возмущение %s: новое имя %q занято или пусто", name, nu)
			}
			seen[nu] = true
		}
		r.tools[name] = &perturbedTool{inner: t, p: pp}
	}
	return nil
}

// perturbedTool — инструмент с правленым описанием; исполняет прежний.
type perturbedTool struct {
	inner Tool
	p     Perturbation
}

func (t *perturbedTool) Name() string { return t.inner.Name() }

func (t *perturbedTool) Spec() ollama.Tool {
	s := t.inner.Spec()
	if t.p.Description != "" {
		s.Function.Description = t.p.Description
	}
	// Карта свойств — общая с исходным описанием: правится копия.
	props := make(map[string]ollama.ToolProp, len(s.Function.Parameters.Properties))
	for k, v := range s.Function.Parameters.Properties {
		if nu, ok := t.p.Rename[k]; ok {
			k = nu
		}
		if d, ok := t.p.Params[k]; ok {
			v.Description = d
		}
		props[k] = v
	}
	s.Function.Parameters.Properties = props
	req := make([]string, len(s.Function.Parameters.Required))
	for i, k := range s.Function.Parameters.Required {
		if nu, ok := t.p.Rename[k]; ok {
			k = nu
		}
		req[i] = k
	}
	s.Function.Parameters.Required = req
	return s
}

// back — аргументы модели под именами, которые понимает исходный инструмент.
func (t *perturbedTool) back(args map[string]any) map[string]any {
	rev := make(map[string]string, len(t.p.Rename))
	for old, nu := range t.p.Rename {
		rev[nu] = old
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if old, ok := rev[k]; ok {
			k = old
		}
		out[k] = v
	}
	return out
}

func (t *perturbedTool) Plan(args map[string]any) (*Plan, error) {
	return t.inner.Plan(t.back(args))
}

// SafeArgs — чтобы обёртка не прятала от журналов скрытие персональных данных.
func (t *perturbedTool) SafeArgs(args map[string]any) string {
	if h, ok := t.inner.(argsHider); ok {
		return h.SafeArgs(t.back(args))
	}
	return ""
}
