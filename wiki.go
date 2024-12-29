package main

import (
    "encoding/json"
    "errors"
	"math"
    "fmt"
    "html/template"
    "log"
    "net/http"
    "os"
    "path/filepath"
    "regexp"
    "sort"
    "strconv"
    "strings"
    "sync"
    "time"

    "github.com/sergi/go-diff/diffmatchpatch"
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

    // Maximum number of versions to keep per page
    max_versions_per_page = 1000

    // Default data directory if not specified
    default_data_dir = "data"
)

// Common errors
var (
    ErrPageTooLarge   = errors.New("page content exceeds maximum allowed size")
    ErrEmptyTitle     = errors.New("page title cannot be empty")
    ErrServerBusy     = errors.New("server is at maximum capacity")
    ErrVersionTooOld  = errors.New("version number is too old")
    ErrVersionInvalid = errors.New("invalid version number")
)

// PageVersion represents a specific version of a page
type PageVersion struct {
    Content   string    `json:"content"`
    Timestamp time.Time `json:"timestamp"`
    Author    string    `json:"author"`
    Comment   string    `json:"comment"`
    Size      int64     `json:"size"`
    Version   int64     `json:"version"`
}

// Page represents a wiki page with its metadata
type Page struct {
    Title       string
    Body        template.HTML // rendered HTML content
    RawBody     string       // raw markdown content
    Size_bytes  int64        // size of raw content
    Modified_at int64        // unix timestamp
    Version     int64        // current version number
    Comment     string       // last change comment
}

// Search result
type SearchResult struct {
    Title     string
    Snippet   string
    Relevance float64
}

// VersionMetadata stores version history information
type VersionMetadata struct {
    CurrentVersion int64                  `json:"current_version"`
    Versions      map[int64]*PageVersion `json:"versions"`
}

// VersionDiff represents the difference between two versions
type VersionDiff struct {
    OldVersion int64
    NewVersion int64
    Diffs      []DiffSegment
}

// DiffSegment represents a segment in a version diff
type DiffSegment struct {
    Type     string // "equal", "insert", "delete"
    Text     string
    Added    bool
    Removed  bool
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

// Move from Page to Wiki
func (w *Wiki) savePage(p *Page, comment string) error {
    if err := p.validate(); err != nil {
        return err
    }

    // Create version directory structure
    pageDir := filepath.Join(getDataDir(), "pages", p.Title)
    if err := os.MkdirAll(pageDir, 0755); err != nil {
        return err
    }

    // Load or create version metadata
    metadata, err := loadVersionMetadata(p.Title)
    if err != nil {
        metadata = &VersionMetadata{
            Versions: make(map[int64]*PageVersion),
        }
    }

    // Create new version
    newVersion := metadata.CurrentVersion + 1
    version := &PageVersion{
        Content:   p.RawBody,
        Timestamp: time.Now(),
        Comment:   comment,
        Size:      p.Size_bytes,
        Version:   newVersion,
    }

    // Save version file
    versionFile := filepath.Join(pageDir, fmt.Sprintf("%d.md", newVersion))
    if err := os.WriteFile(versionFile, []byte(p.RawBody), 0600); err != nil {
        return err
    }

    // Update metadata
    metadata.CurrentVersion = newVersion
    metadata.Versions[newVersion] = version

    // Save current version
    currentFile := filepath.Join(getDataDir(), p.Title+".md")
    if err := os.WriteFile(currentFile, []byte(p.RawBody), 0600); err != nil {
        return err
    }

    // Save metadata
    if err := saveVersionMetadata(p.Title, metadata); err != nil {
        return err
    }

    // Update search index
    w.pageIndex.Store(p.Title, p.RawBody)

    return nil
}

// Load a specific version of a page
func (w *Wiki) loadPageVersion(title string, version int64) (*Page, error) {
    if title == "" {
        return nil, ErrEmptyTitle
    }

    metadata, err := loadVersionMetadata(title)
    if err != nil {
        return nil, err
    }

    var content []byte
    if version <= 0 || version == metadata.CurrentVersion {
        // Load current version
        content, err = os.ReadFile(filepath.Join(getDataDir(), title+".md"))
    } else {
        if version > metadata.CurrentVersion {
            return nil, ErrVersionInvalid
        }
        // Load specific version
        versionFile := filepath.Join(getDataDir(), "pages", title, fmt.Sprintf("%d.md", version))
        content, err = os.ReadFile(versionFile)
    }
    if err != nil {
        return nil, err
    }

    // Check size bounds
    if len(content) > max_page_size_bytes {
        return nil, ErrPageTooLarge
    }

    // Convert markdown to HTML
    var html_body strings.Builder
    if err := goldmark.Convert(content, &html_body); err != nil {
        return nil, err
    }

    page := &Page{
        Title:      title,
        Body:       template.HTML(html_body.String()),
        RawBody:    string(content),
        Size_bytes: int64(len(content)),
        Version:    version,
    }

    if v, ok := metadata.Versions[version]; ok {
        page.Comment = v.Comment
        page.Modified_at = v.Timestamp.Unix()
    }

    // Update search index with current version only
    if version <= 0 || version == metadata.CurrentVersion {
        w.pageIndex.Store(title, string(content))
    }

    return page, nil
}

// Get version history for a page
func getPageHistory(title string) ([]*PageVersion, error) {
    metadata, err := loadVersionMetadata(title)
    if err != nil {
        return nil, err
    }

    history := make([]*PageVersion, 0, len(metadata.Versions))
    for _, version := range metadata.Versions {
        history = append(history, version)
    }

    // Sort by version number descending
    sort.Slice(history, func(i, j int) bool {
        return history[i].Version > history[j].Version
    })

    return history, nil
}

// Compare two versions and generate diff
func (w *Wiki) compareVersions(title string, oldVersion, newVersion int64) (*VersionDiff, error) {
    oldPage, err := w.loadPageVersion(title, oldVersion)
    if err != nil {
        return nil, err
    }

    newPage, err := w.loadPageVersion(title, newVersion)
    if err != nil {
        return nil, err
    }

    dmp := diffmatchpatch.New()
    diffs := dmp.DiffMain(oldPage.RawBody, newPage.RawBody, true)

    segments := make([]DiffSegment, len(diffs))
    for i, diff := range diffs {
        segments[i] = DiffSegment{Text: diff.Text}
        switch diff.Type {
        case diffmatchpatch.DiffEqual:
            segments[i].Type = "equal"
        case diffmatchpatch.DiffInsert:
            segments[i].Type = "insert"
            segments[i].Added = true
        case diffmatchpatch.DiffDelete:
            segments[i].Type = "delete"
            segments[i].Removed = true
        }
    }

    return &VersionDiff{
        OldVersion: oldVersion,
        NewVersion: newVersion,
        Diffs:      segments,
    }, nil
}

// Restore a specific version
func (w *Wiki) restoreVersion(title string, version int64) error {
    oldPage, err := w.loadPageVersion(title, version)
    if err != nil {
        return err
    }

    page := &Page{
        Title:      title,
        RawBody:    oldPage.RawBody,
        Size_bytes: oldPage.Size_bytes,
    }

    comment := fmt.Sprintf("Restored from version %d", version)
    return w.savePage(page, comment)
}

// Helper functions for metadata
func loadVersionMetadata(title string) (*VersionMetadata, error) {
    metadataFile := filepath.Join(getDataDir(), "pages", title, "metadata.json")
    data, err := os.ReadFile(metadataFile)
    if err != nil {
        if os.IsNotExist(err) {
            return &VersionMetadata{Versions: make(map[int64]*PageVersion)}, nil
        }
        return nil, err
    }

    var metadata VersionMetadata
    if err := json.Unmarshal(data, &metadata); err != nil {
        return nil, err
    }
    return &metadata, nil
}

func saveVersionMetadata(title string, metadata *VersionMetadata) error {
    metadataFile := filepath.Join(getDataDir(), "pages", title, "metadata.json")
    data, err := json.MarshalIndent(metadata, "", "    ")
    if err != nil {
        return err
    }
    return os.WriteFile(metadataFile, data, 0600)
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
	pageIndex sync.Map     // Used for search functionality, stores page content in memory
}

func (w *Wiki) initializeSearchIndex() error {
    // Scan the data directory for all pages
    baseDir := getDataDir()
    files, err := os.ReadDir(baseDir)
    if err != nil {
        return err
    }

	log.Printf("Indexing pages...")
    for _, file := range files {
        // Only process .md files
        if !file.IsDir() && strings.HasSuffix(file.Name(), ".md") {
            pageName := strings.TrimSuffix(file.Name(), ".md")
            content, err := os.ReadFile(filepath.Join(baseDir, file.Name()))
            if err != nil {
                log.Printf("Error reading %s: %v", file.Name(), err)
                continue
            }
            w.pageIndex.Store(pageName, string(content))
        }
    }

	log.Printf("Page index complete")

    return nil
}

func NewWiki() (*Wiki, error) {
    // Ensure data directory exists
    dataDir := getDataDir()
    if err := os.MkdirAll(filepath.Join(dataDir, "pages"), 0755); err != nil {
        return nil, err
    }

    // Load templates
    templates, err := template.ParseGlob("templates/*.html")
    if err != nil {
        return nil, err
    }

    wiki := &Wiki{
        templates: templates,
        limiter:   NewRequestLimiter(),
    }

    // Initialize search index
    if err := wiki.initializeSearchIndex(); err != nil {
        return nil, err
    }

    return wiki, nil
}

// HTTP Handlers

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

    version := int64(0)
    if v := request.URL.Query().Get("version"); v != "" {
        var err error
        version, err = strconv.ParseInt(v, 10, 64)
        if err != nil {
            http.Error(writer, "Invalid version number", http.StatusBadRequest)
            return
        }
    }

    w.mu.RLock()
    page, err := w.loadPageVersion(title, version)
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
    page, err := w.loadPageVersion(title, 0)
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
    if request.Method != http.MethodPost {
        http.Error(writer, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    if err := w.limiter.Acquire(); err != nil {
        http.Error(writer, err.Error(), http.StatusServiceUnavailable)
        return
    }
    defer w.limiter.Release()

    if err := request.ParseForm(); err != nil {
        http.Error(writer, err.Error(), http.StatusBadRequest)
        return
    }

    body := request.FormValue("body")
    comment := request.FormValue("comment")
    page := &Page{Title: title, RawBody: body}

    w.mu.Lock()
    err := w.savePage(page, comment)
    w.mu.Unlock()

    if err != nil {
        http.Error(writer, err.Error(), http.StatusInternalServerError)
        return
    }

    http.Redirect(writer, request, "/view/"+title, http.StatusFound)
}

func (w *Wiki) historyHandler(writer http.ResponseWriter, request *http.Request, title string) {
    if err := w.limiter.Acquire(); err != nil {
        http.Error(writer, err.Error(), http.StatusServiceUnavailable)
        return
    }
    defer w.limiter.Release()

    w.mu.RLock()
    history, err := getPageHistory(title)
    w.mu.RUnlock()

    if err != nil {
        http.Error(writer, err.Error(), http.StatusInternalServerError)
        return
    }

    data := struct {
        Title   string
        History []*PageVersion
    }{title, history}

    w.renderTemplate(writer, "history", data)
}

func (w *Wiki) diffHandler(writer http.ResponseWriter, request *http.Request, title string) {
    if err := w.limiter.Acquire(); err != nil {
        http.Error(writer, err.Error(), http.StatusServiceUnavailable)
        return
    }
    defer w.limiter.Release()

    oldVersion, err := strconv.ParseInt(request.URL.Query().Get("old"), 10, 64)
    if err != nil {
        http.Error(writer, "Invalid old version", http.StatusBadRequest)
        return
    }

    newVersion, err := strconv.ParseInt(request.URL.Query().Get("new"), 10, 64)
    if err != nil {
        http.Error(writer, "Invalid new version", http.StatusBadRequest)
        return
    }

    w.mu.RLock()
    diff, err := w.compareVersions(title, oldVersion, newVersion)
    w.mu.RUnlock()

    if err != nil {
        http.Error(writer, err.Error(), http.StatusInternalServerError)
        return
    }

    data := struct {
        Title string
        Diff  *VersionDiff
    }{title, diff}

    w.renderTemplate(writer, "diff", data)
}

func (w *Wiki) restoreHandler(writer http.ResponseWriter, request *http.Request, title string) {
    if request.Method != http.MethodPost {
        http.Error(writer, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    if err := w.limiter.Acquire(); err != nil {
        http.Error(writer, err.Error(), http.StatusServiceUnavailable)
        return
    }
    defer w.limiter.Release()

    version, err := strconv.ParseInt(request.URL.Query().Get("version"), 10, 64)
    if err != nil {
        http.Error(writer, "Invalid version", http.StatusBadRequest)
        return
    }

    w.mu.Lock()
    err = w.restoreVersion(title, version)
    w.mu.Unlock()

    if err != nil {
        http.Error(writer, err.Error(), http.StatusInternalServerError)
        return
    }

    http.Redirect(writer, request, "/view/"+title, http.StatusFound)
}

func (w *Wiki) serveDirectory(writer http.ResponseWriter) {
    w.mu.RLock()
    defer w.mu.RUnlock()

    pages := make(map[string]bool) // Use map to avoid duplicates
    baseDir := getDataDir()

    // Debug: print the directory we're scanning
    log.Printf("Scanning directory: %s", baseDir)

    files, err := os.ReadDir(baseDir)
    if err != nil {
        log.Printf("Error reading directory: %v", err)
        http.Error(writer, err.Error(), http.StatusInternalServerError)
        return
    }

    for _, file := range files {
        // Skip directories and non-.md files
        name := file.Name()
        if file.IsDir() || !strings.HasSuffix(name, ".md") {
            continue
        }

        pageName := strings.TrimSuffix(name, ".md")
        pages[pageName] = true
        log.Printf("Found page: %s", pageName)

        if len(pages) >= max_directory_pages {
            http.Error(writer, "too many pages", http.StatusInternalServerError)
            return
        }
    }

    // Convert map to sorted slice
    pageList := make([]string, 0, len(pages))
    for page := range pages {
        pageList = append(pageList, page)
    }
    sort.Strings(pageList)

    log.Printf("Final page list: %v", pageList)

    data := struct {
        Pages []string
    }{pageList}

    w.renderTemplate(writer, "directory", data)
}

func (w *Wiki) renderTemplate(writer http.ResponseWriter, tmpl string, data interface{}) {
    err := w.templates.ExecuteTemplate(writer, tmpl+".html", data)
    if err != nil {
        http.Error(writer, err.Error(), http.StatusInternalServerError)
    }
}

// More restrictive path validation following TigerStyle
var validPath = regexp.MustCompile(`^/(edit|save|view|history|diff|restore)/([a-zA-Z][a-zA-Z0-9_-]{0,63})$`)

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

func (w *Wiki) searchHandler(writer http.ResponseWriter, request *http.Request) {
    if err := w.limiter.Acquire(); err != nil {
        http.Error(writer, err.Error(), http.StatusServiceUnavailable)
        return
    }
    defer w.limiter.Release()

    query := request.URL.Query().Get("q")
    if query == "" {
        w.renderTemplate(writer, "search", nil)
        return
    }

    w.mu.RLock()
    results := w.searchPages(query)
    w.mu.RUnlock()

    data := struct {
        Query    string
        Results  []SearchResult
        Count    int
    }{
        Query:    query,
        Results:  results,
        Count:    len(results),
    }

    w.renderTemplate(writer, "search", data)
}

func (w *Wiki) searchPages(query string) []SearchResult {
    query = strings.ToLower(query)
    var results []SearchResult

    w.pageIndex.Range(func(key, value interface{}) bool {
        title := key.(string)
        content := value.(string)

        // Simple relevance scoring based on title and content matches
        relevance := 0.0

        titleLower := strings.ToLower(title)
        // Exact title match gets highest score
        if titleLower == query {
            relevance += 5.0
        } else if strings.Contains(titleLower, query) {
            relevance += 2.0
        }

        // Content matches
        contentLower := strings.ToLower(content)
        if strings.Contains(contentLower, query) {
            relevance += 1.0

            // Create a snippet of the matching content
            index := strings.Index(contentLower, query)
            start := math.Max(0, float64(index-50))
            end := math.Min(float64(len(content)), float64(index+len(query)+50))

            snippet := content[int(start):int(end)]
            if start > 0 {
                snippet = "..." + snippet
            }
            if end < float64(len(content)) {
                snippet = snippet + "..."
            }

            if relevance > 0 {
                results = append(results, SearchResult{
                    Title:     title,
                    Snippet:   snippet,
                    Relevance: relevance,
                })
            }
        }
        return true
    })

    // Sort results by relevance
    sort.Slice(results, func(i, j int) bool {
        return results[i].Relevance > results[j].Relevance
    })

    return results
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
    http.HandleFunc("/history/", wiki.makeHandler(wiki.historyHandler))
    http.HandleFunc("/diff/", wiki.makeHandler(wiki.diffHandler))
    http.HandleFunc("/restore/", wiki.makeHandler(wiki.restoreHandler))
	http.HandleFunc("/search", wiki.searchHandler)

    port := ":8080"
    log.Printf("Starting wiki server on %s", port)
    log.Fatal(http.ListenAndServe(port, nil))
}
