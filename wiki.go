package main

import (
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/yuin/goldmark"
)

// Constants define our system boundaries
const (
	// Maximum size for page content to prevent memory exhaustion
	max_page_size_bytes = 1 << 20 // 1MB

	// Maximum number of concurrent requests to prevent resource exhaustion
	max_concurrent_requests = 100

	// Maximum number of pages to list in directory
	max_directory_pages = 1000

	// Default data directory if not specified
	default_data_dir = "data"
)

// Common errors
var (
	ErrPageTooLarge = errors.New("page content exceeds maximum allowed size")
	ErrEmptyTitle   = errors.New("page title cannot be empty")
	ErrServerBusy   = errors.New("server is at maximum capacity")
)

// Page represents a wiki page with its metadata
type Page struct {
	Title       string
	Body        template.HTML // rendered HTML content
	RawBody     string       // raw markdown content
	Size_bytes  int64        // size of raw content
	Modified_at int64        // unix timestamp
}

// Assert that the page is valid before any operation
func (p *Page) validate() error {
	if p.Title == "" {
		return ErrEmptyTitle
	}
	if int64(len(p.RawBody)) > max_page_size_bytes {
		return ErrPageTooLarge
	}
	return nil
}

// getDataDir returns the configured data directory or default
func getDataDir() string {
	if dir := os.Getenv("WIKI_DATA_DIR"); dir != "" {
		return dir
	}
	return default_data_dir
}

func (p *Page) save() error {
	// Validate before saving
	if err := p.validate(); err != nil {
		return err
	}

	filename := filepath.Join(getDataDir(), p.Title+".md")
	return os.WriteFile(filename, []byte(p.RawBody), 0600)
}

func loadPage(title string) (*Page, error) {
	if title == "" {
		return nil, ErrEmptyTitle
	}

	filename := filepath.Join(getDataDir(), title+".md")
	body, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	// Check size bounds
	if len(body) > max_page_size_bytes {
		return nil, ErrPageTooLarge
	}

	// Get file info for metadata
	info, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}

	// Convert markdown to HTML
	var html_body strings.Builder
	if err := goldmark.Convert(body, &html_body); err != nil {
		return nil, err
	}

	page := &Page{
		Title:       title,
		Body:        template.HTML(html_body.String()),
		RawBody:     string(body),
		Size_bytes:  info.Size(),
		Modified_at: info.ModTime().Unix(),
	}

	// Validate after loading
	if err := page.validate(); err != nil {
		return nil, err
	}

	return page, nil
}

// RequestLimiter implements a concurrent request limiter
type RequestLimiter struct {
	semaphore chan struct{}
}

func NewRequestLimiter() *RequestLimiter {
	return &RequestLimiter{
		semaphore: make(chan struct{}, max_concurrent_requests),
	}
}

func (l *RequestLimiter) Acquire() error {
	select {
	case l.semaphore <- struct{}{}:
		return nil
	default:
		return ErrServerBusy
	}
}

func (l *RequestLimiter) Release() {
	<-l.semaphore
}

// Wiki represents our application state
type Wiki struct {
	templates *template.Template
	limiter   *RequestLimiter
	mu        sync.RWMutex // Protects concurrent page operations
}

func NewWiki() (*Wiki, error) {
	// Ensure data directory exists
	dataDir := getDataDir()
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	// Load templates
	templates, err := template.ParseGlob("templates/*.html")
	if err != nil {
		return nil, err
	}

	return &Wiki{
		templates: templates,
		limiter:   NewRequestLimiter(),
	}, nil
}

func (w *Wiki) viewHandler(writer http.ResponseWriter, request *http.Request, title string) {
	if err := w.limiter.Acquire(); err != nil {
		http.Error(writer, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer w.limiter.Release()

	if title == "Directory" {
		w.serveDirectory(writer)
		return
	}

	w.mu.RLock()
	page, err := loadPage(title)
	w.mu.RUnlock()

	if err != nil {
		if os.IsNotExist(err) {
			http.Redirect(writer, request, "/edit/"+title, http.StatusFound)
			return
		}
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	w.renderTemplate(writer, "view", page)
}

func (w *Wiki) editHandler(writer http.ResponseWriter, request *http.Request, title string) {
	if err := w.limiter.Acquire(); err != nil {
		http.Error(writer, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer w.limiter.Release()

	w.mu.RLock()
	page, err := loadPage(title)
	w.mu.RUnlock()

	if err != nil && !os.IsNotExist(err) {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	if err != nil {
		page = &Page{Title: title}
	}

	w.renderTemplate(writer, "edit", page)
}

func (w *Wiki) saveHandler(writer http.ResponseWriter, request *http.Request, title string) {
    // Only accept POST method
    if request.Method != http.MethodPost {
        http.Error(writer, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    if err := w.limiter.Acquire(); err != nil {
        http.Error(writer, err.Error(), http.StatusServiceUnavailable)
        return
    }
    defer w.limiter.Release()

    // Parse form data
    if err := request.ParseForm(); err != nil {
        http.Error(writer, err.Error(), http.StatusBadRequest)
        return
    }

    body := request.FormValue("body")
    page := &Page{Title: title, RawBody: body}

    w.mu.Lock()
    err := page.save()
    w.mu.Unlock()

    if err != nil {
        log.Printf("Error saving page %q: %v", title, err)
        http.Error(writer, err.Error(), http.StatusInternalServerError)
        return
    }

    // Always use StatusFound (302) for redirection after successful POST
    http.Redirect(writer, request, "/view/"+title, http.StatusFound)
}

func (w *Wiki) serveDirectory(writer http.ResponseWriter) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var pages []string
	err := filepath.Walk(getDataDir(), func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
			if len(pages) >= max_directory_pages {
				return errors.New("too many pages")
			}
			pageName := strings.TrimSuffix(info.Name(), ".md")
			pages = append(pages, pageName)
		}
		return nil
	})

	if err != nil {
		http.Error(writer, "Error listing directory", http.StatusInternalServerError)
		return
	}

	var body strings.Builder
	body.WriteString(`<h1>Wiki Pages Directory</h1>`)
	body.WriteString(`<ul class="page-list">`)
	for _, page := range pages {
		body.WriteString(`<li><a href="/view/` + page + `">` + page + `</a></li>`)
	}
	body.WriteString(`</ul>`)
	body.WriteString(`<p><a href="/view/FrontPage">Return to FrontPage</a></p>`)

	writer.Write([]byte(body.String()))
}

func (w *Wiki) renderTemplate(writer http.ResponseWriter, tmpl string, page *Page) {
	err := w.templates.ExecuteTemplate(writer, tmpl+".html", page)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

// More restrictive path validation following TigerStyle
var validPath = regexp.MustCompile(`^/(edit|save|view)/([a-zA-Z][a-zA-Z0-9_-]{0,63})$`)

func (w *Wiki) makeHandler(fn func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		matches := validPath.FindStringSubmatch(request.URL.Path)
		if matches == nil {
			http.NotFound(writer, request)
			return
		}
		fn(writer, request, matches[2])
	}
}

func main() {
	// Initialize wiki
	wiki, err := NewWiki()
	if err != nil {
		log.Fatal(err)
	}

	// Setup routes
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/view/FrontPage", http.StatusFound)
	})
	http.HandleFunc("/view/", wiki.makeHandler(wiki.viewHandler))
	http.HandleFunc("/edit/", wiki.makeHandler(wiki.editHandler))
	http.HandleFunc("/save/", wiki.makeHandler(wiki.saveHandler))

	port := ":8080"
	log.Printf("Starting wiki server on %s", port)
	log.Fatal(http.ListenAndServe(port, nil))
}
