package web

import (
	"embed"
	"html/template"
	"io"
	"log"
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
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		// fallback if wrapper definition missing
		log.Printf("template %q exec error: %v", name, err)
		return tmpl.ExecuteTemplate(w, "base", data)
	}
	return nil
}
