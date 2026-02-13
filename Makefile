BINARY_NAME=go-callgraph-neo4j
CMD_PATH=./

.PHONY: dep build build_native clean vet

dep:
	go mod tidy -v
	go mod vendor

build_native: dep
	go build -mod=vendor -o ./$(BINARY_NAME) $(CMD_PATH)
	cp ./$(BINARY_NAME) $(GOPATH)/bin/$(BINARY_NAME)

build: dep
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -o ./$(BINARY_NAME) $(CMD_PATH)

vet:
	go vet ./...

clean:
	rm -f $(BINARY_NAME)
