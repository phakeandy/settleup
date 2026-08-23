.PHONY: build run

build:
	@echo "Building ..."
	go build -o server ./cmd/server

run: build
	@echo "Running ..."
	@./server
