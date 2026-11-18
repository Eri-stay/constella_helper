package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

// part is a piece of code that belongs to one way of publishing: some
// functions of a file, or the whole file when funcs is empty.
type part struct {
	file  string
	funcs []string // "Func" or "Type.Method"
	what  string
}

var (
	// Code that only the classic mode needs.
	classicParts = []part{
		{"services/release/internal/app/service.go", []string{"Service.PublishClassic", "saga.auditStep"}, "сага: синхронний виклик і обов'язковий аудит"},
		{"services/release/internal/adapters/clients.go", []string{"AuditClient.Record"}, "HTTP-клієнт аудиту"},
	}
	// Code that only the event mode needs.
	edaParts = []part{
		{"services/release/internal/app/service.go", []string{"Service.RequestEDA", "Service.Wake", "Service.RunWorker",
			"Service.resumeAll", "Service.Advance", "saga.envelopes", "saga.attempt", "retryable", "Service.WithRelay", "Service.WithRetry"},
			"воркер саги, повтори, продовження після перезапуску, інтеграційні події"},
		{"services/release/internal/adapters/store.go", []string{"Store.Open"}, "пошук незавершених релізів"},
		{"internal/platform/outbox/outbox.go", nil, "transactional outbox"},
		{"internal/platform/outbox/relay.go", nil, "relay outbox → брокер"},
		{"internal/platform/inbox/inbox.go", nil, "inbox (ідемпотентні споживачі)"},
		{"internal/platform/mq/client.go", nil, "RabbitMQ: publisher confirms, перепідключення"},
		{"internal/platform/mq/consumer.go", nil, "RabbitMQ: споживач, DLQ, retry"},
		{"services/telemetry/internal/app/service.go", []string{"Service.HandleEvent"}, "споживач аудиту з inbox"},
		{"services/gateway/internal/adapters/sse.go", nil, "SSE: живий статус в UI"},
	}
	// The saga that both modes share.
	sharedFile = "services/release/internal/app/service.go"
)

func complexity() string {
	classic, cRows := sum(classicParts)
	eda, eRows := sum(edaParts)
	total := fileLines(sharedFile)
	shared := total - func() int { // the shared file minus the parts counted above
		n := 0
		for _, p := range append(append([]part{}, classicParts...), edaParts...) {
			if p.file == sharedFile {
				n += lines(p)
			}
		}
		return n
	}()

	var sb strings.Builder
	sb.WriteString("### Складність розробки\n\n")
	sb.WriteString("Рядки коду без порожніх рядків і коментарів (`go run ./tools/bench -loc`).\n\n")
	sb.WriteString("| | Класичний | Подієвий |\n|---|---|---|\n")
	fmt.Fprintf(&sb, "| Код лише цього режиму, рядків | %d | %d |\n", classic, eda)
	fmt.Fprintf(&sb, "| Спільна сага (резерв, switch, позначка, відкат, спани), рядків | %d | %d |\n", shared, shared)
	sb.WriteString("| Компоненти лише цього режиму | HTTP-клієнт аудиту | outbox, relay, inbox, брокер RabbitMQ, воркер саги, споживач аудиту, SSE |\n")
	sb.WriteString("| Нові види збоїв | немає (помилка кроку дає відкат і `502`) | недоставлена подія, дублікат, зупинка посеред саги, зависання воркера |\n\n")
	sb.WriteString("Склад коду режиму:\n\n| Режим | Що | Рядків |\n|---|---|---|\n")
	for _, r := range cRows {
		sb.WriteString("| класичний | " + r + "\n")
	}
	for _, r := range eRows {
		sb.WriteString("| подієвий | " + r + "\n")
	}
	return sb.String()
}

func sum(parts []part) (int, []string) {
	total := 0
	var rows []string
	for _, p := range parts {
		n := lines(p)
		total += n
		rows = append(rows, fmt.Sprintf("%s (`%s`) | %d |", p.what, p.file, n))
	}
	return total, rows
}

// lines counts code lines of a part.
func lines(p part) int {
	if len(p.funcs) == 0 {
		return fileLines(p.file)
	}
	src, err := os.ReadFile(p.file)
	if err != nil {
		return 0
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, p.file, src, 0)
	if err != nil {
		return 0
	}
	all := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	want := map[string]bool{}
	for _, n := range p.funcs {
		want[n] = true
	}
	n := 0
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || !want[funcName(fd)] {
			continue
		}
		n += codeLines(all[fset.Position(fd.Pos()).Line-1 : fset.Position(fd.End()).Line])
	}
	return n
}

// funcName returns "Type.Method" for methods and "Func" for functions.
func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

func fileLines(path string) int {
	src, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return codeLines(strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n"))
}

func codeLines(ls []string) int {
	n := 0
	for _, l := range ls {
		t := strings.TrimSpace(l)
		if t != "" && !strings.HasPrefix(t, "//") {
			n++
		}
	}
	return n
}
