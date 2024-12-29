# Go Wiki Developer Documentation

## Overview

Go Wiki is a simple yet robust wiki engine written in Go, designed with safety, performance, and developer experience in mind. This documentation covers the technical aspects, design decisions, and development guidelines.

## Core Principles

Following TigerStyle principles, Go Wiki is built on:

1. Safety First
   - Static memory allocation
   - Explicit bounds checking
   - Strong input validation
   - Concurrent access protection

2. Performance
   - Efficient file operations
   - Request batching
   - Memory-conscious design
   - Optimized template handling

3. Developer Experience
   - Clear code structure
   - Comprehensive testing
   - Predictable behavior
   - Well-documented interfaces

## Architecture

### Core Components

1. Page Management
   ```go
   type Page struct {
       Title       string
       Body        template.HTML
       RawBody     string
       Size_bytes  int64
       Modified_at int64
   }
   ```
   - Handles page storage and retrieval
   - Manages metadata
   - Ensures content validation

2. Request Handling
   ```go
   type Wiki struct {
       templates *template.Template
       limiter   *RequestLimiter
       mu        sync.RWMutex
   }
   ```
   - Route management
   - Concurrent access control
   - Template rendering

3. Safety Controls
   ```go
   const (
       max_page_size_bytes = 1 << 20      // 1MB
       max_concurrent_requests = 100
       max_directory_pages = 1000
   )
   ```
   - Resource limits
   - Input validation
   - Error handling

## Setup and Configuration

### Prerequisites
- Go 1.20 or later
- Docker (optional)
- Make (optional)

### Directory Structure
```
.
├── data/               # Wiki content
├── docs/               # Documentation
├── static/             # Static assets
├── templates/          # HTML templates
├── tests/              # Test files
├── Dockerfile
├── go.mod
├── Makefile
└── wiki.go
```

### Environment Variables
- `WIKI_DATA_DIR`: Set custom data directory (default: "data")

## Development Guidelines

### Code Style
1. Naming Conventions
   - Use snake_case for variables and functions
   - Add units to variable names (e.g., size_bytes)
   - Keep names descriptive and meaningful

2. Function Organization
   - Maximum 70 lines per function
   - Clear separation of concerns
   - Proper error handling

3. Testing
   - Write tests for both positive and negative cases
   - Include concurrent access tests
   - Test error conditions thoroughly

### Safety Practices

1. Input Validation
   ```go
   func (p *Page) validate() error {
       if p.Title == "" {
           return ErrEmptyTitle
       }
       if int64(len(p.RawBody)) > max_page_size_bytes {
           return ErrPageTooLarge
       }
       return nil
   }
   ```

2. Concurrency Control
   ```go
   // Always use mutex for file operations
   w.mu.Lock()
   defer w.mu.Unlock()
   ```

3. Resource Limits
   ```go
   // Use request limiter for concurrent access
   if err := w.limiter.Acquire(); err != nil {
       http.Error(writer, err.Error(), http.StatusServiceUnavailable)
       return
   }
   defer w.limiter.Release()
   ```

## Testing

### Running Tests
```bash
# Run all tests
./tests/run_tests.sh

# Run specific test
go test -v -run TestPageOperations
```

### Test Categories
1. Basic Operations
   - Page creation
   - Content retrieval
   - Metadata handling

2. Validation Tests
   - Invalid input
   - Size limits
   - Character restrictions

3. Concurrent Access
   - Multiple readers
   - Multiple writers
   - Race condition prevention

## Deployment

### Local Development
```bash
# Run directly
go run wiki.go

# Build and run
make build
./wiki
```

### Docker Deployment
```bash
# Build container
make docker-build

# Run container
make docker-run
```

## Common Tasks

### Adding a New Feature
1. Create tests first
2. Implement the feature
3. Ensure all tests pass
4. Document changes
5. Update this documentation if needed

### Modifying Templates
1. Edit files in `templates/`
2. Test all supported content types
3. Verify mobile responsiveness
4. Check accessibility

### Performance Optimization
1. Profile the application
2. Identify bottlenecks
3. Make targeted improvements
4. Verify with benchmarks
5. Document optimizations

## Troubleshooting

### Common Issues

1. Template Errors
   - Check template syntax
   - Verify file paths
   - Validate data structure

2. Concurrent Access Issues
   - Review mutex usage
   - Check request limiter
   - Verify error handling

3. File System Errors
   - Check permissions
   - Verify path configuration
   - Ensure proper cleanup

## Contributing

1. Fork the repository
2. Create a feature branch
3. Follow code style guidelines
4. Add tests for new features
5. Update documentation
6. Submit pull request

## Performance Considerations

### Optimization Tips
1. Use strings.Builder for string concatenation
2. Implement proper caching where appropriate
3. Batch file operations
4. Use efficient data structures
5. Profile before optimizing

### Resource Management
1. Monitor memory usage
2. Track file descriptors
3. Watch concurrent connections
4. Handle cleanup properly

## Security

### Best Practices
1. Validate all input
2. Sanitize output
3. Use proper file permissions
4. Implement rate limiting
5. Follow security guidelines

## Future Enhancements

Potential improvements to consider:
1. Caching layer for frequently accessed pages
2. Full-text search capability
3. Version control for pages
4. User authentication and authorization
5. API endpoints for programmatic access

Remember to follow TigerStyle principles when implementing any enhancements!
