package web

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html
var tmplFS embed.FS

var (
	once      sync.Once
	pageTmpls map[string]*template.Template
)

func load() {
	base := template.Must(template.New("base").Funcs(template.FuncMap{}).ParseFS(tmplFS, "templates/base.html"))
	entries, err := tmplFS.ReadDir("templates")
	if err != nil {
		log.Printf("template readdir error: %v", err)
		return
	}
	pageTmpls = make(map[string]*template.Template)
	var files []string
	for _, e := range entries {
		n := e.Name()
		if n == "base.html" || !strings.HasSuffix(n, ".html") {
			continue
		}
		files = append(files, n)
	}
	sort.Strings(files)
	for _, f := range files {
		clone, err := base.Clone()
		if err != nil {
			log.Printf("template clone error %s: %v", f, err)
			continue
		}
		if _, err := clone.ParseFS(tmplFS, "templates/"+f); err != nil {
			log.Printf("template parse error %s: %v", f, err)
			continue
		}
		key := strings.TrimSuffix(f, ".html")
		pageTmpls[key] = clone
	}
}

// Render writes the named template (which can rely on base) to w with data enriched by Now.
func Render(w io.Writer, name string, data map[string]any) error {
	once.Do(load)
	if data == nil {
		data = map[string]any{}
	}
	data["Now"] = time.Now().Format(time.RFC822)
	key := strings.TrimSuffix(name, ".html")
	t := pageTmpls[key]
	if t == nil {
		return fmt.Errorf("unknown template: %s", name)
	}
	return t.ExecuteTemplate(w, "base", data)
}
