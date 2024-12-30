# 📚✍ Go Wiki

A lightweight, safety-focused wiki engine written in Go, designed to be simple, fast, and intuitive.

![Tests](https://github.com/github/docs/actions/workflows/test.yml/badge.svg)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](http://makeapullrequest.com)
[![Contributions Welcome](https://img.shields.io/badge/contributions-welcome-brightgreen.svg?style=flat)](https://github.com/yourusername/blazingly-fast-dev/issues)

## Motivation

I have used WorldAnvil and Fandom; both are fantastic in their own right, and offer comprehensive,
online solutions for "The Wiki" we often need when assembling complex universes.
However, both come with challenges:

* WorldAnvil's development is slow and the UI is **cumbersome**. It is **paid**.
* Fandom feels great as a user - it is tested, and the premier option in the space. However Fandom Wiki SETUP is **far from intuitive**.
  * Additionally, it lacks the ability to restrict access to certain pages or groups of pages based on user roles.

I've decided to build a third option which resolves these shortcomings.

## Features

- 🚀 Fast and lightweight
- 📝 Markdown support with HTML rendering
- 🔒 Built-in safety features
- 📱 Mobile-responsive design
- 🔄 Concurrent access handling
- 💾 Simple file-based storage
- 🐳 Docker support
- ✅ Comprehensive testing

## Quick Start

### Local Development

```bash
# Clone the repository
git clone https://github.com/yourusername/go-wiki.git
cd go-wiki

# Run directly
go run wiki.go

# Or build and run
make build
./wiki
```

### Docker

```bash
# Build and run with Docker
make docker-build
make docker-run
```

Visit `http://localhost:8080` to see your wiki.

## Development

### Prerequisites

- Go 1.20 or later
- Docker (optional)
- Make (optional)

### Testing

```bash
# Run all tests
./tests/run_tests.sh
```

### Project Structure

```
.
├── data/               # Wiki content
├── docs/               # Documentation
│   ├── api/            # API documentation
│   ├── developer/      # Developer guides
│   └── user/           # User guides
├── static/             # Static assets
├── templates/          # HTML templates
├── tests/              # Test files
├── Dockerfile
├── go.mod
├── Makefile
└── wiki.go
```

## Configuration

Environment variables:
- `WIKI_DATA_DIR`: Set custom data directory (default: "data")

## Documentation

For detailed information, please consult our comprehensive documentation:

* [User Guide](./docs/user/index.md)
* [Developer Documentation](./docs/developer/index.md)
* [API Documentation](./docs/api/index.md)

## Contributing

1. Fork the repository
2. Create your feature branch
3. Add tests for new features
4. Update documentation
5. Submit a pull request

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Acknowledgments

This project was inspired by the need for a simpler, more developer-friendly wiki solution. While great platforms like WorldAnvil and Fandom exist, Go Wiki aims to provide a lightweight, flexible alternative that prioritizes ease of use and customization.

---
*Built with ❤️ for the TTRPG community*
