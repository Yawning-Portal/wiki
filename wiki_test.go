package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// constants for testing
const (
	testMaxPageSize = 1 << 20 // 1MB
)

type TestHelper struct {
	t       *testing.T
	tempDir string
	wiki    *Wiki
	server  *httptest.Server
	mutex   sync.Mutex	// protect file operations during tests
}

func newTestHelper(t *testing.T) *TestHelper {
	t.Helper()

	// create temporary directory for test data
	tempDir, err := os.MkdirTemp("", "wiki-test-*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}

	// Create data directory
	dataDir := filepath.Join(tempDir, "data")
	err = os.MkdirAll(dataDir, 0755)
	if err != nil {
		t.Fatalf("failed to create data directory: %v", err)
	}

	// Copy templates
	err = copyDir("templates", filepath.Join(tempDir, "templates"))
	if err != nil {
		t.Fatalf("failed to copy templates: %v", err)
	}

	// Initialize wiki with temp directory
	os.Setenv("WIKI_DATA_DIR", dataDir)
	wiki, err := NewWiki()
	if err != nil {
		t.Fatalf("failed to create wiki: %v", err)
	}

	// Debug handler that wraps all requests
	debugHandler := func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Handling %s request to %s", r.Method, r.URL.Path)

		// Apply rate limiting to all requests
		if err := wiki.limiter.Acquire(); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		defer wiki.limiter.Release()

		// Use the same routing logic as main()
		switch {
		case r.URL.Path == "/":
			http.Redirect(w, r, "/view/FrontPage", http.StatusFound)

		case strings.HasPrefix(r.URL.Path, "/view/"):
			// Check for Directory specifically
			if strings.HasSuffix(r.URL.Path, "Directory") {
				wiki.serveDirectory(w)
				return
			}
			wiki.makeHandler(wiki.viewHandler).ServeHTTP(w, r)

		case strings.HasPrefix(r.URL.Path, "/edit/"):
			wiki.makeHandler(wiki.editHandler).ServeHTTP(w, r)

		case strings.HasPrefix(r.URL.Path, "/save/"):
			wiki.makeHandler(wiki.saveHandler).ServeHTTP(w, r)

		case strings.HasPrefix(r.URL.Path, "/history/"):
			wiki.makeHandler(wiki.historyHandler).ServeHTTP(w, r)

		case strings.HasPrefix(r.URL.Path, "/restore/"):
			wiki.makeHandler(wiki.restoreHandler).ServeHTTP(w, r)

		case strings.HasPrefix(r.URL.Path, "/search"):  // note: no trailing slash
			wiki.searchHandler(w, r)

		case strings.HasPrefix(r.URL.Path, "/diff/"):
			wiki.makeHandler(wiki.diffHandler).ServeHTTP(w, r)

		case r.URL.Path == "/":
			http.Redirect(w, r, "/view/FrontPage", http.StatusFound)

		default:
			t.Logf("Not found: %s", r.URL.Path)
			http.NotFound(w, r)
		}

	}

	server := httptest.NewServer(http.HandlerFunc(debugHandler))

	return &TestHelper{
		t:       t,
		tempDir: tempDir,
		wiki:    wiki,
		server:  server,
	}
}

func (h *TestHelper) cleanup() {
	h.server.Close()
	os.RemoveAll(h.tempDir)
}

func (h *TestHelper) createTestPage(title, content string) {
	h.t.Helper()
	h.mutex.Lock()
	defer h.mutex.Unlock()

	page := &Page{
		Title:      title,
		RawBody:    content,
		Size_bytes: int64(len(content)),
	}

	err := h.wiki.savePage(page, "Test page creation")
	if err != nil {
		h.t.Fatalf("failed to create test page: %v", err)
	}
}

func (h *TestHelper) assertPageContent(title, expectedContent string) {
	h.t.Helper()

	resp, err := http.Get(h.server.URL + "/view/" + title)
	if err != nil {
		h.t.Fatalf("failed to get page: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("failed to read response body: %v", err)
	}

	// Check if the content is somewhere in the HTML response
	if !strings.Contains(string(body), expectedContent) {
		h.t.Errorf("page content not found\ngot HTML: %s\nexpected to contain: %s", body, expectedContent)
	}
}

// Test basic page operations
func TestPageOperations(t *testing.T) {
	h := newTestHelper(t)
	defer h.cleanup()

	tests := []struct {
		name    string
		title   string
		content string
	}{
		{
			name:    "valid page",
			title:   "TestPage",
			content: "Test content",
		},
		{
			name:    "empty page",
			title:   "EmptyPage",
			content: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h.createTestPage(tt.title, tt.content)
			h.assertPageContent(tt.title, tt.content)
		})
	}
}

// Test validation and error cases
func TestValidation(t *testing.T) {
	h := newTestHelper(t)
	defer h.cleanup()

	tests := []struct {
		name           string
		title          string
		content        string
		expectedStatus int
	}{
		{
			name:           "invalid title chars",
			title:         "Test/Page",
			content:       "Test content",
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "content too large",
			title:         "LargePage",
			content:       strings.Repeat("a", testMaxPageSize+1),
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Post(
				h.server.URL+"/save/"+tt.title,
				"application/x-www-form-urlencoded",
				strings.NewReader("body="+tt.content),
				)
			if err != nil {
				t.Fatalf("failed to post page: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.expectedStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("got status %d, want %d. Response: %s", resp.StatusCode, tt.expectedStatus, body)
			}
		})
	}
}

// Test concurrent access
func TestConcurrentAccess(t *testing.T) {
	h := newTestHelper(t)
	defer h.cleanup()

	// Create initial page
	h.createTestPage("ConcurrentPage", "Initial content")

	// Run concurrent requests
	var wg sync.WaitGroup
	concurrentRequests := 10
	errorChan := make(chan error, concurrentRequests)

	for i := 0; i < concurrentRequests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			if i%2 == 0 {
				// Read operation
				resp, err := http.Get(h.server.URL + "/view/ConcurrentPage")
				if err != nil {
					errorChan <- fmt.Errorf("read failed: %v", err)
					return
				}
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					body, _ := io.ReadAll(resp.Body)
					errorChan <- fmt.Errorf("read returned unexpected status %d, body: %s",
						resp.StatusCode, string(body))
				}
			} else {
				// Write operation
				// Create form data
				formData := url.Values{
					"body": {fmt.Sprintf("Update %d", i)},
				}

				// Create POST request
				req, err := http.NewRequest(
					"POST",
					h.server.URL+"/save/ConcurrentPage",
					strings.NewReader(formData.Encode()),
					)
				if err != nil {
					errorChan <- fmt.Errorf("failed to create request: %v", err)
					return
				}

				// Set headers
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

				// Debug request
				t.Logf("Making POST request to %s with body: %s", req.URL, formData.Encode())

				// Send request
				client := &http.Client{
					CheckRedirect: func(req *http.Request, via []*http.Request) error {
						return http.ErrUseLastResponse // Don't follow redirects
					},
				}

				resp, err := client.Do(req)
				if err != nil {
					errorChan <- fmt.Errorf("write request failed: %v", err)
					return
				}
				defer resp.Body.Close()

				// Check response
				body, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != http.StatusFound {
					errorChan <- fmt.Errorf("write returned unexpected status %d, headers: %v, body: %s",
						resp.StatusCode, resp.Header, string(body))
					return
				}

				// Verify redirect location
				location := resp.Header.Get("Location")
				if location != "/view/ConcurrentPage" {
					errorChan <- fmt.Errorf("unexpected redirect location: %s", location)
				}
			}
		}(i)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(errorChan)

	// Check for any errors
	var errors []string
	for err := range errorChan {
		errors = append(errors, err.Error())
	}

	if len(errors) > 0 {
		t.Errorf("concurrent operations failed with %d errors:\n%s",
			len(errors), strings.Join(errors, "\n"))
	}

	// Verify final content exists
	finalResp, err := http.Get(h.server.URL + "/view/ConcurrentPage")
	if err != nil {
		t.Fatalf("failed to get final page state: %v", err)
	}
	defer finalResp.Body.Close()

	if finalResp.StatusCode != http.StatusOK {
		t.Errorf("final page state returned status %d", finalResp.StatusCode)
	}
}

// Test directory listing
func TestDirectory(t *testing.T) {
	h := newTestHelper(t)
	defer h.cleanup()

	// Create test pages
	testPages := []struct {
		title   string
		content string
	}{
		{"TestPage1", "Content 1"},
		{"TestPage2", "Content 2"},
		{"TestPage3", "Content 3"},
	}

	for _, p := range testPages {
		h.createTestPage(p.title, p.content)
	}

	// Get directory listing
	resp, err := http.Get(h.server.URL + "/view/Directory")
	if err != nil {
		t.Fatalf("failed to get directory: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read directory response: %v", err)
	}

	// Check that all test pages are listed
	for _, p := range testPages {
		if !strings.Contains(string(body), p.title) {
			t.Errorf("directory listing missing page %q", p.title)
		}
	}
}

// Utility functions remain the same
func copyDir(src, dst string) error {
	err := os.MkdirAll(dst, 0755)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			err = copyDir(srcPath, dstPath)
			if err != nil {
				return err
			}
		} else {
			err = copyFile(srcPath, dstPath)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

func TestPageCache(t *testing.T) {
	h := newTestHelper(t)
	defer h.cleanup()

	// Create initial page
	h.createTestPage("CachePage", "Initial content")

	// Access the page to populate cache
	resp, err := http.Get(h.server.URL + "/view/CachePage")
	if err != nil {
		t.Fatalf("Failed to get page: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status OK, got %v", resp.StatusCode)
	}

	// Test cache hit
	page1, ok := h.wiki.pageCache.get("CachePage")
	if !ok || page1 == nil {
		t.Error("Expected cache hit, got miss")
	}

	// Wait for cache to expire
	time.Sleep(page_cache_ttl_ms * time.Millisecond)

	// Should get cache miss after expiration
	page2, ok := h.wiki.pageCache.get("CachePage")
	if ok || page2 != nil {
		t.Error("Expected cache miss after expiration")
	}

	// Verify we can still access the page after cache miss
	resp, err = http.Get(h.server.URL + "/view/CachePage")
	if err != nil {
		t.Fatalf("Failed to get page after cache expiration: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status OK after cache miss, got %v", resp.StatusCode)
	}
}

func TestVersionControl(t *testing.T) {
	h := newTestHelper(t)
	defer h.cleanup()

	// Create page with multiple versions
	title := "VersionTest"
	h.createTestPage(title, "Version 1")
	h.createTestPage(title, "Version 2")
	h.createTestPage(title, "Version 3")

	// Test version retrieval
	page, err := h.wiki.loadPageVersion(title, 1)
	if err != nil {
		t.Fatalf("Failed to load version 1: %v", err)
	}
	if page.RawBody != "Version 1" {
		t.Errorf("Expected 'Version 1', got '%s'", page.RawBody)
	}

	// Test version comparison
	diff, err := h.wiki.compareVersions(title, 1, 2)
	if err != nil {
		t.Fatalf("Failed to compare versions: %v", err)
	}
	if len(diff.Diffs) == 0 {
		t.Error("Expected non-empty diff")
	}
}

func TestSearch(t *testing.T) {
    h := newTestHelper(t)
    defer h.cleanup()

    // Create pages with searchable content
    h.createTestPage("SearchTest1", "This is a test page about cats")
    h.createTestPage("SearchTest2", "Another page about dogs")
    h.createTestPage("CatPage", "More cat content here")

    // Test empty search
    resp, err := http.Get(h.server.URL + "/search")
    if err != nil {
        t.Fatalf("Failed to execute search: %v", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        t.Errorf("Expected status OK for empty search, got %v", resp.StatusCode)
    }

    // Test search with query
    resp, err = http.Get(h.server.URL + "/search?q=cat")
    if err != nil {
        t.Fatalf("Failed to execute search: %v", err)
    }
    defer resp.Body.Close()

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        t.Fatalf("Failed to read response: %v", err)
    }

    // Check that both cat pages are found
    if !strings.Contains(string(body), "SearchTest1") || !strings.Contains(string(body), "CatPage") {
        t.Error("Search results missing expected pages")
    }

    // Check that dog page is not found
    if strings.Contains(string(body), "SearchTest2") {
        t.Error("Search results contain unexpected page")
    }
}

func TestErrorHandling(t *testing.T) {
	h := newTestHelper(t)
	defer h.cleanup()

	tests := []struct {
		name    string
		title   string
		content string
		wantErr error
	}{
		{
			name:    "empty title",
			title:   "",
			content: "Some content",
			wantErr: ErrEmptyTitle,
		},
		{
			name:    "content too large",
			title:   "LargePage",
			content: strings.Repeat("a", max_page_size_bytes+1),
			wantErr: ErrPageTooLarge,
		},
		{
			name:    "invalid characters in title",
			title:   "Invalid/Title",
			content: "Content",
			wantErr: fmt.Errorf("title contains invalid characters"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page := &Page{
				Title:   tt.title,
				RawBody: tt.content,
			}
			err := h.wiki.savePage(page, "test")
			if err == nil || err.Error() != tt.wantErr.Error() {
				t.Errorf("Expected error %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestRequestLimiter(t *testing.T) {
	limiter := NewRequestLimiter()

	// Test successful acquisition
	for i := 0; i < max_concurrent_requests; i++ {
		if err := limiter.Acquire(); err != nil {
			t.Errorf("Failed to acquire limiter: %v", err)
		}
	}

	// Test limit exceeded
	if err := limiter.Acquire(); err != ErrServerBusy {
		t.Errorf("Expected ErrServerBusy, got %v", err)
	}

	// Test release
	limiter.Release()
	if err := limiter.Acquire(); err != nil {
		t.Errorf("Failed to acquire after release: %v", err)
	}
}

func TestHTTPHandlers(t *testing.T) {
	h := newTestHelper(t)
	defer h.cleanup()

	// Create a test page for the "view existing page" test
	h.createTestPage("TestPage", "Test content")

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "view existing page",
			method:     "GET",
			path:       "/view/TestPage",
			wantStatus: http.StatusOK,
		},
		{
			name:       "view nonexistent page",
			method:     "GET",
			path:       "/view/NonExistent",
			wantStatus: http.StatusFound, // Should redirect to edit
		},
		{
			name:       "save page invalid method",
			method:     "GET",
			path:       "/save/TestPage",
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			var err error

			if tt.body != "" {
				req, err = http.NewRequest(tt.method, h.server.URL+tt.path,
					strings.NewReader(tt.body))
			} else {
				req, err = http.NewRequest(tt.method, h.server.URL+tt.path, nil)
			}

			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			// Don't follow redirects for this test
			client := &http.Client{
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Failed to send request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("Expected status %d, got %d", tt.wantStatus, resp.StatusCode)
			}

			// Verify redirect location for nonexistent page
			if tt.wantStatus == http.StatusFound {
				location := resp.Header.Get("Location")
				expectedLocation := "/edit/" + strings.TrimPrefix(tt.path, "/view/")
				if location != expectedLocation {
					t.Errorf("Expected redirect to %q, got %q", expectedLocation, location)
				}
			}
		})
	}
}

func TestHistoryAndRestore(t *testing.T) {
    h := newTestHelper(t)
    defer h.cleanup()

    // Create page with multiple versions
    h.createTestPage("HistoryPage", "Version 1")
    h.createTestPage("HistoryPage", "Version 2")
    h.createTestPage("HistoryPage", "Version 3")

    // Test history handler
    resp, err := http.Get(h.server.URL + "/history/HistoryPage")
    if err != nil {
        t.Fatalf("Failed to get history: %v", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        t.Errorf("Expected status OK, got %v", resp.StatusCode)
    }

    // Test restore operation
    form := url.Values{}
    form.Set("version", "1")

    req, err := http.NewRequest(
        "POST",
        h.server.URL + "/restore/HistoryPage?version=1",
        strings.NewReader(form.Encode()),
    )
    if err != nil {
        t.Fatalf("Failed to create restore request: %v", err)
    }

    // Set content type for form submission
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

    // Use client that doesn't follow redirects
    client := &http.Client{
        CheckRedirect: func(req *http.Request, via []*http.Request) error {
            return http.ErrUseLastResponse
        },
    }

    resp, err = client.Do(req)
    if err != nil {
        t.Fatalf("Failed to execute restore: %v", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusFound {
        t.Errorf("Expected redirect after restore, got %v", resp.StatusCode)
    }

    // Verify the page was restored to version 1
    resp, err = http.Get(h.server.URL + "/view/HistoryPage")
    if err != nil {
        t.Fatalf("Failed to view restored page: %v", err)
    }
    defer resp.Body.Close()

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        t.Fatalf("Failed to read response body: %v", err)
    }

    if !strings.Contains(string(body), "Version 1") {
        t.Error("Page was not restored to version 1")
    }
}

func TestEditHandler(t *testing.T) {
    h := newTestHelper(t)
    defer h.cleanup()

    tests := []struct {
        name       string
        title      string
        method     string
        wantStatus int
    }{
        {
            name:       "edit existing page",
            title:      "ExistingPage",
            method:     "GET",
            wantStatus: http.StatusOK,
        },
        {
            name:       "edit new page",
            title:      "NewPage",
            method:     "GET",
            wantStatus: http.StatusOK,
        },
    }

    // Create test page for existing page test
    h.createTestPage("ExistingPage", "Test content")

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            req, err := http.NewRequest(tt.method, h.server.URL+"/edit/"+tt.title, nil)
            if err != nil {
                t.Fatalf("Failed to create request: %v", err)
            }

            resp, err := http.DefaultClient.Do(req)
            if err != nil {
                t.Fatalf("Failed to execute request: %v", err)
            }
            defer resp.Body.Close()

            if resp.StatusCode != tt.wantStatus {
                t.Errorf("Expected status %v, got %v", tt.wantStatus, resp.StatusCode)
            }

            // Read body to verify content
            body, err := io.ReadAll(resp.Body)
            if err != nil {
                t.Fatalf("Failed to read response body: %v", err)
            }

            // For existing page, verify content is present
            if tt.name == "edit existing page" {
                if !strings.Contains(string(body), "Test content") {
                    t.Error("Edit page missing existing content")
                }
            }
        })
    }
}

func TestRateLimiter(t *testing.T) {
    h := newTestHelper(t)
    defer h.cleanup()

    // Create test page that we'll request repeatedly
    h.createTestPage("LimitTest", "Test content")

    // Acquire all slots to ensure they're empty
    limiter := NewRequestLimiter()
    for i := 0; i < max_concurrent_requests; i++ {
        err := limiter.Acquire()
        if err != nil {
            t.Fatalf("Failed to acquire initial permits: %v", err)
        }
    }

    // Try one more - should fail
    err := limiter.Acquire()
    if err != ErrServerBusy {
        t.Errorf("Expected ErrServerBusy when over limit, got: %v", err)
    }

    // Release one and try again - should succeed
    limiter.Release()
    err = limiter.Acquire()
    if err != nil {
        t.Errorf("Expected success after release, got: %v", err)
    }
}

func TestDiffHandler(t *testing.T) {
    h := newTestHelper(t)
    defer h.cleanup()

    // Create page with multiple versions
    h.createTestPage("DiffPage", "Version 1 content")
    h.createTestPage("DiffPage", "Version 2 content")

    // Test diff view with correct versions
    resp, err := http.Get(h.server.URL + "/diff/DiffPage?old=1&new=2")
    if err != nil {
        t.Fatalf("Failed to get diff: %v", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        t.Errorf("Expected status OK, got %v", resp.StatusCode)
    }

    // Read body to verify diff content
    body, err := io.ReadAll(resp.Body)
    if err != nil {
        t.Fatalf("Failed to read response body: %v", err)
    }

    content := string(body)
    if !strings.Contains(content, "Version 1") || !strings.Contains(content, "Version 2") {
        t.Error("Diff content missing version information")
    }

    // Test invalid version params
    resp, err = http.Get(h.server.URL + "/diff/DiffPage?old=invalid&new=2")
    if err != nil {
        t.Fatalf("Failed to get diff with invalid version: %v", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusBadRequest {
        t.Errorf("Expected status BadRequest for invalid version, got %v", resp.StatusCode)
    }
}

func TestRootHandler(t *testing.T) {
    h := newTestHelper(t)
    defer h.cleanup()

    tests := []struct {
        name         string
        path         string
        wantStatus   int
        wantLocation string
    }{
        {
            name:         "root path",
            path:         "/",
            wantStatus:   http.StatusFound, // 302 redirect
            wantLocation: "/view/FrontPage",
        },
        {
            name:         "invalid path",
            path:         "/invalid",
            wantStatus:   http.StatusNotFound,
            wantLocation: "",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Use client that doesn't automatically follow redirects
            client := &http.Client{
                CheckRedirect: func(req *http.Request, via []*http.Request) error {
                    return http.ErrUseLastResponse
                },
            }

            resp, err := client.Get(h.server.URL + tt.path)
            if err != nil {
                t.Fatalf("Failed to get %s: %v", tt.path, err)
            }
            defer resp.Body.Close()

            if resp.StatusCode != tt.wantStatus {
                t.Errorf("Expected status %d for %s, got %d",
                    tt.wantStatus, tt.path, resp.StatusCode)
            }

            if tt.wantLocation != "" {
                location := resp.Header.Get("Location")
                if location != tt.wantLocation {
                    t.Errorf("Expected redirect to %q, got %q",
                        tt.wantLocation, location)
                }
            }
        })
    }
}
