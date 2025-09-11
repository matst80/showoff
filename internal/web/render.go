package web

import (
	"embed"
	"html/template"
	"io"
	"sync"
	"time"
)

//go:embed templates/*.html
var tmplFS embed.FS

var (
	once sync.Once
	tmpl *template.Template
)

func load() {
	base := template.New("base").Funcs(template.FuncMap{})
	tmpl = template.Must(base.ParseFS(tmplFS, "templates/base.html", "templates/*.html"))
}

// Render writes the named template (which can rely on base) to w with data enriched by Now.
func Render(w io.Writer, name string, data map[string]any) error {
	once.Do(load)
	if data == nil {
		data = map[string]any{}
	}
	data["Now"] = time.Now().Format(time.RFC822)
	return tmpl.ExecuteTemplate(w, name, data)
}
