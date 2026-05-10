BINARY := cadport
BUILD_DIR := build

$(BUILD_DIR)/$(BINARY): $(shell find . -name '*.go' -type f) go.mod
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY) .

.PHONY: build clean run
build: $(BUILD_DIR)/$(BINARY)

clean:
	rm -rf $(BUILD_DIR)

run: build
	$(BUILD_DIR)/$(BINARY) run
