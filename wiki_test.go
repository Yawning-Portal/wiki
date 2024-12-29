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

        // Use the same routing logic as main()
        switch {
        case r.URL.Path == "/":
            http.Redirect(w, r, "/view/FrontPage", http.StatusFound)

        case strings.HasPrefix(r.URL.Path, "/save/"):
            if r.Method != "POST" {
                t.Logf("Method not allowed: %s", r.Method)
                http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
                return
            }
            wiki.makeHandler(wiki.saveHandler).ServeHTTP(w, r)

        case strings.HasPrefix(r.URL.Path, "/view/"):
            wiki.makeHandler(wiki.viewHandler).ServeHTTP(w, r)

        case strings.HasPrefix(r.URL.Path, "/edit/"):
            wiki.makeHandler(wiki.editHandler).ServeHTTP(w, r)

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

	filename := filepath.Join(h.tempDir, "data", title+".md")
	err := os.WriteFile(filename, []byte(content), 0600)
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
