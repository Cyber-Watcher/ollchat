package kb

import "testing"

func TestLooksLikeTOC(t *testing.T) {
	toc := "DNS in Kubernetes     192\n10.1  A brief intro to DNS (and CoreDNS)    192\n" +
		"NXDOMAINs, A records, and CNAME records 193\nPods need internal DNS 195\n" +
		"10.2  Why StatefulSets instead of Deployments?  196\n"
	if !LooksLikeTOC(toc) {
		t.Error("оглавление не распознано")
	}
	// Абзац по делу: год или версия на конце строки встречаются, но редко —
	// на настоящих кусках 0–10% строк (замер 07.09.2026 пробником по коллекции),
	// у оглавлений 59–70%.
	prose := "CoreDNS is powered by plugins, and you read a CoreDNS configuration\n" +
		"from the top down. The Corefile lives in a ConfigMap since Kubernetes 1.11\n" +
		"and each plugin is a line. Restart the Pods after editing it,\n" +
		"or the change is never picked up. The forward plugin sends what the\n" +
		"cluster cannot answer to the upstream resolvers from resolv.conf,\n" +
		"and the cache plugin keeps answers for the configured seconds.\n"
	if LooksLikeTOC(prose) {
		t.Error("абзац принят за оглавление")
	}
	// Дамп памяти из стенограммы WinDbg: строки кончаются шестнадцатеричными
	// словами и значениями после «=», это не номера страниц.
	dump := "0:000> ~13s eax=00000000 ebx=00000001 ecx=00000000 edx=00000\n" +
		"00 000000b7`4ab7fd58 00007ff9`5709029d win32u!NtUserMsgWaitForMultipleObjectsEx+0x14\n" +
		"01 000000b7`4ab7fd60 00007ff9`57090234 user32!RealMsgWaitForMultipleObjectsEx+0x1e\n" +
		"02 000000b7`4ab7fda0 00007ff6`61f7c33a user32!MsgWaitForMultipleObjects+0x1f\n" +
		"eip=6a0ba32d esp=05ddefd0 ebp=05ddeff8 iopl=0\n"
	if LooksLikeTOC(dump) {
		t.Error("дамп памяти принят за оглавление")
	}
	short := "Глава 1 5\nГлава 2 9\n"
	if LooksLikeTOC(short) {
		t.Error("две строки — ещё не оглавление")
	}
	if LooksLikeTOC("") {
		t.Error("пустой кусок")
	}
}
