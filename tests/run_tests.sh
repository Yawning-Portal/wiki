#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo "Running Wiki Tests..."
echo "===================="

# Ensure required directories exist
mkdir -p data
mkdir -p templates

# Run tests with verbose output and race detection
go test -v -race ./...

if [ $? -eq 0 ]; then
    echo -e "${GREEN}All tests passed!${NC}"

    # Run tests again with coverage
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html
    echo "Coverage report generated at coverage.html"
else
    echo -e "${RED}Tests failed!${NC}"
    exit 1
fi
