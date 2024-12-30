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

// Constants with compile-time checks using const expressions
const (
    // Maximum size for page content to prevent memory exhaustion
    max_page_size_bytes = 1 << 20 // 1MB

    // Maximum number of concurrent requests to prevent resource exhaustion
    max_concurrent_requests = 100

    // Maximum number of pages to list in directory
    max_directory_pages = 1000

    // Maximum number of versions to keep per page
    max_versions_per_page = 1000

	// 1 second cache TTL
	page_cache_ttl_ms = 1000

	max_batch_size = 10

	batch_timeout_ms = 100

	// default data directory if not specified
	default_data_dir = "data"

    // Compile-time checks using constant expressions
    _max_page_check        = uint64(max_page_size_bytes - (1 << 20))     // Will fail if > 1MB
    _max_versions_check    = uint64(max_versions_per_page - 1000)        // Will fail if > 1000
    _max_concurrent_check  = uint64(max_concurrent_requests - 100)        // Will fail if > 100
    _max_directory_check   = uint64(max_directory_pages - 1000)          // Will fail if > 1000
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

type PageCache struct {
    cache sync.Map  // map[string]*Page
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

// BatchOperation represents a pending page operation
type BatchOperation struct {
    Page    *Page
    Comment string
    Done    chan error // Signal completion to caller
}

// Read batching
type ReadOperation struct {
    Title   string
    Version int64
    Done    chan ReadResult
}

type ReadResult struct {
    Page *Page
    Err  error
}

// Batch processing metrics
type BatchMetrics struct {
    Processed  int64
    Errors     int64
    BatchesFull int64
    BatchesPartial int64
}

// PageBatcher handles batched page operations
type PageBatcher struct {
    operations chan BatchOperation
    readOps    chan ReadOperation
    maxBatch   int
    timeout    time.Duration
    wiki       *Wiki
    metrics    BatchMetrics
    mu         sync.RWMutex
}

// Assert that the page is valid before any operation (uses paired assertions)
func (p *Page) validate() error {
    // check title
    if p.Title == "" {
        return ErrEmptyTitle
    }

    // Check title length - reasonable maximum for file systems
    if len(p.Title) > 64 {
        return fmt.Errorf("title exceeds maximum length of 64 characters")
    }

    // Check title characters using your existing validPath regexp
    if !validPath.MatchString("/view/" + p.Title) {
        return fmt.Errorf("title contains invalid characters")
    }

    // Check content size
    if int64(len(p.RawBody)) > max_page_size_bytes {
        return ErrPageTooLarge
    }

    // Paired assertion: verify title and content consistency
    if len(p.RawBody) > 0 && p.Title == "" {
        return fmt.Errorf("content present but title missing")
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

    // Try cache first for current versions
    if version == 0 {
        if cached, ok := w.pageCache.get(title); ok {
            return cached, nil
        }
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

    // Update search index and cache with current version only
    if version <= 0 || version == metadata.CurrentVersion {
        w.pageIndex.Store(title, string(content))
        w.pageCache.set(page)
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
    mu        sync.RWMutex 	// Protects concurrent page operations
	pageIndex sync.Map     	// Used for search functionality, stores page content in memory
	batcher   *PageBatcher 	// Page batching for performance
	pageCache PageCache		// Not a ptr
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
        pageCache: PageCache{},
    }

    // Create batcher after wiki is initialized
    wiki.batcher = NewPageBatcher(wiki, 10, 100*time.Millisecond)

    // Initialize search index
    if err := wiki.initializeSearchIndex(); err != nil {
        return nil, err
    }

    return wiki, nil
}

// Batching

// Cache implementation
func (c *PageCache) get(title string) (*Page, bool) {
    if value, ok := c.cache.Load(title); ok {
        page := value.(*Page)
        // Check if cache entry is still valid
        if time.Since(time.Unix(page.Modified_at, 0)).Milliseconds() < page_cache_ttl_ms {
            return page, true
        }
        // Expired, remove from cache
        c.cache.Delete(title)
    }
    return nil, false
}

func (c *PageCache) set(page *Page) {
    c.cache.Store(page.Title, page)
}

func NewPageBatcher(wiki *Wiki, maxBatch int, timeout time.Duration) *PageBatcher {
    if maxBatch <= 0 {
        maxBatch = max_batch_size
    }
    if timeout <= 0 {
        timeout = batch_timeout_ms * time.Millisecond
    }

    batcher := &PageBatcher{
        operations: make(chan BatchOperation, maxBatch),
        readOps:    make(chan ReadOperation, maxBatch),
        maxBatch:   maxBatch,
        timeout:    timeout,
        wiki:       wiki,
    }
    go batcher.processLoop()  // Handle writes
    go batcher.processReads() // Handle reads
    return batcher
}

func (b *PageBatcher) processReads() {
    batch := make([]*ReadOperation, 0, b.maxBatch)
    timer := time.NewTimer(b.timeout)

    for {
        select {
        case op := <-b.readOps:
            batch = append(batch, &op)

            if len(batch) >= b.maxBatch {
                b.processReadBatch(batch)
                batch = batch[:0]
                timer.Reset(b.timeout)
            }

        case <-timer.C:
            if len(batch) > 0 {
                b.processReadBatch(batch)
                batch = batch[:0]
            }
            timer.Reset(b.timeout)
        }
    }
}

func (b *PageBatcher) processReadBatch(batch []*ReadOperation) {
    b.wiki.mu.RLock()
    defer b.wiki.mu.RUnlock()

    for _, op := range batch {
        page, err := b.wiki.loadPageVersion(op.Title, op.Version)
        op.Done <- ReadResult{Page: page, Err: err}
    }
}

func (b *PageBatcher) processLoop() {
    batch := make([]*BatchOperation, 0, b.maxBatch)
    timer := time.NewTimer(b.timeout)

    for {
        select {
        case op := <-b.operations:
            batch = append(batch, &op)

			// Process if batch is full
            if len(batch) >= b.maxBatch {
                b.processBatch(batch)
                batch = batch[:0]
                timer.Reset(b.timeout)

                b.mu.Lock()
                b.metrics.BatchesFull++
                b.mu.Unlock()
            }

        case <-timer.C:
            if len(batch) > 0 {
				// Process partial batch on timeout
                b.processBatch(batch)
                batch = batch[:0]

                b.mu.Lock()
                b.metrics.BatchesPartial++
                b.mu.Unlock()
            }
            timer.Reset(b.timeout)
        }
    }
}

func (b *PageBatcher) processBatch(batch []*BatchOperation) {
    // Lock once for the entire batch
    b.wiki.mu.Lock()
    defer b.wiki.mu.Unlock()

    for _, op := range batch {
        err := b.wiki.savePage(op.Page, op.Comment)
        op.Done <- err

        b.mu.Lock()
        b.metrics.Processed++
        if err != nil {
            b.metrics.Errors++
        }
        b.mu.Unlock()
    }
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

    // Get version from query params
    version := int64(0)
    if v := request.URL.Query().Get("version"); v != "" {
        var err error
        version, err = strconv.ParseInt(v, 10, 64)
        if err != nil {
            http.Error(writer, "Invalid version number", http.StatusBadRequest)
            return
        }
    }

    // Use batched read
    result := make(chan ReadResult, 1)
    w.batcher.readOps <- ReadOperation{
        Title:   title,
        Version: version,
        Done:  result,
    }

    // Wait for result
    readResult := <-result
    if readResult.Err != nil {
        if os.IsNotExist(readResult.Err) {
            http.Redirect(writer, request, "/edit/"+title, http.StatusFound)
            return
        }
        http.Error(writer, readResult.Err.Error(), http.StatusInternalServerError)
        return
    }

    w.renderTemplate(writer, "view", readResult.Page)
}

func (w *Wiki) editHandler(writer http.ResponseWriter, request *http.Request, title string) {
    if err := w.limiter.Acquire(); err != nil {
        http.Error(writer, err.Error(), http.StatusServiceUnavailable)
        return
    }
    defer w.limiter.Release()

    result := make(chan ReadResult, 1)
    w.batcher.readOps <- ReadOperation{
        Title:   title,
        Version: 0, // Always get latest for edit
        Done:  result,
    }

    readResult := <-result
    if readResult.Err != nil && !os.IsNotExist(readResult.Err) {
        http.Error(writer, readResult.Err.Error(), http.StatusInternalServerError)
        return
    }

    if readResult.Err != nil {
        // Page doesn't exist, create new
        readResult.Page = &Page{Title: title}
    }

    w.renderTemplate(writer, "edit", readResult.Page)
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

    // Read both versions in parallel
    oldResult := make(chan ReadResult, 1)
    newResult := make(chan ReadResult, 1)

    w.batcher.readOps <- ReadOperation{
        Title:   title,
        Version: oldVersion,
        Done:  oldResult,
    }
    w.batcher.readOps <- ReadOperation{
        Title:   title,
        Version: newVersion,
        Done:  newResult,
    }

    // Wait for both results
    oldReadResult := <-oldResult
    newReadResult := <-newResult

    if oldReadResult.Err != nil {
        http.Error(writer, oldReadResult.Err.Error(), http.StatusInternalServerError)
        return
    }
    if newReadResult.Err != nil {
        http.Error(writer, newReadResult.Err.Error(), http.StatusInternalServerError)
        return
    }

    // Generate diff
    dmp := diffmatchpatch.New()
    diffs := dmp.DiffMain(oldReadResult.Page.RawBody, newReadResult.Page.RawBody, true)

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

    data := struct {
        Title string
        Diff  *VersionDiff
    }{title, &VersionDiff{
        OldVersion: oldVersion,
        NewVersion: newVersion,
        Diffs:      segments,
    }}

    w.renderTemplate(writer, "diff", data)
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

    done := make(chan error, 1)
    w.batcher.operations <- BatchOperation{
        Page:    page,
        Comment: comment,
        Done:    done,
    }

    // Wait for batch operation to complete
	// Don't need direct mutex lock as batching system handles synchronization
    if err := <-done; err != nil {
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
