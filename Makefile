# Colors for output
GREEN := \033[0;32m
RED := \033[0;31m
YELLOW := \033[1;33m
NC := \033[0m # No Color

# Go variables
GO = go
GOFILES = wiki.go
BINARY = wiki

# Docker variables
DOCKER_IMAGE = go-wiki
DOCKER_CONTAINER = go-wiki-container
DOCKER_PORT = 8080

# Directories
DATA_DIR = data
COVERAGE_DIR = coverage

# targets
.PHONY: all run build clean deep-clean test coverage docker-build docker-run docker-clean docker-stop docker-rebuild

# default target - run the Go app locally
all: run

# build the Go application locally
build:
	@echo "$(YELLOW)Building $(BINARY)...$(NC)"
	@$(GO) build -o $(BINARY) $(GOFILES)
	@echo "$(GREEN)Build successful!$(NC)"

# run the Go application locally
run:
	@echo "$(YELLOW)Running $(BINARY)...$(NC)"
	@$(GO) run $(GOFILES)

# run tests with coverage
test:
	@echo "$(YELLOW)Running tests...$(NC)"
	@mkdir -p $(COVERAGE_DIR)
	@$(GO) test -v -race -coverprofile=$(COVERAGE_DIR)/coverage.out ./...
	@$(GO) tool cover -html=$(COVERAGE_DIR)/coverage.out -o $(COVERAGE_DIR)/coverage.html
	@echo "$(GREEN)Tests completed!$(NC)"

# generate coverage report
coverage: test
	@echo "$(YELLOW)Generating coverage report...$(NC)"
	@echo "$(GREEN)Coverage report generated at $(COVERAGE_DIR)/coverage.html$(NC)"
	@$(GO) tool cover -func=$(COVERAGE_DIR)/coverage.out

# clean up build files and coverage reports
clean:
	@echo "$(YELLOW)Cleaning build files and coverage reports...$(NC)"
	@$(GO) clean
	@rm -f $(BINARY)
	@rm -f coverage.out coverage.html
	@rm -rf $(COVERAGE_DIR)
	@rm -f tests/temp/*
	@echo "$(GREEN)Clean complete!$(NC)"

# deep clean - removes all generated content including wiki pages
deep-clean: clean
	@echo "$(RED)WARNING: Removing all wiki pages...$(NC)"
	@rm -f $(DATA_DIR)/*.md
	@rm -rf $(DATA_DIR)/*
	@echo "$(GREEN)Deep clean complete!$(NC)"
	@echo "$(YELLOW)All wiki pages have been removed$(NC)"

# build the Docker image
docker-build:
	@echo "$(YELLOW)Building Docker image...$(NC)"
	@docker build -t $(DOCKER_IMAGE) .
	@echo "$(GREEN)Docker image built successfully!$(NC)"

# run the Docker container
docker-run: docker-stop
	@echo "$(YELLOW)Starting Docker container...$(NC)"
	@docker run -p $(DOCKER_PORT):8080 -v $(PWD)/data:/app/data --name $(DOCKER_CONTAINER) $(DOCKER_IMAGE)

# stop and remove any existing Docker container
docker-stop:
	@echo "$(YELLOW)Stopping Docker container...$(NC)"
	@docker stop $(DOCKER_CONTAINER) 2>/dev/null || true
	@docker rm $(DOCKER_CONTAINER) 2>/dev/null || true
	@echo "$(GREEN)Docker container stopped!$(NC)"

# clean up Docker images and containers
docker-clean: docker-stop
	@echo "$(YELLOW)Removing Docker image...$(NC)"
	@docker rmi $(DOCKER_IMAGE)
	@echo "$(GREEN)Docker cleanup complete!$(NC)"

# rebuild and run Docker container
docker-rebuild: docker-clean docker-build docker-run

# help target
help:
	@echo "$(YELLOW)Available targets:$(NC)"
	@echo "  $(GREEN)all$(NC)          : Default target, runs the application"
	@echo "  $(GREEN)build$(NC)        : Build the application"
	@echo "  $(GREEN)run$(NC)          : Run the application locally"
	@echo "  $(GREEN)test$(NC)         : Run tests with coverage"
	@echo "  $(GREEN)coverage$(NC)     : Generate and show coverage report"
	@echo "  $(GREEN)clean$(NC)        : Clean build files and coverage reports"
	@echo "  $(RED)deep-clean$(NC)   : Clean everything including wiki pages"
	@echo "  $(GREEN)docker-build$(NC) : Build Docker image"
	@echo "  $(GREEN)docker-run$(NC)   : Run Docker container"
	@echo "  $(GREEN)docker-clean$(NC) : Clean Docker artifacts"
	@echo "  $(GREEN)docker-rebuild$(NC): Rebuild and run Docker container"
	@echo "  $(GREEN)help$(NC)         : Show this help message"
