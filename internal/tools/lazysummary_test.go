package tools

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Ленивые описания берут из обзора только темы без названия и не больше
// предела: каждая — обращение к модели, которого ждёт обзор.
func TestUndescribedTopicsLimited(t *testing.T) {
	res := graph.OverviewResult{Topics: []graph.OverviewTopic{
		{Community: graph.Community{ID: 1, Title: "есть"}},
		{Community: graph.Community{ID: 2}},
		{Community: graph.Community{ID: 3}},
		{Community: graph.Community{ID: 4}},
		{Community: graph.Community{ID: 5}},
	}}
	got := undescribed(res, 3)
	if len(got) != 3 || got[0] != 2 || got[2] != 4 {
		t.Fatalf("темы к описанию: %v", got)
	}
	if got := undescribed(graph.OverviewResult{}, 3); len(got) != 0 {
		t.Fatalf("пустой обзор дал темы: %v", got)
	}
}
