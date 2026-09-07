package graph

import (
	"strings"
	"testing"
)

// Строка подтверждения у связи берётся не из оглавления, пока есть другой кусок.
func TestEvidenceLineSkipsTableOfContents(t *testing.T) {
	src := memChunks{
		{104, 21}:  "DNS in Kubernetes 192\n10.1 A brief intro to DNS (and CoreDNS) 192\nPods need internal DNS 195\nConfiguring CoreDNS 203\n",
		{104, 682}: "In Kubernetes, CoreDNS is the Pod that answers cluster DNS.",
	}
	r := FoundRelation{Src: "CoreDNS", Dst: "Kubernetes", Evidences: []ChunkKey{{104, 21}, {104, 682}}}
	line := evidenceLine(src, r, 120)
	if !strings.Contains(line, "answers cluster DNS") {
		t.Fatalf("подтверждение взято из оглавления: %q", line)
	}
	r.Evidences = []ChunkKey{{104, 21}}
	if evidenceLine(src, r, 120) == "" {
		t.Fatal("единственное оглавление выброшено — строка пуста")
	}
}
